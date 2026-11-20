package verify

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"github.com/Warewave-Technology/gaia/internal/evidence"
)

// Verdicts (OK / WARNING / CRITICAL / UNKNOWN, exit codes 0 / 1 / 2 / 3).
const (
	OK       = "OK"
	Warning  = "WARNING"
	Critical = "CRITICAL"
	Unknown  = "UNKNOWN"
)

// Worst combines verdicts: CRITICAL > UNKNOWN > WARNING > OK; no verdicts at all is UNKNOWN.
func Worst(vs ...string) string {
	if len(vs) == 0 {
		return Unknown
	}
	rank := map[string]int{OK: 0, Warning: 1, Unknown: 2, Critical: 3}
	w := OK
	for _, v := range vs {
		r, ok := rank[v]
		if !ok {
			r, v = 2, Unknown
		}
		if r > rank[w] {
			w = v
		}
	}
	return w
}

// ExitCode maps a verdict to 0 / 1 / 2 / 3; anything else is 3.
func ExitCode(v string) int {
	switch v {
	case OK:
		return 0
	case Warning:
		return 1
	case Critical:
		return 2
	}
	return 3
}

// Options of one verification.
type Options struct {
	Config      *Config
	Image       string // the restored image; never written
	ImageFormat string // raw (default) or qcow2
	// Disks, for a guest with several disks, replaces Image: every restored disk, the boot disk first. None is
	// ever written.
	Disks       []DiskImage
	RestoreJSON string  // optional: gaia-backup restore -json output, for the restore time (Proven RTO)
	RestoreS    float64 // optional: the restore time in seconds, when the caller restored the disks itself
	WorkDir     string  // for the overlay; qemu must be able to use it (e.g. /var/lib/libvirt/images/gaia-verify)
	SerialDir   string  // for the serial log (e.g. /var/log/libvirt/qemu)
	EvidenceDir string  // bundles go to EvidenceDir/<bundle id>
	Ledger      string
	Key         ed25519.PrivateKey // nil: the bundle is written but not sealed; the caller seals it (gaia server)
	Socket      string             // libvirtd socket
	Self        string             // this binary, run as `Self probe ...` inside the sandbox's network namespace
	Log         io.Writer          // progress lines
	// Slot is the sandbox slot the guest runs in (ADR-007 C4): its own network namespace, bridge, dnsmasq, overlay
	// and address range, 10.254.<slot>.0/24, so guests of parallel tests never share an address or a link.
	Slot int
	// For callers that do more than verify (the migration check): steps that ran before,
	// recorded as checks ahead of the VM's own; files to add to the bundle (bundle path ->
	// local file); and, when there is no image (Image == ""), why the guest was not started.
	PreChecks  []CheckResult
	Extra      map[string]string
	SkipReason string
}

// DiskImage is one restored disk of the guest.
type DiskImage struct {
	Path   string `json:"path"`
	Format string `json:"format"` // raw (default) or qcow2
	Target string `json:"target"` // the guest's device name (vda, vdb, sda...); default from the disk bus and order
}

// CheckResult is the outcome of one check. Seconds counts from the moment the guest's vCPUs started.
type CheckResult struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Verdict  string  `json:"verdict"`
	Seconds  float64 `json:"seconds"`
	Attempts int     `json:"attempts,omitempty"`
	Detail   string  `json:"detail"`
}

// Image describes the verified image and whether it stayed untouched.
type Image struct {
	Path         string `json:"path"`
	Format       string `json:"format"`
	Target       string `json:"target,omitempty"`
	Bytes        int64  `json:"bytes"`
	SHA256Before string `json:"sha256_before"`
	SHA256After  string `json:"sha256_after"`
	Unchanged    *bool  `json:"unchanged"`
}

// Sandbox records how the guest was isolated.
type Sandbox struct {
	Domain       string   `json:"domain"`
	Network      string   `json:"network"`
	Bridge       string   `json:"bridge"`
	Netns        string   `json:"netns"`
	Slot         int      `json:"slot"`
	ProbeAddress string   `json:"probe_address"`
	GuestIP      string   `json:"guest_ip"`
	Virt         string   `json:"virt"` // kvm, or qemu (emulated, no /dev/kvm)
	Libvirt      string   `json:"libvirt"`
	QEMU         string   `json:"qemu"`
	HostAddrs    []string `json:"host_addresses_on_bridge"` // must be empty: the guest cannot reach the host
	NetnsRoutes  []string `json:"netns_routes"`             // must have no default route: no uplink
	Forwarding   string   `json:"netns_ip_forward"`         // must be 0: the namespace forwards nothing (not to another slot)
	Isolated     bool     `json:"isolated"`
}

// Result is result.json of a bundle.
type Result struct {
	Format      string             `json:"format"`
	BundleID    string             `json:"bundle_id"`
	VM          string             `json:"vm"`
	Verdict     string             `json:"verdict"`
	Checks      []CheckResult      `json:"checks"`
	Image       Image              `json:"image"`            // the boot disk
	Images      []Image            `json:"images,omitempty"` // every disk, when the guest has more than one
	Sandbox     Sandbox            `json:"sandbox"`
	Timings     map[string]float64 `json:"timings_s"`
	Shutdown    string             `json:"shutdown"`
	Screenshots []string           `json:"screenshots"`
	Errors      []string           `json:"errors"`
	Started     time.Time          `json:"started"`
	Finished    time.Time          `json:"finished"`
	// Filled after sealing (not part of the bundle's result.json).
	BundleDir    string `json:"bundle_dir,omitempty"`
	BundleSHA256 string `json:"bundle_sha256,omitempty"`
	LedgerSeq    int    `json:"ledger_seq,omitempty"`
}

// MaxSlots is the most sandbox slots there are (ADR-007 C4: at most 8).
const MaxSlots = 8

// slotNet is a slot's addresses: the probe's (the namespace's), and the guests' DHCP range. dnsmasq hands the range
// out in order (--dhcp-sequential-ip), so a slot's guest is .10.
func slotNet(slot int) (probe, from, to string) {
	return fmt.Sprintf("10.254.%d.1", slot), fmt.Sprintf("10.254.%d.10", slot), fmt.Sprintf("10.254.%d.99", slot)
}

type run struct {
	o        Options
	c        *Config
	r        *Result
	dir      string // bundle dir
	l        *libvirt.Libvirt
	dom      libvirt.Domain
	domUp    bool
	net      libvirt.Network
	netUp    bool
	nsUp     bool
	dnsmasq  *exec.Cmd
	disks    []DiskImage
	overlays []string
	serial   string
	leases   string
	work     string
	log      bytes.Buffer // sandbox.log: every command and its output
	t0       time.Time    // vCPUs started
}

// Run verifies one image and seals the evidence. A harness failure still produces a
// sealed bundle, with verdict UNKNOWN and the error in it.
func Run(ctx context.Context, o Options) (*Result, error) {
	short := randHex(3)
	start := time.Now().UTC()
	r := &Result{Format: "gaia-verify-result/v1", VM: o.Config.VM, Verdict: Unknown, Checks: []CheckResult{},
		Timings: map[string]float64{}, Screenshots: []string{}, Errors: []string{}, Started: start,
		BundleID: fmt.Sprintf("%s-%s-%s", o.Config.VM, start.Format("20060102T150405Z"), short)}
	x := &run{o: o, c: o.Config, r: r, dir: filepath.Join(o.EvidenceDir, r.BundleID)}
	if err := os.MkdirAll(x.dir, 0o755); err != nil {
		return nil, err
	}
	x.work = filepath.Join(o.WorkDir, "gaia-verify-"+short)
	x.disks = o.Disks
	if len(x.disks) == 0 && o.Image != "" {
		x.disks = []DiskImage{{Path: o.Image, Format: o.ImageFormat}}
	}
	for i := range x.disks {
		d := &x.disks[i]
		if d.Format == "" {
			d.Format = "raw"
		}
		if d.Target == "" {
			d.Target = defaultTarget(o.Config.Machine.DiskBus, i)
		}
		name := "overlay.qcow2"
		if i > 0 {
			name = fmt.Sprintf("overlay-%s.qcow2", d.Target)
		}
		x.overlays = append(x.overlays, filepath.Join(x.work, name))
	}
	x.leases = filepath.Join(x.work, "dnsmasq.leases")
	x.serial = filepath.Join(o.SerialDir, "gaia-verify-"+short+"-serial.log")
	sb := &r.Sandbox
	sb.Domain, sb.Network, sb.Bridge, sb.Netns = "gaia-verify-"+short, "gaia-verify-"+short, "gvb-"+short, "gaia-verify-"+short
	if o.Slot < 0 || o.Slot >= MaxSlots {
		return nil, fmt.Errorf("slot %d: 0-%d", o.Slot, MaxSlots-1)
	}
	probe, _, _ := slotNet(o.Slot)
	sb.Slot, sb.ProbeAddress, sb.HostAddrs, sb.NetnsRoutes = o.Slot, probe, []string{}, []string{}

	if err := x.verify(ctx); err != nil {
		r.Errors = append(r.Errors, err.Error())
		x.logf("error: %v", err)
	}
	x.teardown()
	x.finish()
	if err := x.seal(); err != nil {
		return r, fmt.Errorf("sealing evidence: %w", err)
	}
	return r, nil
}

func (x *run) logf(f string, a ...any) {
	line := fmt.Sprintf("[%s] %s", time.Now().UTC().Format("15:04:05.000"), fmt.Sprintf(f, a...))
	x.log.WriteString(line + "\n")
	if x.o.Log != nil {
		fmt.Fprintln(x.o.Log, line)
	}
}

// cmd runs a command, logs it with its output, and fails on a non-zero exit.
func (x *run) cmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	x.logf("$ %s %s\n%s", name, strings.Join(args, " "), strings.TrimRight(string(out), "\n"))
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (x *run) verify(ctx context.Context) error {
	c, r, sb := x.c, x.r, &x.r.Sandbox
	cfg, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(filepath.Join(x.dir, "config.json"), append(cfg, '\n'), 0o644); err != nil {
		return err
	}
	r.Checks = append(r.Checks, x.o.PreChecks...)
	for dst, src := range x.o.Extra {
		if strings.Contains(dst, "..") || filepath.IsAbs(dst) {
			return fmt.Errorf("extra file %q: bad bundle path", dst)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("extra file %s: %w", src, err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(x.dir, dst)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(x.dir, dst), b, 0o644); err != nil {
			return err
		}
	}
	if len(x.disks) == 0 {
		for _, k := range c.Checks {
			r.Checks = append(r.Checks, CheckResult{Name: k.Name, Type: k.Type, Verdict: Unknown, Detail: "not run: " + x.o.SkipReason})
		}
		return nil
	}
	if x.o.RestoreS > 0 {
		r.Timings["restore"] = x.o.RestoreS
	}
	if x.o.RestoreJSON != "" {
		b, err := os.ReadFile(x.o.RestoreJSON)
		if err != nil {
			return err
		}
		os.WriteFile(filepath.Join(x.dir, "restore.json"), b, 0o644)
		var rj struct {
			Stats struct {
				Seconds float64 `json:"seconds"`
			} `json:"stats"`
		}
		if json.Unmarshal(b, &rj) == nil && rj.Stats.Seconds > 0 {
			r.Timings["restore"] = rj.Stats.Seconds
		}
	}

	// The images: hashed now and again at the end; each is only ever a read-only backing file.
	t := time.Now()
	for _, d := range x.disks {
		sum, n, err := fileSHA256(d.Path)
		if err != nil {
			return fmt.Errorf("image %s: %w", d.Target, err)
		}
		r.Images = append(r.Images, Image{Path: d.Path, Format: d.Format, Target: d.Target, Bytes: n, SHA256Before: sum})
		x.logf("image %s (%s): sha256 %s (%d bytes)", d.Path, d.Target, sum, n)
	}
	r.Image = r.Images[0]
	if len(x.disks) == 1 {
		r.Image.Target, r.Images = "", nil // one disk: result.json as before
	}
	r.Timings["image_hash"] = time.Since(t).Seconds()

	t = time.Now()
	l := libvirt.NewWithDialer(dialers.NewLocal(dialers.WithSocket(x.o.Socket)))
	if err := l.ConnectToURI(libvirt.QEMUSystem); err != nil {
		return fmt.Errorf("libvirt: %w", err)
	}
	x.l = l
	if lv, err := l.ConnectGetLibVersion(); err == nil {
		sb.Libvirt = versionString(lv)
	}
	if hv, err := l.ConnectGetVersion(); err == nil {
		sb.QEMU = versionString(hv)
	}
	if err := x.setupNetwork(); err != nil {
		return err
	}
	if err := os.MkdirAll(x.work, 0o755); err != nil {
		return err
	}
	for i, d := range x.disks {
		if _, err := x.cmd("qemu-img", "create", "-q", "-f", "qcow2", "-F", d.Format, "-b", absPath(d.Path), x.overlays[i]); err != nil {
			return err
		}
	}
	domXML := x.domainXML()
	os.WriteFile(filepath.Join(x.dir, "domain.xml"), []byte(domXML), 0o644)
	var err error
	if x.dom, err = l.DomainCreateXML(domXML, libvirt.DomainStartPaused); err != nil {
		return fmt.Errorf("create sandbox domain: %w", err)
	}
	x.domUp = true
	if err := x.checkIsolation(); err != nil {
		return err
	}
	r.Timings["sandbox_setup"] = time.Since(t).Seconds()

	// Boot.
	if err := l.DomainResume(x.dom); err != nil {
		return fmt.Errorf("start guest: %w", err)
	}
	x.t0 = time.Now()
	x.logf("guest started (%s)", sb.Virt)
	var boot Check
	for _, k := range c.Checks {
		if k.IsBoot() {
			boot = k
		}
	}
	var br CheckResult
	if boot.Type == CheckBootSerial {
		br = x.waitBoot(ctx, boot)
	} else {
		br = x.waitNetBoot(ctx, boot)
	}
	r.Checks = append(r.Checks, br)
	if br.Verdict == OK {
		r.Timings["boot"] = br.Seconds
	}
	x.screenshot("boot")

	// Probes, from inside the sandbox network.
	probes := slices.DeleteFunc(slices.Clone(c.Checks), func(k Check) bool { return k.IsBoot() })
	if br.Verdict != OK {
		for _, k := range probes {
			r.Checks = append(r.Checks, CheckResult{Name: k.Name, Type: k.Type, Verdict: Unknown, Detail: "not run: the guest did not boot"})
		}
	} else if len(probes) > 0 {
		ip, err := x.waitLease(ctx)
		for _, k := range probes {
			if err != nil {
				r.Checks = append(r.Checks, CheckResult{Name: k.Name, Type: k.Type, Verdict: Critical, Seconds: time.Since(x.t0).Seconds(), Detail: err.Error()})
				continue
			}
			r.Checks = append(r.Checks, x.probe(ctx, k, ip, x.c.ProbeTimeoutS))
		}
	}
	if Worst(checkVerdicts(r.Checks)...) == OK {
		// Seconds since the vCPUs started, so only the guest's own checks count; pre-checks carry
		// their own durations (a download, a conversion) and are not part of it.
		ready := 0.0
		for _, k := range r.Checks[len(x.o.PreChecks):] {
			ready = max(ready, k.Seconds)
		}
		r.Timings["ready"] = ready
		if boot.Type != CheckBootSerial && c.SettleS > 0 {
			ip := x.r.Sandbox.GuestIP
			st := settle(ctx, boot, time.Duration(c.SettleS)*time.Second, 5*time.Second,
				func(timeoutS int) CheckResult { return x.probe(ctx, boot, ip, timeoutS) }, x.alive)
			st.Seconds = time.Since(x.t0).Seconds()
			x.logf("check %s: %s (%s)", st.Name, st.Verdict, st.Detail)
			r.Checks = append(r.Checks, st)
		}
	}
	x.screenshot("end")
	x.shutdown(br.Verdict == OK)
	return nil
}

func (x *run) setupNetwork() error {
	sb := &x.r.Sandbox
	// No <forward>: isolated. No <ip>: the host has no address on the bridge, and libvirt runs no DHCP or DNS for it.
	netXML := fmt.Sprintf(`<network><name>%s</name><bridge name='%s' stp='off' delay='0'/></network>`, sb.Network, sb.Bridge)
	os.WriteFile(filepath.Join(x.dir, "network.xml"), []byte(netXML), 0o644)
	n, err := x.l.NetworkCreateXML(netXML)
	if err != nil {
		return fmt.Errorf("create sandbox network: %w", err)
	}
	x.net, x.netUp = n, true
	host, ns := "gvh-"+strings.TrimPrefix(sb.Bridge, "gvb-"), "gvn-"+strings.TrimPrefix(sb.Bridge, "gvb-")
	steps := [][]string{
		{"sysctl", "-q", "-w", "net.ipv6.conf." + sb.Bridge + ".disable_ipv6=1"},
		{"ip", "netns", "add", sb.Netns},
		{"ip", "link", "add", host, "type", "veth", "peer", "name", ns},
		{"sysctl", "-q", "-w", "net.ipv6.conf." + host + ".disable_ipv6=1"},
		{"ip", "link", "set", host, "master", sb.Bridge, "up"},
		{"ip", "link", "set", ns, "netns", sb.Netns},
		{"ip", "-n", sb.Netns, "addr", "add", sb.ProbeAddress + "/24", "dev", ns},
		{"ip", "-n", sb.Netns, "link", "set", ns, "up"},
		{"ip", "-n", sb.Netns, "link", "set", "lo", "up"},
	}
	for i, s := range steps {
		if i == 1 {
			x.nsUp = true
		}
		if _, err := x.cmd(s[0], s[1:]...); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(x.work, 0o755); err != nil {
		return err
	}
	_, dhcpFrom, dhcpTo := slotNet(sb.Slot)
	x.dnsmasq = exec.Command("ip", "netns", "exec", sb.Netns, "dnsmasq", "--keep-in-foreground", "--conf-file=/dev/null",
		"--no-resolv", "--no-hosts", "--port=0", "--bind-interfaces", "--interface="+ns, "--except-interface=lo",
		"--dhcp-range="+dhcpFrom+","+dhcpTo+",255.255.255.0,1h", "--dhcp-sequential-ip", "--dhcp-leasefile="+x.leases,
		"--pid-file="+filepath.Join(x.work, "dnsmasq.pid"), "--log-dhcp", "--log-facility="+filepath.Join(x.work, "dnsmasq.log"),
		"--user=root", "--group=root")
	x.logf("$ %s", strings.Join(x.dnsmasq.Args, " "))
	if err := x.dnsmasq.Start(); err != nil {
		return fmt.Errorf("dnsmasq: %w", err)
	}
	return nil
}

func (x *run) checkIsolation() error {
	sb := &x.r.Sandbox
	out, err := x.cmd("ip", "-o", "addr", "show", "dev", sb.Bridge)
	if err != nil {
		return err
	}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(ln); len(f) >= 4 && (f[2] == "inet" || f[2] == "inet6") {
			sb.HostAddrs = append(sb.HostAddrs, f[3])
		}
	}
	out, err = x.cmd("ip", "-n", sb.Netns, "route", "show", "table", "all")
	if err != nil {
		return err
	}
	defaultRoute := false
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			sb.NetnsRoutes = append(sb.NetnsRoutes, ln)
			defaultRoute = defaultRoute || strings.HasPrefix(ln, "default")
		}
	}
	if out, err = x.cmd("ip", "netns", "exec", sb.Netns, "cat", "/proc/sys/net/ipv4/ip_forward"); err != nil {
		return err
	}
	sb.Forwarding = strings.TrimSpace(out)
	sb.Isolated = len(sb.HostAddrs) == 0 && !defaultRoute && sb.Forwarding == "0"
	if !sb.Isolated {
		return fmt.Errorf("sandbox is not isolated (host addresses %v, netns routes %v, ip_forward %q): not booting the guest",
			sb.HostAddrs, sb.NetnsRoutes, sb.Forwarding)
	}
	return nil
}

func (x *run) domainXML() string {
	c, m, sb := x.c, x.c.Machine, &x.r.Sandbox
	sb.Virt = "kvm"
	if _, err := os.Stat("/dev/kvm"); err != nil {
		sb.Virt = "qemu"
	}
	cpu := "<cpu mode='host-passthrough'/>"
	if sb.Virt != "kvm" {
		cpu = "<cpu mode='custom'><model>max</model></cpu>"
	}
	firmware := ""
	if m.Firmware == "efi" {
		firmware = " firmware='efi'"
	}
	osXML := fmt.Sprintf("<os%s><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os><features><acpi/><apic/></features>", firmware)
	emulator, video := "/usr/bin/qemu-system-x86_64", "vga"
	if m.Arch == "aarch64" {
		osXML = fmt.Sprintf("<os%s><type arch='aarch64' machine='virt'>hvm</type><boot dev='hd'/></os><features><acpi/><gic version='3'/></features>", firmware)
		emulator, video = "/usr/bin/qemu-system-aarch64", "virtio"
	}
	var disks strings.Builder
	for i, d := range x.disks {
		fmt.Fprintf(&disks, "<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='%s' bus='%s'/></disk>\n    ",
			x.overlays[i], d.Target, m.DiskBus)
	}
	controller := ""
	if m.DiskBus == "scsi" {
		controller = "<controller type='scsi' model='virtio-scsi'/>"
	}
	return fmt.Sprintf(`<domain type='%s'>
  <name>%s</name>
  <description>Gaia verification sandbox for %s (transient)</description>
  <memory unit='MiB'>%d</memory>
  <vcpu>%d</vcpu>
  %s
  %s
  <on_poweroff>destroy</on_poweroff><on_reboot>restart</on_reboot><on_crash>destroy</on_crash>
  <devices>
    <emulator>%s</emulator>
    %s
    %s<interface type='network'><source network='%s'/><mac address='%s'/><model type='%s'/><rom enabled='no'/></interface>
    <serial type='file'><source path='%s'/><target port='0'/></serial>
    <graphics type='vnc'><listen type='none'/></graphics>
    <video><model type='%s'/></video>
  </devices>
</domain>
`, sb.Virt, sb.Domain, xmlText(c.VM), m.MemoryMiB, m.VCPUs, osXML, cpu, emulator, controller, disks.String(),
		sb.Network, m.MAC, m.NICModel, x.serial, video)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\r`)

func (x *run) waitBoot(ctx context.Context, k Check) CheckResult {
	res := CheckResult{Name: k.Name, Type: k.Type, Verdict: Critical}
	re := regexp.MustCompile(k.Pattern)
	deadline := x.t0.Add(time.Duration(x.c.BootTimeoutS) * time.Second)
	for {
		res.Attempts++
		if b, err := os.ReadFile(x.serial); err == nil {
			text := ansi.ReplaceAllString(string(b), "")
			if loc := re.FindStringIndex(text); loc != nil {
				res.Verdict, res.Seconds = OK, time.Since(x.t0).Seconds()
				lineStart := strings.LastIndex(text[:loc[0]], "\n") + 1
				res.Detail = fmt.Sprintf("serial console matched %q: %q", k.Pattern, strings.TrimSpace(text[lineStart:loc[1]]))
				x.logf("boot reached after %.3f s", res.Seconds)
				return res
			}
		}
		if st, _, err := x.l.DomainGetState(x.dom, 0); err != nil || libvirt.DomainState(st) == libvirt.DomainShutoff ||
			libvirt.DomainState(st) == libvirt.DomainCrashed {
			res.Seconds, res.Detail = time.Since(x.t0).Seconds(), fmt.Sprintf("the guest stopped before boot was reached (state %d, %v)", st, err)
			x.domUp = err == nil
			return res
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			res.Seconds = time.Since(x.t0).Seconds()
			res.Detail = fmt.Sprintf("no match for %q on the serial console within %d s", k.Pattern, x.c.BootTimeoutS)
			x.logf("boot not reached within %d s", x.c.BootTimeoutS)
			return res
		}
		time.Sleep(time.Second)
	}
}

// waitNetBoot decides boot with a probe (a guest without a serial console, e.g. Windows): the guest
// must take a DHCP lease and then pass the probe, both within the boot timeout. Firmware takes no
// lease (the NIC's option ROM is off), so the lease already comes from the booted OS.
func (x *run) waitNetBoot(ctx context.Context, k Check) CheckResult {
	res := CheckResult{Name: k.Name, Type: k.Type, Verdict: Critical}
	deadline := x.t0.Add(time.Duration(x.c.BootTimeoutS) * time.Second)
	ip, err := x.leaseBy(ctx, deadline, func() error {
		if st, _, err := x.l.DomainGetState(x.dom, 0); err != nil || libvirt.DomainState(st) == libvirt.DomainShutoff ||
			libvirt.DomainState(st) == libvirt.DomainCrashed {
			x.domUp = err == nil
			return fmt.Errorf("the guest stopped before boot was reached (state %d, %v)", st, err)
		}
		return nil
	})
	if err != nil {
		res.Seconds, res.Detail = time.Since(x.t0).Seconds(), err.Error()+netBootWhy(k.Type)
		x.logf("boot not reached: %v", err)
		return res
	}
	left := max(1, int(time.Until(deadline).Seconds()))
	res = x.probe(ctx, k, ip, left)
	if res.Verdict == OK {
		res.Detail = fmt.Sprintf("%s (DHCP lease %s, then the probe)", res.Detail, ip)
		x.logf("boot reached after %.3f s", res.Seconds)
	} else {
		res.Detail += netBootWhy(k.Type)
	}
	return res
}

// alive is an error once the sandbox guest stopped or crashed.
func (x *run) alive() error {
	st, _, err := x.l.DomainGetState(x.dom, 0)
	if err != nil {
		return err
	}
	if s := libvirt.DomainState(st); s == libvirt.DomainShutoff || s == libvirt.DomainCrashed {
		return fmt.Errorf("the guest stopped (libvirt state %d)", st)
	}
	return nil
}

// settle checks that a guest booted by a network probe stays up: the boot probe must keep passing, every `every`,
// for d after every check passed. One answer proves the guest got that far, not that it stays: a Windows whose
// first-boot scripts fail restarts on every boot and answers WinRM for a few seconds each time (T23).
func settle(ctx context.Context, k Check, d, every time.Duration, probe func(timeoutS int) CheckResult, alive func() error) CheckResult {
	res := CheckResult{Name: "stable", Type: k.Type, Verdict: OK}
	start := time.Now()
	for n := 1; ; n++ {
		if err := alive(); err != nil {
			res.Verdict, res.Detail = Critical, fmt.Sprintf("the guest stopped %.0f s after it passed every check: %v", time.Since(start).Seconds(), err)
			return res
		}
		if p := probe(max(1, int(every.Seconds()))); p.Verdict != OK {
			res.Verdict = p.Verdict
			res.Detail = fmt.Sprintf("%s stopped answering %.0f s after it passed (probe %d, one every %s for %s): %s. "+
				"A guest that restarts itself answers for a few seconds only (a Windows whose first-boot scripts fail "+
				"restarts on every boot)", k.Name, time.Since(start).Seconds(), n, every, d, p.Detail)
			return res
		} else if time.Since(start) >= d {
			res.Detail = fmt.Sprintf("%s kept answering for %s after every check passed (%d probes, one every %s)", k.Name, d, n, every)
			return res
		}
		select {
		case <-ctx.Done():
			res.Verdict, res.Detail = Unknown, "not finished: "+ctx.Err().Error()
			return res
		case <-time.After(every):
		}
	}
}

// netBootWhy says why a boot probe cannot pass on a guest that did not boot (T11's rule, ADR-005 §3): the report
// must say what the check rests on, not only that it failed.
func netBootWhy(typ string) string {
	switch typ {
	case CheckWinRM:
		return ". WinRM answers only from a running Windows: the WinRM service listens on 5985 through HTTP.sys once " +
			"Windows has started its network stack and taken a DHCP lease, so a guest stopped in firmware, the boot " +
			"loader, a stop error or the Recovery Environment takes no lease or never answers on 5985"
	case CheckRDP:
		return ". Remote Desktop answers only from a running Windows with RDP enabled: TermService accepts the X.224 " +
			"handshake once Windows has taken a DHCP lease, so a guest that did not boot cannot confirm it"
	}
	return ""
}

// waitLease waits for the guest's DHCP lease (by its MAC) in dnsmasq's lease file.
func (x *run) waitLease(ctx context.Context) (string, error) {
	return x.leaseBy(ctx, time.Now().Add(time.Duration(x.c.ProbeTimeoutS)*time.Second), nil)
}

// leaseBy waits until deadline for the lease; alive, if set, stops the wait early with its error.
func (x *run) leaseBy(ctx context.Context, deadline time.Time, alive func() error) (string, error) {
	mac := strings.ToLower(x.c.Machine.MAC)
	if ip := x.r.Sandbox.GuestIP; ip != "" {
		return ip, nil
	}
	for {
		if b, err := os.ReadFile(x.leases); err == nil {
			for _, ln := range strings.Split(string(b), "\n") {
				if f := strings.Fields(ln); len(f) >= 3 && strings.ToLower(f[1]) == mac {
					x.r.Sandbox.GuestIP = f[2]
					x.logf("guest %s has %s after %.3f s", mac, f[2], time.Since(x.t0).Seconds())
					return f[2], nil
				}
			}
		}
		if alive != nil {
			if err := alive(); err != nil {
				return "", err
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if alive != nil {
				return "", fmt.Errorf("the guest took no DHCP lease within the %d s boot timeout", x.c.BootTimeoutS)
			}
			return "", fmt.Errorf("the guest took no DHCP lease within %d s of booting", x.c.ProbeTimeoutS)
		}
		time.Sleep(time.Second)
	}
}

func (x *run) probe(ctx context.Context, k Check, ip string, timeoutS int) CheckResult {
	res := CheckResult{Name: k.Name, Type: k.Type, Verdict: Unknown}
	kj, _ := json.Marshal(k)
	args := []string{"netns", "exec", x.r.Sandbox.Netns, x.o.Self, "probe", "-check", string(kj), "-host", ip,
		"-timeout", fmt.Sprint(timeoutS)}
	out, err := exec.CommandContext(ctx, "ip", args...).Output()
	x.logf("$ ip %s\n%s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	var pr ProbeResult
	if jerr := json.Unmarshal(out, &pr); jerr != nil {
		res.Seconds, res.Detail = time.Since(x.t0).Seconds(), fmt.Sprintf("probe runner failed: %v %v", err, jerr)
		return res // the probe did not run: UNKNOWN, not CRITICAL
	}
	res.Seconds, res.Attempts, res.Detail = time.Since(x.t0).Seconds(), pr.Attempts, pr.Detail
	res.Verdict = Critical
	if pr.OK {
		res.Verdict = OK
	}
	x.logf("check %s: %s (%s)", k.Name, res.Verdict, pr.Detail)
	return res
}

// screenshot captures the guest's console (virDomainScreenshot) through `virsh screenshot`: go-libvirt's
// stream handling for this call either misdecodes the reply or deadlocks (seen in the T7 mock), so per
// ADR-004 §5 it falls back to virsh as a subprocess.
func (x *run) screenshot(label string) {
	if !x.domUp {
		return
	}
	tmp := filepath.Join(x.work, "screenshot-"+label+".ppm")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "virsh", "-c", "qemu:///system", "screenshot", x.r.Sandbox.Domain, "--file", tmp, "--screen", "0").CombinedOutput()
	x.logf("$ virsh screenshot %s --file %s\n%s", x.r.Sandbox.Domain, tmp, strings.TrimSpace(string(out)))
	if err != nil {
		x.r.Errors = append(x.r.Errors, fmt.Sprintf("screenshot %s: %v: %s", label, err, strings.TrimSpace(string(out))))
		return
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		x.r.Errors = append(x.r.Errors, fmt.Sprintf("screenshot %s: %v", label, err))
		return
	}
	// qemu answers in PNG (qemu >= 7.1 with libvirt >= 8.?) or PPM; PPM is converted, anything else is not a screenshot.
	name := "screenshot-" + label + ".png"
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		png, err := ppmToPNG(data)
		if err != nil {
			x.r.Errors = append(x.r.Errors, fmt.Sprintf("screenshot %s: neither PNG nor PPM: %v", label, err))
			return
		}
		data = png
	}
	if err := os.WriteFile(filepath.Join(x.dir, name), data, 0o644); err != nil {
		x.r.Errors = append(x.r.Errors, err.Error())
		return
	}
	x.r.Screenshots = append(x.r.Screenshots, name)
}

func (x *run) shutdown(booted bool) {
	if !x.domUp {
		return
	}
	t := time.Now()
	defer func() { x.r.Timings["shutdown"] = time.Since(t).Seconds() }()
	if booted {
		if err := x.l.DomainShutdown(x.dom); err == nil {
			for deadline := time.Now().Add(time.Duration(x.c.ShutdownTimeoutS) * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
				if _, _, err := x.l.DomainGetState(x.dom, 0); err != nil { // a transient domain is gone once it is off
					x.r.Shutdown, x.domUp = "acpi", false
					return
				}
			}
		}
	}
	x.r.Shutdown = "forced"
	if !booted {
		x.r.Shutdown = "forced (not booted)"
	}
	if err := x.l.DomainDestroy(x.dom); err != nil {
		x.r.Errors = append(x.r.Errors, fmt.Sprintf("destroy: %v", err))
	}
	x.domUp = false
}

// teardown removes everything the sandbox created; failures are recorded, not fatal.
func (x *run) teardown() {
	if x.domUp {
		x.shutdown(false)
	}
	if x.dnsmasq != nil && x.dnsmasq.Process != nil {
		x.dnsmasq.Process.Kill()
		x.dnsmasq.Wait()
	}
	if x.nsUp {
		if _, err := x.cmd("ip", "netns", "del", x.r.Sandbox.Netns); err != nil {
			x.r.Errors = append(x.r.Errors, err.Error())
		}
	}
	if x.netUp {
		if err := x.l.NetworkDestroy(x.net); err != nil {
			x.r.Errors = append(x.r.Errors, fmt.Sprintf("network destroy: %v", err))
		}
	}
	if x.l != nil {
		x.l.Disconnect()
	}
	for src, dst := range map[string]string{x.serial: "serial.log", x.leases: "dnsmasq.leases", filepath.Join(x.work, "dnsmasq.log"): "dnsmasq.log"} {
		if b, err := os.ReadFile(src); err == nil {
			os.WriteFile(filepath.Join(x.dir, dst), b, 0o644)
		}
	}
	os.Remove(x.serial)
	os.RemoveAll(x.work)
}

func (x *run) finish() {
	r := x.r
	imgs := []*Image{&r.Image}
	if len(r.Images) > 0 {
		imgs = nil
		for i := range r.Images {
			imgs = append(imgs, &r.Images[i])
		}
	}
	for _, im := range imgs {
		if im.SHA256Before == "" {
			continue
		}
		name := "image_unchanged"
		if im.Target != "" {
			name += "_" + im.Target
		}
		if sum, _, err := fileSHA256(im.Path); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("image after: %v", err))
		} else {
			im.SHA256After = sum
			same := sum == im.SHA256Before
			im.Unchanged = &same
			v, d := OK, "sha256 before and after are equal"
			if !same {
				v, d = Critical, "the restored image changed during verification"
			}
			r.Checks = append(r.Checks, CheckResult{Name: name, Type: "image_hash", Verdict: v, Detail: d})
		}
	}
	if len(r.Images) > 0 {
		r.Image = r.Images[0]
	}
	verdicts := checkVerdicts(r.Checks)
	if len(r.Errors) > 0 && len(verdicts) > 0 {
		verdicts = append(verdicts, Unknown) // a harness error: what was not checked is not OK
	}
	if len(r.Checks) < len(x.o.PreChecks)+len(x.c.Checks)+max(1, len(x.disks)) { // + image_unchanged per disk
		verdicts = append(verdicts, Unknown)
	}
	r.Verdict = Worst(verdicts...)
	if rs, ok := r.Timings["restore"]; ok && r.Verdict == OK {
		r.Timings["proven_rto"] = rs + r.Timings["ready"] // restore + time until every check passed
	}
	r.Finished = time.Now().UTC()
	r.Timings["total"] = r.Finished.Sub(r.Started).Seconds()
}

func (x *run) seal() error {
	r := x.r
	os.WriteFile(filepath.Join(x.dir, "sandbox.log"), x.log.Bytes(), 0o644)
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(x.dir, "result.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if x.o.Key == nil {
		r.BundleDir = x.dir // the caller seals it
		return nil
	}
	h, err := evidence.Seal(x.dir, r.BundleID, map[string]string{"vm": r.VM, "verdict": r.Verdict}, x.o.Key)
	if err != nil {
		return err
	}
	e, err := evidence.Append(x.o.Ledger, evidence.Entry{BundleID: r.BundleID, BundleSHA256: h, Subject: r.VM, Verdict: r.Verdict}, x.o.Key)
	if err != nil {
		return err
	}
	r.BundleDir, r.BundleSHA256, r.LedgerSeq = x.dir, h, e.Seq
	return nil
}

// defaultTarget names the i-th disk on a bus the way libvirt does: vda, vdb... or sda, sdb...
func defaultTarget(bus string, i int) string {
	prefix := "vd"
	if bus == "sata" || bus == "scsi" {
		prefix = "sd"
	}
	return prefix + string(rune('a'+i))
}

func checkVerdicts(cs []CheckResult) []string {
	var v []string
	for _, c := range cs {
		v = append(v, c.Verdict)
	}
	return v
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func versionString(v uint64) string { return fmt.Sprintf("%d.%d.%d", v/1000000, v/1000%1000, v%1000) }

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

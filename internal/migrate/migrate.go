// Package migrate is the migration check, stage 1 (cold) of ADR-003: for each
// powered-off VM of a vSphere source it reads the inventory and the VM's files
// with read-only calls (package vsphereread), converts the VM for KVM with
// virt-v2v (a separate process; Gaia proves, it does not move), boots the result
// in the verification sandbox (package verify), and seals one signed readiness
// bundle per VM: the source facts, the downloads, the conversion log, the guard's
// call log, the sandbox evidence and the verdict.
package migrate

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/verify"
	"github.com/Warewave-Technology/gaia/internal/vsphereread"
)

// Checks is the per-VM verification declaration: a template and checks on top.
type Checks struct {
	Template      string         `json:"template"`
	Checks        []verify.Check `json:"checks"`
	BootTimeoutS  int            `json:"boot_timeout_s,omitempty"`
	ProbeTimeoutS int            `json:"probe_timeout_s,omitempty"`
}

// ChecksFile maps VM names to their checks; "*" applies to VMs without an entry.
type ChecksFile map[string]Checks

// LoadChecks reads a ChecksFile (JSON).
func LoadChecks(p string) (ChecksFile, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var f ChecksFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return f, nil
}

// Options of one migration check run.
type Options struct {
	Source     *vsphereread.Client
	VMs        []string // names; empty = every powered-off VM that is not a template
	Checks     ChecksFile
	WorkDir    string // downloads go to WorkDir/<vm>/
	ImageDir   string // converted images go to ImageDir/<vm>/ (qemu must be able to read them)
	CallLog    string // the guard's call log file, copied into each bundle as it is at that point
	V2VTimeout time.Duration
	Verify     verify.Options // Key, EvidenceDir, Ledger, WorkDir, SerialDir, Socket, Self, Log
	Log        io.Writer
}

// Readiness is the outcome for one VM.
type Readiness struct {
	VM        string               `json:"vm"`
	Readiness string               `json:"readiness"` // the bundle's verdict
	BundleID  string               `json:"bundle_id"`
	LedgerSeq int                  `json:"ledger_seq"`
	Checks    []verify.CheckResult `json:"checks"`
	Timings   map[string]float64   `json:"timings_s"`
}

// Report is the outcome of a run.
type Report struct {
	Source    string           `json:"source"`
	Product   string           `json:"product"`
	Inventory []vsphereread.VM `json:"inventory"`
	VMs       []Readiness      `json:"vms"`
	Verdict   string           `json:"verdict"` // worst readiness; UNKNOWN when no VM was checked
	Calls     int              `json:"calls"`
	Blocked   int              `json:"blocked_calls"`
	Started   time.Time        `json:"started"`
	Finished  time.Time        `json:"finished"`
}

func (o Options) logf(f string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, "[%s] %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(f, a...))
	}
}

// Run checks every selected VM. Per-VM failures are verdicts in that VM's bundle;
// an error means the run itself could not proceed (e.g. no inventory).
func Run(ctx context.Context, o Options) (*Report, error) {
	rep := &Report{Source: o.Source.Host, Product: o.Source.Vim.ServiceContent.About.FullName, Started: time.Now().UTC(), VMs: []Readiness{}}
	inv, err := o.Source.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	rep.Inventory = inv
	o.logf("inventory: %d VMs on %s (%s)", len(inv), rep.Source, rep.Product)
	byName := map[string]vsphereread.VM{}
	for _, v := range inv {
		byName[v.Name] = v
	}
	names := o.VMs
	if len(names) == 0 {
		for _, v := range inv {
			if v.PowerState == "poweredOff" && !v.Template {
				names = append(names, v.Name)
			}
		}
	}
	for _, name := range names {
		v, found := byName[name]
		r, err := checkVM(ctx, o, name, v, found)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		rep.VMs = append(rep.VMs, *r)
	}
	var vs []string
	for _, r := range rep.VMs {
		vs = append(vs, r.Readiness)
	}
	rep.Verdict = verify.Worst(vs...)
	for _, c := range o.Source.Guard.Calls() {
		rep.Calls++
		if !c.Allowed {
			rep.Blocked++
		}
	}
	rep.Finished = time.Now().UTC()
	return rep, nil
}

type vmRun struct {
	o      Options
	name   string
	vm     vsphereread.VM
	work   string
	images string
	pre    []verify.CheckResult
	extra  map[string]string
	t      map[string]float64
}

func (x *vmRun) add(name, typ, verdict, detail string, start time.Time) {
	x.pre = append(x.pre, verify.CheckResult{Name: name, Type: typ, Verdict: verdict, Seconds: time.Since(start).Seconds(), Detail: detail})
	x.o.logf("%s: %s %s: %s", x.name, name, verdict, detail)
}

// notRun records the steps after a failed one as UNKNOWN: unchecked is never OK.
func (x *vmRun) notRun(why string, steps ...string) {
	for _, s := range steps {
		x.pre = append(x.pre, verify.CheckResult{Name: s, Type: "migration", Verdict: verify.Unknown, Detail: "not run: " + why})
	}
}

func checkVM(ctx context.Context, o Options, name string, v vsphereread.VM, found bool) (*Readiness, error) {
	x := &vmRun{o: o, name: name, vm: v, work: filepath.Join(o.WorkDir, name), images: filepath.Join(o.ImageDir, name),
		extra: map[string]string{}, t: map[string]float64{}}
	if err := os.MkdirAll(x.work, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(x.images, 0o755); err != nil {
		return nil, err
	}
	image, cfg, skip := x.prepare(ctx, found)
	if cfg == nil { // nothing to build a sandbox from: a minimal config so the bundle still records every step
		cfg = &verify.Config{VM: name, Template: "linux-boot", Machine: verify.Machine{MAC: "52:54:00:00:00:00"}}
		if err := resolveConfig(cfg); err != nil {
			return nil, err
		}
	}
	if o.CallLog != "" {
		x.extra["migration/calls.jsonl"] = o.CallLog
	}
	vo := o.Verify
	vo.Config, vo.Image, vo.ImageFormat, vo.PreChecks, vo.Extra, vo.SkipReason = cfg, image, "qcow2", x.pre, x.extra, skip
	res, err := verify.Run(ctx, vo)
	if err != nil {
		return nil, err
	}
	for k, v := range res.Timings {
		x.t[k] = v
	}
	return &Readiness{VM: name, Readiness: res.Verdict, BundleID: res.BundleID, LedgerSeq: res.LedgerSeq, Checks: res.Checks, Timings: x.t}, nil
}

var allSteps = []string{"source_vm", "cold", "layout", "download", "conversion"}

// prepare runs the migration steps; it returns the converted image and the sandbox config,
// or "" and the reason the guest cannot be started.
func (x *vmRun) prepare(ctx context.Context, found bool) (string, *verify.Config, string) {
	t := time.Now()
	invPath := filepath.Join(x.work, "inventory.json")
	b, _ := json.MarshalIndent(x.vm, "", "  ")
	os.WriteFile(invPath, append(b, '\n'), 0o644)
	x.extra["migration/inventory.json"] = invPath
	switch {
	case !found:
		x.add("source_vm", "inventory", verify.Unknown, "no VM of this name on the source", t)
		x.notRun("the VM was not found", allSteps[1:]...)
		return "", nil, "the VM was not found on the source"
	case x.vm.Unreadable != "":
		x.add("source_vm", "inventory", verify.Unknown, x.vm.Unreadable, t)
		x.notRun("the VM's configuration is unreadable", allSteps[1:]...)
		return "", nil, "the VM's configuration is unreadable"
	}
	x.add("source_vm", "inventory", verify.OK, fmt.Sprintf("%s, guest %s, %d vCPU, %d MiB, firmware %s, %d disk(s), %d NIC(s)",
		x.vm.VMX, x.vm.GuestID, x.vm.CPUs, x.vm.MemoryMiB, x.vm.Firmware, len(x.vm.Disks), len(x.vm.NICs)), t)

	t = time.Now()
	if x.vm.PowerState != "poweredOff" {
		x.add("cold", "power_state", verify.Unknown, "the VM is "+x.vm.PowerState+": stage 1 checks powered-off VMs (or clones) only", t)
		x.notRun("the VM is not powered off", allSteps[2:]...)
		return "", nil, "the VM is not powered off"
	}
	x.add("cold", "power_state", verify.OK, "poweredOff", t)

	t = time.Now()
	files, why := x.layout()
	if why != "" {
		x.add("layout", "files", verify.Unknown, why, t)
		x.notRun("unsupported layout", allSteps[3:]...)
		return "", nil, "unsupported layout: " + why
	}
	x.add("layout", "files", verify.OK, fmt.Sprintf("%d files to read, all in %s", len(files), path.Dir(x.vm.VMX)), t)

	t = time.Now()
	var downloads []vsphereread.Downloaded
	var total int64
	for _, f := range files {
		_, rel, _ := vsphereread.ParseDatastorePath(f.Name)
		d, err := x.o.Source.Download(ctx, f.Name, x.vm.Datacenter, filepath.Join(x.work, path.Base(rel)))
		downloads = append(downloads, d)
		if err != nil {
			x.add("download", "datastore_http", verify.Unknown, err.Error(), t)
			x.notRun("a download failed", "conversion")
			return "", nil, "a download failed"
		}
		total += d.Bytes
	}
	dlPath := filepath.Join(x.work, "downloads.json")
	db, _ := json.MarshalIndent(downloads, "", "  ")
	os.WriteFile(dlPath, append(db, '\n'), 0o644)
	x.extra["migration/downloads.json"] = dlPath
	_, vmxRel, _ := vsphereread.ParseDatastorePath(x.vm.VMX)
	vmx := filepath.Join(x.work, path.Base(vmxRel))
	x.extra["migration/source.vmx"] = vmx
	x.t["download"] = time.Since(t).Seconds()
	x.add("download", "datastore_http", verify.OK, fmt.Sprintf("%d files, %d bytes in %.1f s (GET /folder, sha256 in downloads.json)",
		len(downloads), total, x.t["download"]), t)

	t = time.Now()
	disk, xmlPath, err := x.convert(ctx, vmx)
	x.t["conversion"] = time.Since(t).Seconds()
	if err != nil {
		// virt-v2v ran and refused the guest: not ready for KVM. It could not run at all: not checked.
		v := verify.Critical
		if errors.Is(err, errNotRun) {
			v = verify.Unknown
		}
		x.add("conversion", "virt-v2v", v, err.Error(), t)
		return "", nil, "the conversion failed"
	}
	cfg, err := x.config(xmlPath)
	if err != nil {
		x.add("conversion", "virt-v2v", verify.Unknown, "reading the converted domain: "+err.Error(), t)
		return "", nil, "the converted domain is unreadable"
	}
	x.add("conversion", "virt-v2v", verify.OK, fmt.Sprintf("%s (%s, %s, %d vCPU, %d MiB, NIC %s) in %.1f s",
		filepath.Base(disk), cfg.Machine.Arch, cfg.Machine.Firmware, cfg.Machine.VCPUs, cfg.Machine.MemoryMiB, cfg.Machine.MAC, x.t["conversion"]), t)
	return disk, cfg, ""
}

// layout returns the files to download, or why the VM is outside stage 1's scope.
func (x *vmRun) layout() ([]vsphereread.File, string) {
	switch {
	case x.vm.Snapshots:
		return nil, "the VM has snapshots (stage 1 reads base disks of snapshot-free VMs)"
	case len(x.vm.Disks) != 1:
		return nil, fmt.Sprintf("%d disks (v0 converts and boots single-disk VMs)", len(x.vm.Disks))
	}
	dir := path.Dir(x.vm.VMX)
	var out []vsphereread.File
	want := map[string]bool{"config": true, "diskDescriptor": true, "diskExtent": true, "nvram": true}
	for _, f := range x.vm.Files {
		if !want[f.Type] {
			continue
		}
		if path.Dir(f.Name) != dir {
			return nil, fmt.Sprintf("%s is outside the VM folder %s", f.Name, dir)
		}
		out = append(out, f)
	}
	kinds := map[string]bool{}
	for _, f := range out {
		kinds[f.Type] = true
	}
	if !kinds["config"] || !kinds["diskDescriptor"] || !kinds["diskExtent"] {
		return nil, fmt.Sprintf("layout lacks a vmx, disk descriptor or extent: %v", out)
	}
	return out, ""
}

// convert runs virt-v2v -i vmx locally, to qcow2 with virtio.
func (x *vmRun) convert(ctx context.Context, vmx string) (disk, domXML string, err error) {
	timeout := x.o.V2VTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	logPath := filepath.Join(x.work, "virt-v2v.log")
	lf, err := os.Create(logPath)
	if err != nil {
		return "", "", err
	}
	defer lf.Close()
	x.extra["migration/virt-v2v.log"] = logPath
	args := []string{"-i", "vmx", vmx, "-o", "local", "-os", x.images, "-of", "qcow2", "-on", x.name}
	fmt.Fprintf(lf, "$ virt-v2v %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "virt-v2v", args...)
	cmd.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("%w: %v", errNotRun, err)
	}
	runErr := cmd.Wait()
	disk, domXML = filepath.Join(x.images, x.name+"-sda"), filepath.Join(x.images, x.name+".xml")
	if runErr != nil {
		return "", "", fmt.Errorf("virt-v2v: %v (last line: %s)", runErr, lastLine(logPath))
	}
	if _, err := os.Stat(disk); err != nil {
		return "", "", fmt.Errorf("virt-v2v finished but %s is missing", filepath.Base(disk))
	}
	x.extra["migration/converted-domain.xml"] = domXML
	return disk, domXML, nil
}

var errNotRun = errors.New("virt-v2v could not be started")

func lastLine(p string) string {
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// config builds the sandbox config from virt-v2v's domain XML and the VM's checks.
func (x *vmRun) config(domXMLPath string) (*verify.Config, error) {
	b, err := os.ReadFile(domXMLPath)
	if err != nil {
		return nil, err
	}
	var d struct {
		Memory struct {
			Unit  string `xml:"unit,attr"`
			Value int64  `xml:",chardata"`
		} `xml:"memory"`
		VCPU int `xml:"vcpu"`
		OS   struct {
			Type struct {
				Arch string `xml:"arch,attr"`
			} `xml:"type"`
			Firmware string    `xml:"firmware,attr"`
			Loader   *struct{} `xml:"loader"`
		} `xml:"os"`
		Disks []struct {
			Device string `xml:"device,attr"`
			Target struct {
				Bus string `xml:"bus,attr"`
			} `xml:"target"`
		} `xml:"devices>disk"`
		NICs []struct {
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	m := verify.Machine{Arch: d.OS.Type.Arch, Firmware: "bios", VCPUs: d.VCPU, DiskBus: "virtio", NICModel: "virtio"}
	if d.OS.Firmware == "efi" || d.OS.Loader != nil {
		m.Firmware = "efi"
	}
	m.MemoryMiB = int(toMiB(d.Memory.Value, d.Memory.Unit))
	for _, disk := range d.Disks {
		if disk.Device == "" || disk.Device == "disk" {
			m.DiskBus = disk.Target.Bus
			break
		}
	}
	if len(d.NICs) > 0 {
		m.MAC, m.NICModel = d.NICs[0].MAC.Address, d.NICs[0].Model.Type
	}
	if m.MAC == "" && len(x.vm.NICs) > 0 {
		m.MAC = x.vm.NICs[0].MAC
	}
	ch, ok := x.o.Checks[x.name]
	if !ok {
		ch = x.o.Checks["*"]
	}
	if ch.Template == "" && len(ch.Checks) == 0 {
		ch.Template = "linux-basic"
	}
	cfg := &verify.Config{VM: x.name, Template: ch.Template, Machine: m, Checks: ch.Checks,
		BootTimeoutS: ch.BootTimeoutS, ProbeTimeoutS: ch.ProbeTimeoutS}
	return cfg, resolveConfig(cfg)
}

func toMiB(v int64, unit string) int64 {
	switch strings.ToLower(unit) {
	case "mib", "m":
		return v
	case "gib", "g":
		return v * 1024
	case "b", "bytes":
		return v >> 20
	}
	return v / 1024 // libvirt's default unit is KiB
}

// resolveConfig applies defaults and the template through verify's own loader.
func resolveConfig(c *verify.Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "gaia-verify-config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Write(b)
	f.Close()
	r, err := verify.LoadConfig(f.Name())
	if err != nil {
		return err
	}
	*c = *r
	return nil
}

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
	"crypto/sha256"
	"encoding/hex"
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
	// VirtioWin is the virtio-win ISO that virt-v2v takes Windows drivers from (VIRTIO_WIN);
	// without it, virt-v2v cannot give a Windows guest virtio storage and network.
	VirtioWin string
	Verify    verify.Options // Key, EvidenceDir, Ledger, WorkDir, SerialDir, Socket, Self, Log
	Log       io.Writer
	// Converter converts the downloaded VM; nil runs virt-v2v on this machine (the lab tool). gaia server gives its
	// converter service (ADR-005 §1).
	Converter Converter
	// Dirs gives a VM's download and image directories; nil: WorkDir/<vm> and ImageDir/<vm>.
	Dirs func(vm string) (work, images string)

	virtioWin *Input // hashed once per run
}

// Converter turns a downloaded VM into KVM disks.
type Converter interface {
	Convert(ctx context.Context, in ConvertInput) ConvertOutput
}

// ConvertInput is one conversion: the downloaded .vmx, the guest's name, whether the source says it is Windows.
type ConvertInput struct {
	VMX      string // the downloaded .vmx (its disks next to it)
	Name     string
	Windows  bool
	ImageDir string // where the disks, domain XML and log go
}

// ConvertOutput is what a conversion produced.
type ConvertOutput struct {
	State     string            // converted, failed (virt-v2v refused the guest: CRITICAL), not_run (UNKNOWN)
	Detail    string            // virt-v2v's error, or why it did not run
	Seconds   float64           //
	Disks     []string          // paths, boot disk first
	DomainXML string            // path
	Log       string            // path
	VirtioWin bool              // virtio-win was given to virt-v2v
	Info      any               // the converter's facts for the bundle (image, versions, virtio-win)
	Files     map[string]string // more files for the bundle (bundle path -> local file)
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

// Input is a local file the conversion used, identified by its hash.
type Input struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Report is the outcome of a run.
type Report struct {
	Source    string           `json:"source"`
	Product   string           `json:"product"`
	Inventory []vsphereread.VM `json:"inventory"`
	VirtioWin *Input           `json:"virtio_win,omitempty"`
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
	if o.VirtioWin != "" {
		sum, n, err := fileSHA256(o.VirtioWin)
		if err != nil {
			return nil, fmt.Errorf("virtio-win: %w", err)
		}
		rep.VirtioWin = &Input{Path: o.VirtioWin, Bytes: n, SHA256: sum}
		o.logf("virtio-win: %s, %d bytes, sha256 %s", o.VirtioWin, n, sum)
		o.virtioWin = rep.VirtioWin
	}
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

	convInfo any
	declared verify.Config
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
	p, err := Prepare(ctx, o, name, v, found)
	if err != nil {
		return nil, err
	}
	vo := o.Verify
	vo.Config, vo.PreChecks, vo.Extra, vo.SkipReason = p.Config, p.PreChecks, p.Extra, p.SkipReason
	if len(p.Disks) > 0 {
		vo.Image, vo.ImageFormat = p.Disks[0], "qcow2"
	}
	res, err := verify.Run(ctx, vo)
	if err != nil {
		return nil, err
	}
	for k, v := range res.Timings {
		p.Timings[k] = v
	}
	return &Readiness{VM: name, Readiness: res.Verdict, BundleID: res.BundleID, LedgerSeq: res.LedgerSeq, Checks: res.Checks, Timings: p.Timings}, nil
}

// Prepared is a VM ready for the sandbox, or the reason it is not: the steps so far as checks (they go ahead of the
// guest's own in the bundle), the files for the bundle, the converted disks and the sandbox configuration.
type Prepared struct {
	VM         vsphereread.VM
	PreChecks  []verify.CheckResult
	Extra      map[string]string // bundle path -> local file
	Disks      []string          // converted qcow2 disks, boot disk first; none when SkipReason is set
	Config     *verify.Config    // always set: without a conversion, a minimal one so the bundle still records each step
	Declared   verify.Config     // Config as declared, before the template is applied (what the sandbox helper takes)
	SkipReason string
	Timings    map[string]float64
	Converter  any // the converter's facts (image, versions, virtio-win), when it ran
}

// Prepare runs a VM's migration steps up to the converted disks: what the source says about it, that it is powered
// off, its layout, the download (read-only calls only), the conversion, and the firmware it boots with. A step that
// fails is that VM's verdict; err means the steps themselves could not run (a local file could not be written).
func Prepare(ctx context.Context, o Options, name string, v vsphereread.VM, found bool) (*Prepared, error) {
	work, images := filepath.Join(o.WorkDir, name), filepath.Join(o.ImageDir, name)
	if o.Dirs != nil {
		work, images = o.Dirs(name)
	}
	x := &vmRun{o: o, name: name, vm: v, work: work, images: images, extra: map[string]string{}, t: map[string]float64{}}
	if err := os.MkdirAll(x.work, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(x.images, 0o755); err != nil {
		return nil, err
	}
	disks, cfg, skip := x.prepare(ctx, found)
	if cfg == nil {
		tmpl := "linux-boot"
		if defaultTemplate(v.GuestID) == "windows-basic" {
			tmpl = "windows-basic"
		}
		cfg = &verify.Config{VM: name, Template: tmpl, Machine: verify.Machine{MAC: "52:54:00:00:00:00"}}
		x.declared = *cfg
		if err := resolveConfig(cfg); err != nil {
			return nil, err
		}
	}
	if o.CallLog != "" {
		x.extra["migration/calls.jsonl"] = o.CallLog
	}
	return &Prepared{VM: v, PreChecks: x.pre, Extra: x.extra, Disks: disks, Config: cfg, Declared: x.declared, SkipReason: skip,
		Timings: x.t, Converter: x.convInfo}, nil
}

var allSteps = []string{"source_vm", "cold", "layout", "download", "conversion", "firmware"}

// prepare runs the migration steps; it returns the converted disks and the sandbox config,
// or nil and the reason the guest cannot be started.
func (x *vmRun) prepare(ctx context.Context, found bool) ([]string, *verify.Config, string) {
	t := time.Now()
	invPath := filepath.Join(x.work, "inventory.json")
	b, _ := json.MarshalIndent(x.vm, "", "  ")
	os.WriteFile(invPath, append(b, '\n'), 0o644)
	x.extra["migration/inventory.json"] = invPath
	switch {
	case !found:
		x.add("source_vm", "inventory", verify.Unknown, "no VM of this name on the source", t)
		x.notRun("the VM was not found", allSteps[1:]...)
		return nil, nil, "the VM was not found on the source"
	case x.vm.Unreadable != "":
		x.add("source_vm", "inventory", verify.Unknown, x.vm.Unreadable, t)
		x.notRun("the VM's configuration is unreadable", allSteps[1:]...)
		return nil, nil, "the VM's configuration is unreadable"
	}
	x.add("source_vm", "inventory", verify.OK, fmt.Sprintf("%s, guest %s, %d vCPU, %d MiB, firmware %s%s, %d disk(s), %d NIC(s)",
		x.vm.VMX, x.vm.GuestID, x.vm.CPUs, x.vm.MemoryMiB, x.vm.Firmware, opt(x.vm.SecureBoot, " (Secure Boot)"), len(x.vm.Disks), len(x.vm.NICs)), t)

	t = time.Now()
	if x.vm.PowerState != "poweredOff" {
		x.add("cold", "power_state", verify.Unknown, "the VM is "+x.vm.PowerState+": stage 1 checks powered-off VMs (or clones) only", t)
		x.notRun("the VM is not powered off", allSteps[2:]...)
		return nil, nil, "the VM is not powered off"
	}
	x.add("cold", "power_state", verify.OK, "poweredOff", t)

	t = time.Now()
	files, why := x.layout()
	if why != "" {
		x.add("layout", "files", verify.Unknown, why, t)
		x.notRun("unsupported layout", allSteps[3:]...)
		return nil, nil, "unsupported layout: " + why
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
			x.notRun("a download failed", allSteps[4:]...)
			return nil, nil, "a download failed"
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

	return x.convertStep(ctx, vmx)
}

// convertStep converts the downloaded VM and checks what came out: the guest OS virt-v2v found and the firmware.
func (x *vmRun) convertStep(ctx context.Context, vmx string) ([]string, *verify.Config, string) {
	t := time.Now()
	conv := x.o.Converter
	if conv == nil {
		conv = localConverter{o: x.o}
	}
	windows := defaultTemplate(x.vm.GuestID) == "windows-basic"
	out := conv.Convert(ctx, ConvertInput{VMX: vmx, Name: x.name, Windows: windows, ImageDir: x.images})
	x.t["conversion"] = out.Seconds
	x.convInfo = out.Info
	if out.Log != "" {
		x.extra["migration/virt-v2v.log"] = out.Log
	}
	for k, v := range out.Files {
		x.extra[k] = v
	}
	if out.State != "converted" {
		// virt-v2v ran and refused the guest: not ready for KVM. It could not run at all: not checked.
		v := verify.Critical
		if out.State != "failed" {
			v = verify.Unknown
		}
		x.add("conversion", "virt-v2v", v, out.Detail, t)
		x.notRun("the conversion did not finish", "firmware")
		return nil, nil, "the conversion failed"
	}
	x.extra["migration/converted-domain.xml"] = out.DomainXML
	cfg, osID, err := x.config(out.DomainXML)
	if err != nil {
		x.add("conversion", "virt-v2v", verify.Unknown, "reading the converted domain: "+err.Error(), t)
		x.notRun("the converted domain is unreadable", "firmware")
		return nil, nil, "the converted domain is unreadable"
	}
	// A Windows guest needs virtio-win's drivers on its new virtio disk and NIC. The source's guest OS decides
	// whether they are given; a guest virt-v2v found to be Windows although the source said otherwise was converted
	// without them and would only show that it cannot boot: not checked (ADR-005 §2).
	if isWindowsOS(osID) && !out.VirtioWin {
		x.add("conversion", "virt-v2v", verify.Unknown, fmt.Sprintf("virt-v2v found Windows (%s) but the source's guest OS %q is not Windows, "+
			"so no virtio-win drivers were given: the converted guest cannot be judged", osID, x.vm.GuestID), t)
		x.notRun("converted without virtio drivers", "firmware")
		return nil, nil, "a Windows guest converted without virtio-win"
	}
	x.add("conversion", "virt-v2v", verify.OK, fmt.Sprintf("%d disk(s), %s (%s, %s, %d vCPU, %d MiB, NIC %s)%s in %.1f s",
		len(out.Disks), osID, cfg.Machine.Arch, cfg.Machine.Firmware, cfg.Machine.VCPUs, cfg.Machine.MemoryMiB, cfg.Machine.MAC,
		opt(out.VirtioWin, ", virtio-win drivers"), out.Seconds), t)

	// UEFI detection: the firmware the source boots with must be the one the converted guest is started with (OVMF
	// for UEFI), or the sandbox would prove a boot path the VM does not have.
	t = time.Now()
	src := strings.ToLower(x.vm.Firmware)
	if src == "" {
		src = "bios"
	}
	switch {
	case src != cfg.Machine.Firmware:
		x.add("firmware", "firmware", verify.Critical, fmt.Sprintf("the source boots with %s, the converted guest with %s", src, cfg.Machine.Firmware), t)
	case src == "efi" && x.vm.SecureBoot:
		x.add("firmware", "firmware", verify.OK, "UEFI with Secure Boot on the source; the sandbox boots it with UEFI (OVMF); "+
			"Secure Boot itself is not checked", t)
	case src == "efi":
		x.add("firmware", "firmware", verify.OK, "UEFI on the source and in the sandbox (OVMF)", t)
	default:
		x.add("firmware", "firmware", verify.OK, "BIOS on the source and in the sandbox (SeaBIOS)", t)
	}
	return out.Disks, cfg, ""
}

func opt(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

// isWindowsOS reports whether a libosinfo OS id (virt-v2v's <libosinfo:os id=...>) is Windows.
func isWindowsOS(id string) bool { return strings.Contains(id, "microsoft.com/win") }

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

// localConverter runs virt-v2v -i vmx on this machine, to qcow2 with virtio (the lab tool, gaia migrate-check).
type localConverter struct{ o Options }

func (c localConverter) Convert(ctx context.Context, in ConvertInput) ConvertOutput {
	start := time.Now()
	out := ConvertOutput{Log: filepath.Join(filepath.Dir(in.VMX), "virt-v2v.log")}
	if c.o.virtioWin != nil {
		out.Info = map[string]any{"virtio_win": c.o.virtioWin}
	}
	disk, xml, err := c.convert(ctx, in, out.Log)
	out.Seconds = time.Since(start).Seconds()
	switch {
	case errors.Is(err, errNotRun):
		out.State, out.Detail = "not_run", err.Error()
	case err != nil:
		out.State, out.Detail = "failed", err.Error()
	default:
		out.State, out.Disks, out.DomainXML, out.VirtioWin = "converted", []string{disk}, xml, c.o.VirtioWin != ""
	}
	return out
}

func (c localConverter) convert(ctx context.Context, in ConvertInput, logPath string) (disk, domXML string, err error) {
	timeout := c.o.V2VTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lf, err := os.Create(logPath)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errNotRun, err)
	}
	defer lf.Close()
	args := []string{"-i", "vmx", in.VMX, "-o", "local", "-os", in.ImageDir, "-of", "qcow2", "-on", in.Name}
	fmt.Fprintf(lf, "$ virt-v2v %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "virt-v2v", args...)
	cmd.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	if c.o.VirtioWin != "" {
		cmd.Env = append(cmd.Env, "VIRTIO_WIN="+c.o.VirtioWin)
		fmt.Fprintf(lf, "VIRTIO_WIN=%s (sha256 %s)\n", c.o.VirtioWin, c.o.virtioWin.SHA256)
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("%w: %v", errNotRun, err)
	}
	runErr := cmd.Wait()
	disk, domXML = filepath.Join(in.ImageDir, in.Name+"-sda"), filepath.Join(in.ImageDir, in.Name+".xml")
	if runErr != nil {
		return "", "", fmt.Errorf("virt-v2v: %v (%s)", runErr, errorLine(logPath))
	}
	if _, err := os.Stat(disk); err != nil {
		return "", "", fmt.Errorf("virt-v2v finished but %s is missing", filepath.Base(disk))
	}
	return disk, domXML, nil
}

var errNotRun = errors.New("virt-v2v could not be started")

// errorLine returns virt-v2v's own error message from its log, or the last line when there is none
// (after an error virt-v2v prints how to report a bug, so the last line is rarely the reason).
func errorLine(p string) string {
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "virt-v2v: error:") {
			return strings.TrimSpace(lines[i])
		}
	}
	return "last line: " + lines[len(lines)-1]
}

// config builds the sandbox config from virt-v2v's domain XML and the VM's checks; it also returns the guest OS
// virt-v2v found (its libosinfo id).
func (x *vmRun) config(domXMLPath string) (*verify.Config, string, error) {
	b, err := os.ReadFile(domXMLPath)
	if err != nil {
		return nil, "", err
	}
	var d struct {
		Metadata struct {
			OS struct {
				ID string `xml:"id,attr"`
			} `xml:"http://libosinfo.org/xmlns/libvirt/domain/1.0 libosinfo>os"`
		} `xml:"metadata"`
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
		return nil, "", err
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
		ch.Template = defaultTemplate(x.vm.GuestID)
	}
	cfg := &verify.Config{VM: x.name, Template: ch.Template, Machine: m, Checks: ch.Checks,
		BootTimeoutS: ch.BootTimeoutS, ProbeTimeoutS: ch.ProbeTimeoutS}
	x.declared = *cfg
	x.declared.Checks = append([]verify.Check(nil), ch.Checks...)
	os := d.Metadata.OS.ID
	if os == "" {
		os = "an unknown OS"
	}
	return cfg, os, resolveConfig(cfg)
}

// defaultTemplate picks the built-in checks from the source's guest OS identifier
// (vSphere guestId, e.g. windows2019srvNext_64Guest or debian12_64Guest).
func defaultTemplate(guestID string) string {
	if strings.HasPrefix(strings.ToLower(guestID), "win") {
		return "windows-basic"
	}
	return "linux-basic"
}

func fileSHA256(p string) (string, int64, error) {
	f, err := os.Open(p)
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

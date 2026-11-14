// Package vsphereprobe answers Phase 1 / Track A's gate question: on a given
// vSphere host (in particular free ESXi), which of the API calls an image-level
// backup product needs are actually allowed?
//
// It works on a throwaway VM with an empty 1 GiB thin disk, so it never touches
// real workloads. Every step is recorded as PASS, FAIL or SKIP with the exact
// error, because the error text is the evidence (license restrictions show up
// as faults on write operations).
package vsphereprobe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/license"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// Status of one probe step.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Verdict follows the Nagios-style four-level model used across Gaia.
type Verdict string

const (
	OK       Verdict = "OK"
	Warning  Verdict = "WARNING"
	Critical Verdict = "CRITICAL"
	Unknown  Verdict = "UNKNOWN"
)

// ExitCode maps a verdict to 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN.
// Anything unrecognised is UNKNOWN, never OK.
func (v Verdict) ExitCode() int {
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

// Step is one recorded API check.
type Step struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  Status  `json:"status"`
	Detail  string  `json:"detail,omitempty"`
	Seconds float64 `json:"seconds"`
}

// Report is the probe's evidence.
type Report struct {
	Product  string   `json:"product"`
	APIType  string   `json:"api_type"`
	Licenses []string `json:"licenses"`
	// LicenseError is set when the license list could not be read.
	LicenseError string    `json:"license_error,omitempty"`
	Steps        []Step    `json:"steps"`
	Verdict      Verdict   `json:"verdict"`
	Summary      string    `json:"summary"`
	StartedAt    time.Time `json:"started_at"`
}

// Config controls a probe run.
type Config struct {
	VMName    string // existing VM to use; empty = create "gaia-probe"
	Datastore string // datastore for the created VM; empty = default
	ReadBytes int64  // how much disk data to read in the read steps
	Keep      bool   // keep the created VM afterwards
}

// Step IDs that decide the verdict.
const (
	stepVM       = "vm"
	stepCBT      = "cbt-enable"
	stepSnapshot = "snapshot-create"
	stepQuery    = "cbt-query"
	stepNFC      = "read-nfc-export"
	stepHTTP     = "read-datastore-http"
)

type run struct {
	ctx context.Context
	c   *vim25.Client
	cfg Config
	r   *Report
}

func (p *run) step(id, name string, fn func() (string, error)) bool {
	start := time.Now()
	detail, err := fn()
	s := Step{ID: id, Name: name, Status: Pass, Detail: detail, Seconds: time.Since(start).Seconds()}
	if err != nil {
		s.Status, s.Detail = Fail, err.Error()
	}
	p.r.Steps = append(p.r.Steps, s)
	return err == nil
}

func (p *run) skip(id, name, why string) {
	p.r.Steps = append(p.r.Steps, Step{ID: id, Name: name, Status: Skip, Detail: why})
}

// Run executes the probe. It always returns a report; errors are steps.
func Run(ctx context.Context, c *vim25.Client, cfg Config) *Report {
	if cfg.ReadBytes <= 0 {
		cfg.ReadBytes = 64 << 20
	}
	about := c.ServiceContent.About
	p := &run{ctx: ctx, c: c, cfg: cfg, r: &Report{
		Product: about.FullName, APIType: about.ApiType, StartedAt: time.Now().UTC(),
	}}
	// The license list is evidence for the gate answer, so a failure to read it is recorded, not dropped.
	if infos, err := license.NewManager(c).List(ctx); err != nil {
		p.r.LicenseError = err.Error()
	} else {
		for _, l := range infos {
			p.r.Licenses = append(p.r.Licenses, fmt.Sprintf("%s (%s)", l.Name, l.EditionKey))
		}
	}

	finder := find.NewFinder(c, true)
	var vm *object.VirtualMachine
	var created bool

	// Both paths need the datacenter: VM and datastore lookups are relative to it.
	dc, dcErr := finder.DefaultDatacenter(ctx)
	if dcErr == nil {
		finder.SetDatacenter(dc)
	}

	if cfg.VMName != "" {
		p.step(stepVM, "find existing VM "+cfg.VMName, func() (string, error) {
			if dcErr != nil {
				return "", dcErr
			}
			var err error
			vm, err = finder.VirtualMachine(ctx, cfg.VMName)
			return "", err
		})
	} else {
		p.step(stepVM, "create throwaway VM gaia-probe (CreateVM_Task)", func() (string, error) {
			if dcErr != nil {
				return "", dcErr
			}
			var err error
			vm, err = createProbeVM(ctx, c, finder, dc, cfg.Datastore)
			created = err == nil
			return "1 GiB thin disk, never powered on", err
		})
	}

	if vm == nil {
		for _, s := range []struct{ id, name string }{
			{stepCBT, "enable CBT"}, {stepSnapshot, "create snapshot"}, {stepQuery, "QueryChangedDiskAreas"},
			{stepNFC, "read disk via NFC export"}, {stepHTTP, "read disk via datastore HTTP"},
		} {
			p.skip(s.id, s.name, "no VM to test with")
		}
		p.finish()
		return p.r
	}

	disk, diskErr := firstDisk(ctx, vm)

	p.step(stepCBT, "enable Changed Block Tracking (ReconfigVM_Task)", func() (string, error) {
		task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{ChangeTrackingEnabled: types.NewBool(true)})
		if err != nil {
			return "", err
		}
		return "", task.Wait(ctx)
	})

	var snap *types.ManagedObjectReference
	p.step(stepSnapshot, "create snapshot (CreateSnapshot_Task)", func() (string, error) {
		task, err := vm.CreateSnapshot(ctx, "gaia-probe", "Gaia vsphere-probe", false, false)
		if err != nil {
			return "", err
		}
		info, err := task.WaitForResult(ctx)
		if err != nil {
			return "", err
		}
		ref, ok := info.Result.(types.ManagedObjectReference)
		if !ok {
			return "", errors.New("snapshot task returned no reference")
		}
		snap = &ref
		return ref.Value, nil
	})

	if snap == nil || diskErr != nil {
		why := "no snapshot"
		if diskErr != nil {
			why = diskErr.Error()
		}
		p.skip(stepQuery, "QueryChangedDiskAreas", why)
	} else {
		p.step(stepQuery, "query allocated areas (QueryChangedDiskAreas, changeId *)", func() (string, error) {
			res, err := methods.QueryChangedDiskAreas(ctx, c, &types.QueryChangedDiskAreas{
				This: vm.Reference(), Snapshot: snap, DeviceKey: disk.Key, StartOffset: 0, ChangeId: "*",
			})
			if err != nil {
				return "", err
			}
			var total int64
			for _, a := range res.Returnval.ChangedArea {
				total += a.Length
			}
			return fmt.Sprintf("%d extents, %d bytes allocated", len(res.Returnval.ChangedArea), total), nil
		})
	}

	p.step(stepNFC, "read disk via NFC export (ExportVm / HttpNfcLease)", func() (string, error) {
		return readViaNFC(ctx, c, vm, cfg.ReadBytes)
	})

	if diskErr != nil {
		p.skip(stepHTTP, "read disk via datastore HTTP", diskErr.Error())
	} else {
		p.step(stepHTTP, "read base disk via datastore HTTP (/folder)", func() (string, error) {
			return readViaDatastore(ctx, finder, disk, cfg.ReadBytes)
		})
	}

	// Cleanup is recorded too: a host that lets you snapshot but not clean up is a finding.
	if snap != nil {
		p.step("snapshot-remove", "remove snapshot (RemoveSnapshot_Task)", func() (string, error) {
			res, err := methods.RemoveSnapshot_Task(ctx, c, &types.RemoveSnapshot_Task{This: *snap, RemoveChildren: false})
			if err != nil {
				return "", err
			}
			return "", object.NewTask(c, res.Returnval).Wait(ctx)
		})
	}
	if created && !cfg.Keep {
		p.step("vm-destroy", "destroy throwaway VM (Destroy_Task)", func() (string, error) {
			task, err := vm.Destroy(ctx)
			if err != nil {
				return "", err
			}
			return "", task.Wait(ctx)
		})
	}

	p.finish()
	return p.r
}

// finish derives the verdict:
//
//	OK        CBT, snapshot, CBT query and at least one disk read all pass
//	CRITICAL  snapshots or every disk read are refused: image backup impossible
//	WARNING   anything in between (e.g. no CBT: full backups only)
//	UNKNOWN   there was no VM to test with
func (p *run) finish() {
	st := map[string]Status{}
	for _, s := range p.r.Steps {
		st[s.ID] = s.Status
	}
	readOK := st[stepNFC] == Pass || st[stepHTTP] == Pass
	switch {
	case st[stepVM] != Pass:
		p.r.Verdict, p.r.Summary = Unknown, "no VM to test with; create one in the host UI and rerun with -vm"
	case st[stepSnapshot] != Pass || !readOK:
		p.r.Verdict, p.r.Summary = Critical, "image-level backup is not possible on this host"
	case st[stepCBT] == Pass && st[stepQuery] == Pass:
		p.r.Verdict, p.r.Summary = OK, "snapshots, CBT and disk reads are allowed: incremental image backup is possible"
	default:
		p.r.Verdict, p.r.Summary = Warning, "snapshots and reads work but CBT does not: full backups only"
	}
}

func createProbeVM(ctx context.Context, c *vim25.Client, finder *find.Finder, dc *object.Datacenter, dsName string) (*object.VirtualMachine, error) {
	var ds *object.Datastore
	var err error
	if dsName != "" {
		ds, err = finder.Datastore(ctx, dsName)
	} else {
		ds, err = finder.DefaultDatastore(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("datastore: %w", err)
	}
	pool, err := finder.DefaultResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource pool: %w", err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		return nil, err
	}

	var devices object.VirtualDeviceList
	ctrl, err := devices.CreateSCSIController("pvscsi")
	if err != nil {
		return nil, err
	}
	devices = append(devices, ctrl)
	disk := devices.CreateDisk(ctrl.(types.BaseVirtualController), ds.Reference(), "")
	disk.CapacityInKB = 1 << 20 // 1 GiB
	if b, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
		b.ThinProvisioned = types.NewBool(true)
	}
	devices = append(devices, disk)
	changes, err := devices.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	if err != nil {
		return nil, err
	}
	for _, ch := range changes {
		spec := ch.GetVirtualDeviceConfigSpec()
		if _, ok := spec.Device.(*types.VirtualDisk); ok {
			spec.FileOperation = types.VirtualDeviceConfigSpecFileOperationCreate
		}
	}

	spec := types.VirtualMachineConfigSpec{
		Name:         "gaia-probe",
		GuestId:      string(types.VirtualMachineGuestOsIdentifierOtherGuest64),
		NumCPUs:      1,
		MemoryMB:     128,
		Files:        &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", ds.Name())},
		DeviceChange: changes,
	}
	task, err := folders.VmFolder.CreateVM(ctx, spec, pool, nil)
	if err != nil {
		return nil, err
	}
	info, err := task.WaitForResult(ctx)
	if err != nil {
		return nil, err
	}
	ref, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return nil, errors.New("CreateVM task returned no reference")
	}
	return object.NewVirtualMachine(c, ref), nil
}

func firstDisk(ctx context.Context, vm *object.VirtualMachine) (*types.VirtualDisk, error) {
	devices, err := vm.Device(ctx)
	if err != nil {
		return nil, err
	}
	disks := devices.SelectByType((*types.VirtualDisk)(nil))
	if len(disks) == 0 {
		return nil, errors.New("VM has no virtual disk")
	}
	return disks[0].(*types.VirtualDisk), nil
}

// readViaNFC is how a backup product reads disks without VDDK: export the VM
// through an HttpNfcLease and stream the disk. We read cfg.ReadBytes and abort.
func readViaNFC(ctx context.Context, c *vim25.Client, vm *object.VirtualMachine, n int64) (string, error) {
	lease, err := vm.Export(ctx)
	if err != nil {
		return "", err
	}
	info, err := lease.Wait(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = lease.Abort(ctx, nil) }()
	for _, item := range info.Items {
		if !strings.HasSuffix(strings.ToLower(item.Path), ".vmdk") {
			continue
		}
		rc, _, err := c.Download(ctx, item.URL, &soap.DefaultDownload)
		if err != nil {
			return "", err
		}
		defer rc.Close()
		// A short read is fine (an export of an empty disk is a tiny stream-optimized VMDK),
		// but zero bytes proves nothing.
		got, err := io.CopyN(io.Discard, rc, n)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if got == 0 {
			return "", fmt.Errorf("export of %s returned no data", item.Path)
		}
		return fmt.Sprintf("read %d bytes of %s", got, item.Path), nil
	}
	return "", errors.New("export lease offered no .vmdk item")
}

// readViaDatastore reads the base (snapshotted, now read-only) flat disk file
// directly over the host's /folder HTTP endpoint, with a Range header.
func readViaDatastore(ctx context.Context, finder *find.Finder, disk *types.VirtualDisk, n int64) (string, error) {
	backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	if !ok {
		return "", fmt.Errorf("unsupported disk backing %T", disk.Backing)
	}
	var path object.DatastorePath
	if !path.FromString(backing.FileName) {
		return "", fmt.Errorf("cannot parse disk path %q", backing.FileName)
	}
	ds, err := finder.Datastore(ctx, path.Datastore)
	if err != nil {
		return "", err
	}
	flat := strings.TrimSuffix(path.Path, ".vmdk") + "-flat.vmdk"
	// Not ds.Download: it accepts only 200, but the answer to a Range request is 206 Partial
	// Content (seen on ESXi 8 in T2), and on an error it leaves the response body open.
	u, ticket, err := ds.ServiceTicket(ctx, flat, "GET")
	if err != nil {
		return "", err
	}
	param := soap.DefaultDownload
	param.Headers = map[string]string{"Range": fmt.Sprintf("bytes=0-%d", n-1)}
	if ticket != nil {
		param.Ticket, param.Close = ticket, true
	}
	res, err := ds.Client().DownloadRequest(ctx, u, &param)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("GET %s: %s", u, res.Status)
	}
	// CopyN, not Copy: a host that ignores Range (200) would otherwise stream the whole disk.
	got, err := io.CopyN(io.Discard, res.Body, n)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// The flat file of a thin disk reads as zeros, so a full disk always yields n bytes.
	if got < n {
		return "", fmt.Errorf("short read: %d of %d bytes of [%s] %s", got, n, path.Datastore, flat)
	}
	return fmt.Sprintf("read %d bytes of [%s] %s", got, path.Datastore, flat), nil
}

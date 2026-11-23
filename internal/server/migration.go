package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/convert"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/migrate"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
	"github.com/Warewave-Technology/gaia/internal/vsphereread"
)

// The migration check in the server (ADR-003, ADR-005, T21): a source is a vSphere endpoint read with read-only calls
// only; a check of one VM downloads its files, has the converter service (gaia convert serve, its own unprivileged
// user) convert them in the pinned converter image, boots the result in the sandbox like a restore test, and seals
// one signed readiness bundle. A check is a job; one runs at a time.

// SourceCreate is the request that records a migration source. The password goes to secrets/, never to the
// database, an answer or the audit log.
type SourceCreate struct {
	Name     string `json:"name"`
	URL      string `json:"url"` // https://HOST/sdk
	Username string `json:"username"`
	Password string `json:"password"`
	Insecure bool   `json:"insecure"` // do not verify the endpoint's certificate (a lab ESXi)
}

// MigrationCheckRequest asks for a check of one VM of a source. Without template and checks, the template follows
// the source's guest OS (windows-basic for Windows, linux-basic otherwise).
type MigrationCheckRequest struct {
	VM            string         `json:"vm"`
	Template      string         `json:"template,omitempty"`
	Checks        []verify.Check `json:"checks,omitempty"`
	BootTimeoutS  int            `json:"boot_timeout_s,omitempty"`
	ProbeTimeoutS int            `json:"probe_timeout_s,omitempty"`
	// ExcludeDrivers: virtio-win drivers a Windows guest is converted without (ADR-005 C); the result is at best WARNING.
	ExcludeDrivers []string `json:"exclude_drivers,omitempty"`
}

// SourceVM is one VM of a source's inventory, as a screen lists it.
type SourceVM struct {
	Name       string `json:"name"`
	PowerState string `json:"power_state"`
	GuestID    string `json:"guest_id"`
	GuestName  string `json:"guest_full_name"`
	Firmware   string `json:"firmware"`
	SecureBoot bool   `json:"secure_boot"`
	CPUs       int32  `json:"cpus"`
	MemoryMiB  int32  `json:"memory_mib"`
	Disks      int    `json:"disks"`
	DiskBytes  int64  `json:"disk_bytes"`
	Snapshots  bool   `json:"has_snapshots"`
	Template   bool   `json:"template"`
	Checkable  string `json:"checkable"` // "" when stage 1 can check it, otherwise why not
}

// SourceInventory is a source's VMs and the calls it took to list them (read-only, ADR-003 §2).
type SourceInventory struct {
	Source  string     `json:"source"`
	Product string     `json:"product"`
	VMs     []SourceVM `json:"vms"`
	Calls   int        `json:"calls"`
	Blocked int        `json:"blocked_calls"`
}

// MigrationDetail is what a migration check did; it is the check's detail and the job's.
type MigrationDetail struct {
	Source    string               `json:"source"`
	Product   string               `json:"product"`
	Checks    []verify.CheckResult `json:"checks"`
	Timings   map[string]float64   `json:"timings_s"`
	Converter *convert.Result      `json:"converter,omitempty"`
	Calls     int                  `json:"vsphere_calls"`
	Blocked   int                  `json:"vsphere_blocked_calls"`
	Bundle    string               `json:"bundle"`
	LedgerSeq int                  `json:"ledger_seq"`
	Missing   []string             `json:"missing_files,omitempty"` // bundle files the steps did not produce
	// Code is the reason code when one names the cause (driver_install_loop, with Drivers: the drivers whose
	// first-boot install failed on every boot).
	Code    string   `json:"code,omitempty"`
	Drivers []string `json:"drivers,omitempty"`
	Errors    []string             `json:"errors"`
}

func (c Config) sourcePasswordFile(name string) string {
	return filepath.Join(c.secretsDir(), "source-"+name+".password")
}

func (s *Server) createSource(c *call) (int, any, error) {
	var in SourceCreate
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return 0, nil, fmt.Errorf("%w: url %q: https://HOST/sdk", store.ErrInvalid, in.URL)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/sdk"
	}
	in.URL = u.String()
	if in.Password == "" {
		return 0, nil, fmt.Errorf("%w: password is required", store.ErrInvalid)
	}
	if !store.ValidName(in.Name) {
		return 0, nil, fmt.Errorf("%w: a source name is 1-128 of A-Z a-z 0-9 . _ - and starts with a letter or digit", store.ErrInvalid)
	}
	c.target = "migration_source:" + in.Name
	// The secret first: a source without its password would be a record that cannot be used.
	if err := os.MkdirAll(s.cfg.secretsDir(), 0o700); err != nil {
		return 0, nil, err
	}
	pf := s.cfg.sourcePasswordFile(in.Name)
	if _, err := os.Stat(pf); err == nil {
		return 0, nil, fmt.Errorf("%w: a source named %s exists", store.ErrConflict, in.Name)
	}
	if err := os.WriteFile(pf, []byte(in.Password), 0o600); err != nil {
		return 0, nil, err
	}
	m, err := s.db.CreateMigrationSource(c.r.Context(), c.user.Username, store.MigrationSource{Name: in.Name, Kind: "vsphere", URL: in.URL,
		Username: in.Username, Insecure: in.Insecure})
	if err != nil {
		if rerr := os.Remove(pf); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("source %s (%s as %s)", m.Name, m.URL, m.Username)
	return http.StatusCreated, m, nil
}

func (s *Server) deleteSource(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	m, err := s.db.MigrationSource(c.r.Context(), id)
	if err != nil {
		return 0, nil, err
	}
	c.target = "migration_source:" + m.Name
	if err := s.db.DeleteMigrationSource(c.r.Context(), c.user.Username, id); err != nil {
		return 0, nil, err
	}
	if err := os.Remove(s.cfg.sourcePasswordFile(m.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, nil
}

// connectSource logs in to a source with read-only calls only; every call goes to log.
func (s *Server) connectSource(ctx context.Context, m store.MigrationSource, log io.Writer) (*vsphereread.Client, error) {
	pw, err := os.ReadFile(s.cfg.sourcePasswordFile(m.Name))
	if err != nil {
		return nil, fmt.Errorf("source %s: its password: %w", m.Name, err)
	}
	return vsphereread.Connect(ctx, m.URL, m.Username, strings.TrimRight(string(pw), "\r\n"), m.Insecure, log)
}

func (s *Server) sourceInventory(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	m, err := s.db.MigrationSource(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	logPath := filepath.Join(s.cfg.DataDir, "migration", fmt.Sprintf("source-%d-calls.jsonl", m.ID))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return 0, nil, err
	}
	lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return 0, nil, err
	}
	defer lf.Close()
	cl, err := s.connectSource(ctx, m, lf)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errHost, err)
	}
	defer cl.Logout(context.WithoutCancel(ctx))
	inv, err := cl.Inventory(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errHost, err)
	}
	out := SourceInventory{Source: cl.Host, Product: cl.Vim.ServiceContent.About.FullName, VMs: []SourceVM{}}
	for _, v := range inv {
		sv := SourceVM{Name: v.Name, PowerState: v.PowerState, GuestID: v.GuestID, GuestName: v.GuestName, Firmware: v.Firmware,
			SecureBoot: v.SecureBoot, CPUs: v.CPUs, MemoryMiB: v.MemoryMiB, Disks: len(v.Disks), Snapshots: v.Snapshots, Template: v.Template}
		for _, d := range v.Disks {
			sv.DiskBytes += d.Capacity
		}
		switch {
		case v.Unreadable != "":
			sv.Checkable = v.Unreadable
		case v.Template:
			sv.Checkable = "a template"
		case v.PowerState != "poweredOff":
			sv.Checkable = "powered on: stage 1 checks powered-off VMs (or clones) only"
		case v.Snapshots:
			sv.Checkable = "has snapshots"
		case len(v.Disks) != 1:
			sv.Checkable = fmt.Sprintf("%d disks: single-disk VMs only", len(v.Disks))
		}
		out.VMs = append(out.VMs, sv)
	}
	for _, call := range cl.Guard.Calls() {
		out.Calls++
		if !call.Allowed {
			out.Blocked++
		}
	}
	c.detail = fmt.Sprintf("%d VMs, %d calls, %d blocked", len(out.VMs), out.Calls, out.Blocked)
	return http.StatusOK, out, nil
}

func (s *Server) queueMigrationCheck(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	var in MigrationCheckRequest
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	if err := convert.CheckExcludeDrivers(in.ExcludeDrivers); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", store.ErrInvalid, err)
	}
	if in.Template != "" || len(in.Checks) > 0 {
		cfg := verify.Config{VM: "x", Template: in.Template, Checks: in.Checks, Machine: verify.Machine{MAC: "52:54:00:00:00:01"}}
		if err := cfg.Validate(); err != nil {
			return 0, nil, fmt.Errorf("%w: %v", store.ErrInvalid, err)
		}
	}
	cfg, _ := json.Marshal(in)
	c.target = fmt.Sprintf("migration_source:%d:%s", id, in.VM)
	m, err := s.db.QueueMigrationCheck(c.r.Context(), c.user.Username, id, in.VM, cfg, "requested by "+c.user.Username)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, m, nil // the scheduler's next tick starts it
}

// runMigrationCheck runs one queued migration check job. The job is ok when the check reached a readiness, whatever
// it is; it fails when none could be reached (the check is then UNKNOWN, with the reason).
func (s *Server) runMigrationCheck(ctx context.Context, j store.Job) {
	d := &MigrationDetail{Checks: []verify.CheckResult{}, Timings: map[string]float64{}, Errors: []string{}}
	readiness, err := s.migrationCheck(ctx, j, d)
	cctx := context.WithoutCancel(ctx)
	if err != nil {
		d.Errors = append(d.Errors, err.Error())
	}
	if derr := s.db.SetJobDetail(cctx, j.ID, d); derr != nil {
		s.log.Error("job detail", "job", j.ID, "error", derr)
	}
	to, msg := "ok", ""
	if err != nil {
		to, msg = "failed", err.Error()
		s.log.Error("migration check failed", "job", j.ID, "error", err)
	}
	if _, err := s.db.SetJobState(cctx, "system", j.ID, to, "migration check: "+readiness, msg); err != nil {
		s.log.Error("job state", "job", j.ID, "error", err)
	}
}

func (s *Server) migrationCheck(ctx context.Context, j store.Job, d *MigrationDetail) (string, error) {
	if _, err := s.db.SetJobState(ctx, "system", j.ID, "running", "worker started", ""); err != nil {
		return verify.Unknown, err
	}
	mc, err := s.db.MigrationCheckByJob(ctx, j.ID)
	if err != nil {
		return verify.Unknown, err
	}
	readiness, reason, bundle, cerr := s.checkMigration(ctx, mc, d)
	if cerr != nil {
		readiness, reason = verify.Unknown, "not checked: "+cerr.Error()
	}
	if _, err := s.db.FinishMigrationCheck(context.WithoutCancel(ctx), "system", mc.ID, readiness, reason, bundle, d); err != nil {
		return readiness, errors.Join(cerr, err)
	}
	s.log.Info("migration check", "check", mc.ID, "vm", mc.VM, "readiness", readiness, "reason", reason)
	return readiness, cerr
}

// checkMigration runs the steps, the sandbox and the sealing. err means no readiness was reached.
func (s *Server) checkMigration(ctx context.Context, mc store.MigrationCheck, d *MigrationDetail) (readiness, reason, bundle string, err error) {
	src, err := s.db.MigrationSource(ctx, mc.SourceID)
	if err != nil {
		return "", "", "", err
	}
	var req MigrationCheckRequest
	if err := json.Unmarshal(mc.Config, &req); err != nil {
		return "", "", "", fmt.Errorf("the check's request: %w", err)
	}
	job := fmt.Sprintf("mc%d", mc.ID)
	jobDir := filepath.Join(s.cfg.ConvertJobsDir, job)
	in, out := filepath.Join(jobDir, "in"), convert.OutDir(s.cfg.ConvertJobsDir, job)
	// The jobs directory is setgid, group gaia-convert (install.md step 10d): what gaia server makes in it belongs to
	// that group, so the converter can read in/. gaia server never asks for setgid itself (gaia.service has
	// RestrictSUIDSGID=yes); the converter makes its own out directory, which gaia server reads and removes as a member
	// of the group.
	if fi, err := os.Stat(s.cfg.ConvertJobsDir); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSetgid == 0 {
		return "", "", "", fmt.Errorf("the converter's jobs directory %s must exist and be setgid, group gaia-convert (install.md step 10d): %v",
			s.cfg.ConvertJobsDir, err)
	}
	for _, p := range []string{jobDir, in} {
		if err := os.Mkdir(p, 0o750); err != nil {
			return "", "", "", fmt.Errorf("the converter's job directory: %w", err)
		}
	}
	defer func() {
		for _, d := range []string{jobDir, out} {
			if err := os.RemoveAll(d); err != nil {
				s.log.Error("removing a migration check's files", "dir", d, "error", err)
			}
		}
	}()
	callLog := filepath.Join(jobDir, "calls.jsonl")
	lf, err := os.Create(callLog)
	if err != nil {
		return "", "", "", err
	}
	defer lf.Close()
	cl, err := s.connectSource(ctx, src, lf)
	if err != nil {
		return "", "", "", err
	}
	defer cl.Logout(context.WithoutCancel(ctx))
	d.Source, d.Product = cl.Host, cl.Vim.ServiceContent.About.FullName
	inv, err := cl.Inventory(ctx)
	if err != nil {
		return "", "", "", err
	}
	var vm vsphereread.VM
	found := false
	for _, v := range inv {
		if v.Name == mc.VM {
			vm, found = v, true
		}
	}
	if found {
		if err := s.db.SetMigrationCheckSource(ctx, mc.ID, vm.Firmware, vm.GuestID); err != nil {
			return "", "", "", err
		}
	}
	checks := migrate.ChecksFile{}
	if req.Template != "" || len(req.Checks) > 0 || req.BootTimeoutS > 0 || req.ProbeTimeoutS > 0 || len(req.ExcludeDrivers) > 0 {
		checks[mc.VM] = migrate.Checks{Template: req.Template, Checks: req.Checks, BootTimeoutS: req.BootTimeoutS, ProbeTimeoutS: req.ProbeTimeoutS,
			ExcludeDrivers: req.ExcludeDrivers}
	}
	conv := &serverConverter{client: convert.Client{Socket: s.cfg.ConvertSocket}, job: job, dir: jobDir, out: out}
	p, err := migrate.Prepare(ctx, migrate.Options{Source: cl, Checks: checks, CallLog: callLog, Converter: conv,
		Dirs: func(string) (string, string) { return in, out }}, mc.VM, vm, found)
	if err != nil {
		return "", "", "", err
	}
	d.Converter = conv.result
	for k, v := range p.Timings {
		d.Timings[k] = v
	}

	// The converted disks go where the sandbox helper reads them (ADR-007 B1: plain files directly in its images
	// directory), and are removed after.
	var images []verify.DiskImage
	defer func() {
		for _, im := range images {
			if err := os.Remove(im.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.log.Error("removing a converted disk", "file", im.Path, "error", err)
			}
		}
	}()
	t := time.Now()
	prefix := "vd"
	if p.Config.Machine.DiskBus == "sata" || p.Config.Machine.DiskBus == "scsi" {
		prefix = "sd"
	}
	for i, disk := range p.Disks {
		target := prefix + string(rune('a'+i))
		dst := filepath.Join(s.cfg.SandboxImagesDir, fmt.Sprintf("%s-%s.qcow2", job, target))
		images = append(images, verify.DiskImage{Path: dst, Format: "qcow2", Target: target})
		if err := copyFile(disk, dst); err != nil {
			return "", "", "", fmt.Errorf("handing the converted disk to the sandbox: %w", err)
		}
	}
	if len(p.Disks) > 0 {
		d.Timings["handover"] = time.Since(t).Seconds()
	}

	// A converted Windows that does not pass may be stuck on a first-boot driver install (ADR-005 C): the sandbox keeps
	// what the guest wrote, and the converter reads virt-v2v's first-boot log out of it.
	windows := conv.result != nil && conv.result.VirtioWin != nil
	sreq := sandbox.Request{Config: p.Declared, Disks: images, PreChecks: p.PreChecks, SkipReason: p.SkipReason, KeepDisks: windows}
	dir, res, err := s.sandbox.Verify(ctx, sreq, s.cfg.bundlesDir())
	if res != nil {
		defer func() {
			for _, k := range res.KeptDisks {
				if err := os.Remove(filepath.Join(s.cfg.SandboxImagesDir, filepath.Base(k))); err != nil && !errors.Is(err, os.ErrNotExist) {
					s.log.Error("removing a guest's kept disk", "file", k, "error", err)
				}
			}
		}()
	}
	if err != nil {
		if dir != "" {
			os.RemoveAll(dir)
		}
		return "", "", "", err
	}
	if windows && len(res.KeptDisks) > 0 && len(p.Disks) > 0 {
		if k := s.firstbootStep(ctx, conv, p.Disks[0], filepath.Join(s.cfg.SandboxImagesDir, filepath.Base(res.KeptDisks[0])), dir, d); k != nil {
			res.Checks = append(res.Checks, *k)
			res.Verdict = verify.Worst(res.Verdict, k.Verdict)
		}
	}
	d.Checks = res.Checks
	for k, v := range res.Timings {
		d.Timings[k] = v
	}
	for _, call := range cl.Guard.Calls() {
		d.Calls++
		if !call.Allowed {
			d.Blocked++
		}
	}
	// The steps' files: what the source said, the downloads' hashes, the converter's answer and log, the converted
	// domain, and the read-only guard's log of every vSphere call.
	names := make([]string, 0, len(p.Extra))
	for k := range p.Extra {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if strings.Contains(k, "..") || filepath.IsAbs(k) {
			return "", "", "", fmt.Errorf("bundle file %q: not a bundle path", k)
		}
		if err := copyFile(p.Extra[k], filepath.Join(dir, k)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				d.Missing = append(d.Missing, k)
				continue
			}
			return "", "", "", fmt.Errorf("bundle file %s: %w", k, err)
		}
	}
	readiness, reason = res.Verdict, migrationReason(res, d)
	if d.Blocked > 0 { // Gaia tried a write call on a migration source: the guard stopped it, but that is a bug (ADR-003 §2)
		readiness, reason = verify.Critical, fmt.Sprintf("%d vSphere call(s) were not read-only and were blocked; %s", d.Blocked, reason)
	}
	summary, _ := json.MarshalIndent(map[string]any{"source": map[string]any{"name": src.Name, "host": cl.Host, "product": d.Product},
		"vm": mc.VM, "request": req, "readiness": readiness, "reason": reason, "converter": d.Converter, "timings_s": d.Timings,
		"vsphere_calls": d.Calls, "vsphere_blocked_calls": d.Blocked, "missing_files": d.Missing, "code": d.Code, "drivers": d.Drivers}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "migration", "check.json"), append(summary, '\n'), 0o644); err != nil {
		return "", "", "", err
	}
	key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
	if err != nil {
		return "", "", "", err
	}
	meta := map[string]string{"kind": "migration_check", "source": src.Name, "vm": mc.VM, "verdict": readiness, "check": fmt.Sprint(mc.ID)}
	if d.Converter != nil {
		meta["converter_image"] = d.Converter.Image
	}
	sum, err := evidence.Seal(dir, res.BundleID, meta, key)
	if err != nil {
		return "", "", "", fmt.Errorf("sealing %s: %w", res.BundleID, err)
	}
	e, err := evidence.Append(s.cfg.ledgerPath(), evidence.Entry{BundleID: res.BundleID, BundleSHA256: sum, Subject: mc.VM, Verdict: readiness}, key)
	if err != nil {
		if _, cerr := s.checkLedger(context.WithoutCancel(ctx), "system", "appending bundle "+res.BundleID+" failed: "+err.Error(), store.LedgerCheckFull); cerr != nil {
			s.log.Error("ledger check", "error", cerr)
		}
		return "", "", "", fmt.Errorf("ledger: %w", err)
	}
	d.Bundle, d.LedgerSeq = res.BundleID, e.Seq
	if _, err := s.db.AddMigrationEvidence(context.WithoutCancel(ctx), store.EvidenceBundle{BundleID: res.BundleID, LedgerSeq: e.Seq,
		BundleSHA256: sum, Verdict: readiness}, mc.ID); err != nil {
		return "", "", "", err
	}
	return readiness, reason, res.BundleID, nil
}

// firstbootStep reads virt-v2v's first-boot log out of what a converted Windows wrote in the sandbox and names a
// driver whose install failed on every boot (ADR-005 C: CRITICAL, driver_install_loop, with the driver's name). The
// log and the service's file list go into the bundle. It returns the step's result, or nil when the log shows no such
// loop (the guest's own failed check then stands as the reason). A log that cannot be read is recorded, not fatal:
// the check already has its readiness from the sandbox.
func (s *Server) firstbootStep(ctx context.Context, conv *serverConverter, disk, kept, bundle string, d *MigrationDetail) *verify.CheckResult {
	t := time.Now()
	after := filepath.Base(disk) + ".after.qcow2"
	if err := copyFile(kept, filepath.Join(conv.out, after)); err != nil {
		d.Errors = append(d.Errors, "handing the guest's disk to the converter: "+err.Error())
		return nil
	}
	defer os.Remove(filepath.Join(conv.out, after))
	fb, err := conv.client.Firstboot(ctx, convert.FirstbootRequest{Job: conv.job, Disk: filepath.Base(disk), After: after})
	if err != nil {
		d.Errors = append(d.Errors, "reading the first-boot log: "+err.Error())
		return nil
	}
	d.Timings["firstboot_read"] = time.Since(t).Seconds()
	if b, err := json.MarshalIndent(fb, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(bundle, "migration", "firstboot.json"), append(b, '\n'), 0o644); err != nil {
			d.Errors = append(d.Errors, "bundle file migration/firstboot.json: "+err.Error())
		}
	}
	if fb.State != "read" {
		d.Errors = append(d.Errors, "the first-boot log: "+fb.Detail)
		return nil
	}
	loops := migrate.DriverInstallLoops(fb.Log)
	if len(loops) == 0 {
		return nil
	}
	names := make([]string, len(loops))
	for i, l := range loops {
		names[i] = l.Driver
	}
	d.Code, d.Drivers = migrate.CodeDriverInstallLoop, names
	return &verify.CheckResult{Name: "drivers", Type: "firstboot", Verdict: verify.Critical, Seconds: time.Since(t).Seconds(),
		Detail: migrate.DriverLoopText(loops)}
}

// migrationReason says in one line why a check has its readiness: the first step or check that did not pass, or what
// passed.
func migrationReason(r *verify.Result, d *MigrationDetail) string {
	if d.Code == migrate.CodeDriverInstallLoop { // the cause behind the guest's failed checks
		for _, c := range r.Checks {
			if c.Name == "drivers" {
				return fmt.Sprintf("%s %s: %s", c.Name, strings.ToLower(c.Verdict), c.Detail)
			}
		}
	}
	for _, want := range []string{verify.Critical, verify.Unknown, verify.Warning} {
		for _, c := range r.Checks {
			if c.Verdict == want {
				return fmt.Sprintf("%s %s: %s", c.Name, strings.ToLower(c.Verdict), c.Detail)
			}
		}
	}
	boot := ""
	for _, c := range r.Checks {
		if c.Name == "boot" {
			boot = fmt.Sprintf(", booted in %.0f s", c.Seconds)
		}
	}
	return fmt.Sprintf("ready for KVM: converted in %.0f s%s, all %d checks passed, %d vSphere calls, all read-only",
		d.Timings["conversion"], boot, len(r.Checks), d.Calls)
}

// serverConverter has gaia convert serve convert the job's downloaded VM.
type serverConverter struct {
	client convert.Client
	job    string
	dir    string // the job directory gaia server made (in/, the call log, the converter's answer)
	out    string // the converter's out directory
	result *convert.Result
}

func (c *serverConverter) Convert(ctx context.Context, in migrate.ConvertInput) migrate.ConvertOutput {
	res, err := c.client.Convert(ctx, convert.Request{Job: c.job, VMX: filepath.Base(in.VMX), Name: in.Name, Windows: in.Windows,
		ExcludeDrivers: in.ExcludeDrivers})
	if err != nil {
		return migrate.ConvertOutput{State: "not_run", Detail: err.Error()}
	}
	c.result = &res
	out := migrate.ConvertOutput{State: res.State, Detail: res.Detail, Seconds: res.Seconds, VirtioWin: res.VirtioWin != nil, Info: res,
		Excluded: res.ExcludedPaths, Files: map[string]string{}}
	if b, err := json.MarshalIndent(res, "", "  "); err == nil {
		p := filepath.Join(c.dir, "converter.json")
		if os.WriteFile(p, append(b, '\n'), 0o640) == nil {
			out.Files["migration/converter.json"] = p
		}
	}
	if res.Log != "" {
		out.Log = filepath.Join(c.out, res.Log)
	}
	for _, d := range res.Disks {
		out.Disks = append(out.Disks, filepath.Join(c.out, d))
	}
	if res.DomainXML != "" {
		out.DomainXML = filepath.Join(c.out, res.DomainXML)
	}
	return out
}

// copyFile copies a regular file (in-kernel where the filesystem can: Go uses copy_file_range between files).
func copyFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file (%v)", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	g, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(g, f); err != nil {
		g.Close()
		return err
	}
	return g.Close()
}

func (s *Server) converterInfo(c *call) (int, any, error) {
	in, err := convert.Client{Socket: s.cfg.ConvertSocket}.Info(c.r.Context())
	if err != nil {
		return http.StatusOK, ConverterState{Reachable: false, Detail: err.Error()}, nil
	}
	return http.StatusOK, ConverterState{Reachable: true, Detail: "the converter service answers", Info: &in}, nil
}

// ConverterState is what gaia server knows about its converter service.
type ConverterState struct {
	Reachable bool          `json:"reachable"`
	Detail    string        `json:"detail"`
	Info      *convert.Info `json:"info,omitempty"`
}

// readinessReport serves one check's readiness report (tar.gz): its signed bundle, the ledger and the keys it verifies
// with, a summary, and how to verify it offline.
func (s *Server) readinessReport(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	mc, err := s.db.MigrationCheck(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	if mc.BundleID == "" {
		return 0, nil, fmt.Errorf("%w: migration check %d has no bundle yet (readiness %q)", store.ErrConflict, id, mc.Readiness)
	}
	src, err := s.db.MigrationSource(ctx, mc.SourceID)
	if err != nil {
		return 0, nil, err
	}
	c.target = "migration_check:" + fmt.Sprint(id)
	anchor, err := evidence.GenesisKey(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	current, err := currentKey(s.cfg)
	if err != nil {
		return 0, nil, err
	}
	ledger, err := os.ReadFile(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	now := s.now().UTC()
	root := fmt.Sprintf("gaia-readiness-%s-%d", mc.VM, mc.ID)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	sums := map[string]string{}
	add := func(name string, body []byte) error {
		h := sha256.Sum256(body)
		sums[name] = hex.EncodeToString(h[:])
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)), ModTime: now}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	var d MigrationDetail
	if err := json.Unmarshal(mc.Detail, &d); err != nil {
		return 0, nil, fmt.Errorf("the check's detail: %w", err)
	}
	summary, _ := json.MarshalIndent(map[string]any{"check": mc.ID, "source": src.Name, "source_host": d.Source, "source_product": d.Product,
		"vm": mc.VM, "readiness": mc.Readiness, "reason": mc.Reason, "firmware": mc.Firmware, "guest_id": mc.GuestID,
		"checked_at": mc.FinishedAt, "checks": d.Checks, "timings_s": d.Timings, "converter": d.Converter,
		"vsphere_calls": d.Calls, "vsphere_blocked_calls": d.Blocked, "bundle": mc.BundleID, "ledger_seq": d.LedgerSeq}, "", "  ")
	for _, f := range []struct {
		name string
		body []byte
	}{{"readiness.json", append(summary, '\n')}, {"anchor.pub", []byte(base64.StdEncoding.EncodeToString(anchor) + "\n")},
		{"keys/evidence-key-current.pub", current}, {"ledger.jsonl", ledger}} {
		if err := add(f.name, f.body); err != nil {
			return 0, nil, err
		}
	}
	bdir := filepath.Join(s.cfg.bundlesDir(), mc.BundleID)
	err = filepath.WalkDir(bdir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		if !de.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.cfg.bundlesDir(), p)
		return add("bundles/"+filepath.ToSlash(rel), b)
	})
	if err != nil {
		return 0, nil, err
	}
	var lines []string
	for _, ch := range d.Checks {
		lines = append(lines, fmt.Sprintf("  %-18s %-8s %s", ch.Name, ch.Verdict, oneLine(ch.Detail, 140)))
	}
	readme := fmt.Sprintf(`Migration readiness report: %s on %s (%s), check %d, %s.

Readiness: %s
  %s

Steps and checks
%s

Every step ran against the source with read-only calls only (ADR-003): %d vSphere calls, %d blocked.
The signed bundle (bundles/%s) holds the source's facts, the download hashes, the converter's image and log, the
converted domain, the console screenshots and the serial log, and the call log.

Verify, without Gaia's server
  1. Compare the trust anchor's key_id, %s, with the one recorded when Gaia was installed.
  2. The bundle, with Gaia's command-line tool or the independent Python verifier (Gaia's source,
     docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py):
       gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/%s
       python3 -I verify.py bundle bundles/%s --key keys/evidence-key-current.pub
  3. Its ledger entry: line %d of ledger.jsonl names bundle_sha256 %s, signed and chained from anchor.pub. The
     auditor's export (all bundles) verifies the whole ledger: gaia evidence verify -pubkey anchor.pub -ledger ...
`, mc.VM, src.Name, d.Source, mc.ID, mc.FinishedAt, mc.Readiness, mc.Reason, strings.Join(lines, "\n"), d.Calls, d.Blocked,
		mc.BundleID, evidence.KeyID(anchor), mc.BundleID, mc.BundleID, d.LedgerSeq, bundleSum(ctx, s, mc.BundleID))
	if err := add("README.txt", []byte(readme)); err != nil {
		return 0, nil, err
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&sb, "%s  %s\n", sums[n], n)
	}
	if err := add("SHA256SUMS", sb.Bytes()); err != nil {
		return 0, nil, err
	}
	if err := tw.Close(); err != nil {
		return 0, nil, err
	}
	if err := gz.Close(); err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("readiness report of %s (%s)", mc.VM, mc.Readiness)
	c.raw = true
	c.w.Header().Set("Content-Type", "application/gzip")
	c.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", root+".tar.gz"))
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(buf.Bytes())
	return http.StatusOK, nil, err
}

func bundleSum(ctx context.Context, s *Server, bundleID string) string {
	es, err := s.db.EvidenceBundles(ctx, 0, 1000000)
	if err != nil {
		return "?"
	}
	for _, e := range es {
		if e.BundleID == bundleID {
			return e.BundleSHA256
		}
	}
	return "?"
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// MigrationView is the migration screen in one request (T21): the converter's state, the sources, and the checks
// newest first with what each did.
type MigrationView struct {
	Clock     ServerClock             `json:"clock"`
	Converter ConverterState          `json:"converter"`
	Sources   []store.MigrationSource `json:"sources"`
	Checks    []MigrationCheckView    `json:"checks"`
}

// MigrationCheckView is one check as the screen shows it.
type MigrationCheckView struct {
	ID          int64            `json:"id"`
	SourceID    int64            `json:"source_id"`
	Source      string           `json:"source"`
	VM          string           `json:"vm"`
	JobState    string           `json:"job_state"` // queued, running, ok, failed
	Readiness   string           `json:"readiness"` // "" until it ends
	Reason      string           `json:"reason"`
	Firmware    string           `json:"firmware"`
	GuestID     string           `json:"guest_id"`
	CreatedAt   string           `json:"created_at"`
	StartedAt   string           `json:"started_at"`
	FinishedAt  string           `json:"finished_at"`
	BundleID    string           `json:"bundle_id"`
	EvidenceID  int64            `json:"evidence_id"` // 0 without a bundle
	Screenshots []string         `json:"screenshots"`
	Result      *MigrationDetail `json:"result,omitempty"` // once it ended
}

func (s *Server) migrationView(c *call) (int, any, error) {
	ctx := c.r.Context()
	_, conv, _ := s.converterInfo(c)
	out := MigrationView{Clock: serverClock(s.now()), Converter: conv.(ConverterState), Checks: []MigrationCheckView{}}
	var err error
	if out.Sources, err = s.db.MigrationSources(ctx); err != nil {
		return 0, nil, err
	}
	names := map[int64]string{}
	for _, m := range out.Sources {
		names[m.ID] = m.Name
	}
	ev, err := s.db.EvidenceBundles(ctx, 0, 1000000)
	if err != nil {
		return 0, nil, err
	}
	evID := map[string]int64{}
	for _, e := range ev {
		evID[e.BundleID] = e.ID
	}
	mcs, err := s.db.MigrationChecks(ctx, 0, 200)
	if err != nil {
		return 0, nil, err
	}
	for _, m := range mcs {
		j, err := s.db.Job(ctx, m.JobID)
		if err != nil {
			return 0, nil, err
		}
		v := MigrationCheckView{ID: m.ID, SourceID: m.SourceID, Source: names[m.SourceID], VM: m.VM, JobState: j.State, Readiness: m.Readiness,
			Reason: m.Reason, Firmware: m.Firmware, GuestID: m.GuestID, CreatedAt: m.CreatedAt, StartedAt: j.StartedAt, FinishedAt: m.FinishedAt,
			BundleID: m.BundleID, EvidenceID: evID[m.BundleID], Screenshots: []string{}}
		if m.BundleID != "" {
			v.Screenshots = s.screenshots(m.BundleID)
		}
		if m.Readiness != "" {
			var d MigrationDetail
			if err := json.Unmarshal(m.Detail, &d); err != nil {
				return 0, nil, fmt.Errorf("migration check %d: detail: %w", m.ID, err)
			}
			v.Result = &d
		}
		out.Checks = append(out.Checks, v)
	}
	return http.StatusOK, out, nil
}

// currentKey is the installation's current public key in the specification's key format (base64 of the 32 bytes),
// which every verifier reads (the PEM file on the server is Gaia's own).
func currentKey(c Config) ([]byte, error) {
	pub, err := evidence.LoadPublicKey(c.pubPath())
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(pub) + "\n"), nil
}

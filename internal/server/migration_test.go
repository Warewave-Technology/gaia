package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/Warewave-Technology/gaia/internal/convert"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// fakeConverter stands in for gaia convert serve: it checks the job directory gaia server made and "converts" by
// writing a disk, a domain XML and a log into out/.
type fakeConverter struct {
	t       *testing.T
	jobs    string
	got     []convert.Request
	refuse  bool
	osID    string
	content []byte
}

func (fc *fakeConverter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req convert.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	fc.got = append(fc.got, req)
	in, out := filepath.Join(fc.jobs, req.Job, "in"), filepath.Join(fc.jobs, req.Job, "out")
	if _, err := os.Stat(filepath.Join(in, req.VMX)); err != nil {
		http.Error(w, "no vmx in the job's in/: "+err.Error(), 400)
		return
	}
	res := convert.Result{State: "converted", Image: "sha256:" + strings.Repeat("a", 64), Versions: "virt-v2v-2.9", Log: "virt-v2v.log",
		Disks: []string{}, Seconds: 3}
	os.WriteFile(filepath.Join(out, res.Log), []byte("[ 1.0] Setting up the source\n"), 0o640)
	if fc.refuse {
		res.State, res.Detail = "failed", "virt-v2v: error: inspection could not detect the source guest"
		json.NewEncoder(w).Encode(res)
		return
	}
	os.WriteFile(filepath.Join(out, req.Name+"-sda"), fc.content, 0o644)
	os.WriteFile(filepath.Join(out, req.Name+".xml"), []byte(`<domain type='kvm'><name>`+req.Name+`</name><metadata>`+
		`<libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0"><libosinfo:os id="`+fc.osID+`"/></libosinfo:libosinfo>`+
		`</metadata><memory unit='KiB'>1048576</memory><vcpu>1</vcpu><os><type arch='x86_64' machine='q35'>hvm</type></os>`+
		`<devices><disk device='disk'><target dev='vda' bus='virtio'/></disk><interface type='bridge'><mac address='00:0c:29:36:63:62'/>`+
		`<model type='virtio'/></interface></devices></domain>`), 0o644)
	res.Disks, res.DomainXML = []string{req.Name + "-sda"}, req.Name+".xml"
	json.NewEncoder(w).Encode(res)
}

// migrationSandbox stands in for gaia sandbox serve on a migration check: the disk it gets is the converted one, and
// its verdict is the worst of the steps before and its own boot, as verify.Run's.
type migrationSandbox struct {
	want []byte
	got  []sandbox.Request
}

func (ms *migrationSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req sandbox.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ms.got = append(ms.got, req)
	checks := append([]verify.CheckResult{}, req.PreChecks...)
	if len(req.Disks) == 1 {
		b, err := os.ReadFile(req.Disks[0].Path)
		if err != nil || !bytes.Equal(b, ms.want) || req.Disks[0].Format != "qcow2" {
			http.Error(w, fmt.Sprintf("disk %+v is not the converted one (%v)", req.Disks[0], err), 400)
			return
		}
		checks = append(checks, verify.CheckResult{Name: "boot", Type: verify.CheckBootSerial, Verdict: verify.OK, Seconds: 14, Detail: "login prompt"})
	} else {
		checks = append(checks, verify.CheckResult{Name: "boot", Type: verify.CheckBootSerial, Verdict: verify.Unknown, Detail: "not run: " + req.SkipReason})
	}
	var vs []string
	for _, c := range checks {
		vs = append(vs, c.Verdict)
	}
	res := verify.Result{Format: "gaia-verify-result/v1", BundleID: fmt.Sprintf("%s-%d", req.Config.VM, time.Now().UnixNano()), VM: req.Config.VM,
		Verdict: verify.Worst(vs...), Checks: checks, Timings: map[string]float64{"ready": 14}, Errors: []string{}}
	b, _ := json.Marshal(res)
	w.Header().Set("X-Gaia-Bundle-ID", res.BundleID)
	tw := tar.NewWriter(w)
	tw.WriteHeader(&tar.Header{Name: "result.json", Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
	tw.Write(b)
	tw.Close()
}

func serveUnix(t *testing.T, sock string, h http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

// A migration check end to end on the server, against vcsim: the source's password stays out of answers and the
// audit log; the inventory takes read-only calls only; a check downloads the VM into the converter's job directory,
// the converter's disk reaches the sandbox, and the readiness bundle is sealed into the ledger with the steps' files;
// the readiness report verifies. A converter that cannot be reached or that refuses the guest, and a server stopped
// mid-check, never give a ready VM.
func TestMigrationCheck(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := f.s
	m := simulator.ESX()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	defer m.Remove()
	m.Service.TLS = new(tls.Config) // a source is https only
	vc := m.Service.NewServer()
	defer vc.Close()
	plain, err := govmomi.NewClient(ctx, vc.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := vc.URL.User.Password()
	bare := *vc.URL
	bare.User = nil

	sockDir, err := os.MkdirTemp("", "gmc") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	s.cfg.ConvertJobsDir = filepath.Join(f.dir, "convert-jobs")
	s.cfg.ConvertSocket = filepath.Join(sockDir, "c.sock")
	s.cfg.SandboxImagesDir = filepath.Join(f.dir, "sandbox-images")
	os.MkdirAll(s.cfg.ConvertJobsDir, 0o770)
	os.MkdirAll(s.cfg.SandboxImagesDir, 0o755)
	disk := bytes.Repeat([]byte("converted "), 1000)
	conv := &fakeConverter{t: t, jobs: s.cfg.ConvertJobsDir, osID: "http://debian.org/debian/12", content: disk}
	serveUnix(t, s.cfg.ConvertSocket, conv)
	sb := &migrationSandbox{want: disk}
	sbSock := filepath.Join(sockDir, "s.sock")
	s.sandbox = sandbox.NewClient(sbSock)
	serveUnix(t, sbSock, sb)

	op := f.tokens[store.RoleOperator]
	const secret = "the vSphere password"
	body := fmt.Sprintf(`{"name":"esx","url":%q,"username":%q,"password":%q,"insecure":true}`, bare.String(), vc.URL.User.Username(), secret)
	var raw []byte
	if st := f.do(op, "POST", "/api/v1/migration-sources", strings.Replace(body, secret, pw, 1), &raw); st != 201 || strings.Contains(string(raw), pw) {
		t.Fatalf("source: %d %s", st, raw)
	}
	var src store.MigrationSource
	json.Unmarshal(raw, &src)
	if fi, err := os.Stat(s.cfg.sourcePasswordFile("esx")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v %v", fi, err)
	}
	if st := f.do(op, "POST", "/api/v1/migration-sources", strings.Replace(body, secret, pw, 1), nil); st != 409 {
		t.Fatalf("the same source again: %d", st)
	}
	if st := f.do(f.tokens[store.RoleAuditor], "GET", fmt.Sprintf("/api/v1/migration-sources/%d/inventory", src.ID), "", nil); st != 403 {
		t.Fatalf("an auditor's inventory: %d", st)
	}
	var inv SourceInventory
	if st := f.do(op, "GET", fmt.Sprintf("/api/v1/migration-sources/%d/inventory", src.ID), "", &inv); st != 200 || len(inv.VMs) < 2 || inv.Calls == 0 || inv.Blocked != 0 {
		t.Fatalf("inventory: %d %+v", st, inv)
	}
	on := inv.VMs[1]
	if on.PowerState != "poweredOn" || !strings.Contains(on.Checkable, "powered on") {
		t.Fatalf("a powered-on VM: %+v", on)
	}
	name := inv.VMs[0].Name
	vm := object.NewVirtualMachine(plain.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: vmRef(t, plain, name)})
	task, err := vm.PowerOff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task.Wait(ctx)

	run := func(vmName string) store.MigrationCheck {
		t.Helper()
		var mc store.MigrationCheck
		if st := f.do(op, "POST", fmt.Sprintf("/api/v1/migration-sources/%d/checks", src.ID), fmt.Sprintf(`{"vm":%q,"template":"linux-boot"}`, vmName), &mc); st != 201 {
			t.Fatalf("queue a check: %d", st)
		}
		if st := f.do(op, "POST", fmt.Sprintf("/api/v1/migration-sources/%d/checks", src.ID), fmt.Sprintf(`{"vm":%q}`, vmName), nil); st != 409 {
			t.Fatalf("a second check of the same VM while one is queued: %d", st)
		}
		j, err := s.db.Job(ctx, mc.JobID)
		if err != nil || j.Kind != "migration_check" || j.State != "queued" {
			t.Fatalf("job %+v %v", j, err)
		}
		s.runMigrationCheck(ctx, j)
		if mc, err = s.db.MigrationCheck(ctx, mc.ID); err != nil {
			t.Fatal(err)
		}
		return mc
	}

	// Ready.
	mc := run(name)
	if mc.Readiness != verify.OK || mc.BundleID == "" || mc.Firmware != "bios" || !strings.HasPrefix(mc.Reason, "ready for KVM") {
		t.Fatalf("a ready VM: %+v", mc)
	}
	if len(conv.got) != 1 || conv.got[0].Windows || conv.got[0].Job != fmt.Sprintf("mc%d", mc.ID) {
		t.Fatalf("converter requests %+v", conv.got)
	}
	got := sb.got[len(sb.got)-1]
	if got.Config.Template != "linux-boot" || len(got.Config.Checks) != 0 || len(got.PreChecks) != 6 || got.SkipReason != "" {
		t.Fatalf("sandbox request %+v", got)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.ConvertJobsDir, fmt.Sprintf("mc%d", mc.ID))); !os.IsNotExist(err) {
		t.Fatalf("the job directory is left: %v", err)
	}
	if es, _ := os.ReadDir(s.cfg.SandboxImagesDir); len(es) != 0 {
		t.Fatalf("converted disks left for the sandbox: %v", es)
	}
	bdir := filepath.Join(s.cfg.bundlesDir(), mc.BundleID)
	for _, n := range []string{"migration/check.json", "migration/calls.jsonl", "migration/converter.json", "migration/virt-v2v.log",
		"migration/converted-domain.xml", "migration/inventory.json", "migration/downloads.json"} {
		if _, err := os.Stat(filepath.Join(bdir, n)); err != nil {
			t.Errorf("bundle file %s: %v", n, err)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(bdir, "migration", "calls.jsonl"))
	if !strings.Contains(string(calls), `"op":"RetrievePropertiesEx"`) || strings.Contains(string(calls), `"allowed":false`) || strings.Contains(string(calls), pw) {
		t.Fatalf("call log:\n%s", calls)
	}
	pub, _ := evidence.LoadPublicKey(s.cfg.pubPath())
	if lr, err := evidence.VerifyLedger(s.cfg.ledgerPath(), s.cfg.bundlesDir(), pub); err != nil || !lr.Intact {
		t.Fatalf("ledger %+v %v", lr, err)
	}

	// The readiness report: anyone may take it, and its bundle verifies with the key it carries.
	var rep []byte
	if st := f.do(f.tokens[store.RoleAuditor], "GET", fmt.Sprintf("/api/v1/migration-checks/%d/report", mc.ID), "", &rep); st != 200 {
		t.Fatalf("report: %d %s", st, rep)
	}
	files := untarMap(t, rep)
	root := fmt.Sprintf("gaia-readiness-%s-%d/", name, mc.ID)
	readme := string(files[root+"README.txt"])
	if !strings.Contains(readme, "Readiness: OK") || !strings.Contains(readme, "gaia evidence verify -pubkey keys/evidence-key-current.pub -bundle bundles/"+mc.BundleID) ||
		len(files[root+"bundles/"+mc.BundleID+"/manifest.json"]) == 0 || len(files[root+"SHA256SUMS"]) == 0 {
		t.Fatalf("report files %v\n%s", keys(files), readme)
	}
	ex := t.TempDir()
	for n, b := range files {
		p := filepath.Join(ex, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
	}
	cur, err := evidence.LoadPublicKey(filepath.Join(ex, root, "keys", "evidence-key-current.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if br := evidence.VerifyBundle(filepath.Join(ex, root, "bundles", mc.BundleID), cur); br.Verdict != evidence.OK {
		t.Fatalf("the report's bundle: %+v", br)
	}

	// The converter refuses the guest: not ready.
	conv.refuse = true
	if mc = run(name); mc.Readiness != verify.Critical || !strings.Contains(mc.Reason, "conversion critical") {
		t.Fatalf("a refused conversion: %+v", mc)
	}
	// The converter cannot be reached: not checked, never ready.
	conv.refuse = false
	s.cfg.ConvertSocket = filepath.Join(sockDir, "nothing.sock")
	if mc = run(name); mc.Readiness != verify.Unknown || !strings.Contains(mc.Reason, "conversion unknown") || sb.got[len(sb.got)-1].SkipReason == "" {
		t.Fatalf("no converter: %+v", mc)
	}
	// A VM the source does not have.
	if mc = run("no-such-vm"); mc.Readiness != verify.Unknown || !strings.Contains(mc.Reason, "no VM of this name") {
		t.Fatalf("a missing VM: %+v", mc)
	}

	// The server stops while a check runs: on the next start the check is UNKNOWN, not "still running".
	var stuck store.MigrationCheck
	if st := f.do(op, "POST", fmt.Sprintf("/api/v1/migration-sources/%d/checks", src.ID), fmt.Sprintf(`{"vm":%q}`, name), &stuck); st != 201 {
		t.Fatalf("queue: %d", st)
	}
	s.db.SetJobState(ctx, "system", stuck.JobID, "running", "test", "")
	s.db.FailInterruptedJobs(ctx, "stopped")
	if n, err := s.db.FinishInterruptedMigrationChecks(ctx, "not checked: the server stopped while the check ran"); err != nil || n != 1 {
		t.Fatalf("interrupted: %d %v", n, err)
	}
	if stuck, _ = s.db.MigrationCheck(ctx, stuck.ID); stuck.Readiness != verify.Unknown {
		t.Fatalf("an interrupted check: %+v", stuck)
	}
	if st := f.do(op, "DELETE", fmt.Sprintf("/api/v1/migration-sources/%d", src.ID), "", nil); st != 409 {
		t.Fatalf("removing a source with checks: %d", st)
	}
	// The password is nowhere in the audit log.
	es, _ := s.db.Audit(ctx, 0, 100000)
	for _, e := range es {
		if strings.Contains(e.Detail, pw) || strings.Contains(e.Target, pw) {
			t.Fatalf("audit entry with the password: %+v", e)
		}
	}
}

func vmRef(t *testing.T, c *govmomi.Client, name string) string {
	t.Helper()
	vm, err := object.NewSearchIndex(c.Client).FindByInventoryPath(context.Background(), "/ha-datacenter/vm/"+name)
	if err != nil || vm == nil {
		t.Fatalf("vm %s: %v", name, err)
	}
	return vm.Reference().Value
}

func untarMap(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name], _ = io.ReadAll(tr)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

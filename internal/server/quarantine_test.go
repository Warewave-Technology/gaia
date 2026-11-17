package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// quarantineFixture: two VMs whose disks hold the same data (so deduplication shares every block), one checked point
// each, and the data pack damaged in its middle, inside a block both points use.
func quarantineFixture(t *testing.T) (*fixture, store.Repository, []store.VM, []store.Point, []byte) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t)
	var repo store.Repository
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`,
		filepath.Join(f.dir, "repo"), kitPassphrase), &repo); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	h, _ := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm", Address: "kvm", SSHUser: "gaia-backup"})
	pol, err := f.s.db.CreatePolicy(ctx, store.PolicyInput{Name: "p", RepositoryID: repo.ID, BackupIntervalS: 900, TestIntervalS: 3600, KeepLast: 4,
		RPOTargetS: 8 * 3600, RTOTargetS: 600, MaxProofAgeS: 8 * 3600, CheckTemplate: "linux-boot"})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 3*diskstore.BlockSize) // incompressible: three data contents of 1 MiB fill the pack
	r := rand.New(rand.NewPCG(5, 6))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	var vms []store.VM
	var pts []store.Point
	for _, name := range []string{"web1", "web2"} {
		vm, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: name, PolicyID: &pol.ID})
		if err != nil {
			t.Fatal(err)
		}
		vms = append(vms, vm)
		pts = append(pts, addPoint(t, f.s, repo, vm, data))
	}
	var packs []string
	filepath.Walk(filepath.Join(f.dir, "repo"), func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(filepath.Join(f.dir, "repo"), p)
		if err == nil && !fi.IsDir() && strings.HasPrefix(rel, "p") {
			packs = append(packs, p)
		}
		return nil
	})
	if len(packs) != 1 {
		t.Fatalf("want one data pack, have %v", packs)
	}
	b, _ := os.ReadFile(packs[0])
	b[len(b)/2] ^= 0xff
	os.WriteFile(packs[0], b, 0o600)
	return f, repo, vms, pts, data
}

// addPoint backs data up as the VM's disk vda (as a backup job would, without a host) and records a checked point.
func addPoint(t *testing.T, s *Server, repo store.Repository, vm store.VM, data []byte) store.Point {
	t.Helper()
	ctx := context.Background()
	st, err := s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)
	j, _ := s.db.CreateJob(ctx, "system", "backup", &vm.ID, "test")
	sp, err := st.Backup(ctx, diskstore.BackupRequest{Domain: vm.Name, Disk: "vda", Checkpoint: "c", Size: uint64(len(data)),
		Changed: []diskstore.Range{{Offset: 0, Length: uint64(len(data))}}, Sources: []diskstore.Source{bytes.NewReader(data)}})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := st.CheckPoint(ctx, sp.ID); err != nil || len(c.Problems) != 0 {
		t.Fatalf("post-backup check: %+v %v", c, err)
	}
	p, err := s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vm.ID, JobID: j.ID, RepositoryID: repo.ID, Kind: "full", Checkpoint: "c",
		TakenAt: time.Now().UTC().Format(time.RFC3339Nano), Machine: json.RawMessage(`{}`)},
		[]store.PointDisk{{Target: "vda", SizeBytes: int64(len(data)), BlockSize: diskstore.BlockSize, StoreID: sp.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = s.db.SetPointCheck(ctx, "system", p.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	return p
}

func runVerification(t *testing.T, s *Server, repo store.Repository, mode string) store.Verification {
	t.Helper()
	ctx := context.Background()
	v, err := s.db.StartVerification(ctx, "admin", repo.ID, mode, "test")
	if err != nil {
		t.Fatal(err)
	}
	s.verify(ctx, "admin", repo, v)
	if v, err = s.db.Verification(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	return v
}

// ADR-007 B3 end to end in the server: the verification quarantines the damaged content, both points that use it fail
// with the content ID as the reason (the second one found by the search, not read), both VMs must back up full, and the
// quarantine is in the catalog and the audit log. The next point of the same data restores byte for byte, and a later
// verification passes, skipping the failed points.
func TestVerificationQuarantines(t *testing.T) {
	ctx := context.Background()
	f, repo, vms, pts, data := quarantineFixture(t)
	f.s.cfg.VerifySample = 1e-9 // one block per disk: the damaged block of the second point is found by the search
	var v store.Verification
	for range 20 { // a sample of one block in three may miss the damaged one: verify until it is hit
		v = runVerification(t, f.s, repo, "sampled")
		if v.State != "ok" {
			break
		}
	}
	if v.State != "critical" {
		t.Fatalf("verification %+v", v)
	}
	var aff []Affected
	json.Unmarshal(v.Affected, &aff)
	if len(aff) != 2 || len(aff[0].Contents) != 1 || !slices.Equal(aff[0].Contents, aff[1].Contents) {
		t.Fatalf("affected %+v", aff)
	}
	damaged := aff[0].Contents[0]
	for _, p := range pts {
		got, _ := f.s.db.Point(ctx, p.ID)
		changes, _ := f.s.db.StateChanges(ctx, "point_check", p.ID, 0, 10)
		last := changes[len(changes)-1]
		if got.CheckState != "failed" || !strings.Contains(last.Reason, damaged) || !strings.Contains(last.Reason, "quarantined") {
			t.Fatalf("point %d: %s, %+v", p.ID, got.CheckState, last)
		}
	}
	for _, vm := range vms {
		got, _ := f.s.db.VM(ctx, vm.ID)
		if !strings.HasPrefix(got.FullBackupReason, fmt.Sprintf("full: repository verification %d quarantined damaged contents", v.ID)) {
			t.Fatalf("VM %s: full backup reason %q", vm.Name, got.FullBackupReason)
		}
		if r := fullReason(got.FullBackupReason, &pts[0], nil, nil, nil, repo.ID); r != got.FullBackupReason {
			t.Fatalf("the next backup's reason: %q", r)
		}
	}
	qs, err := f.s.db.QuarantinedContents(ctx, repo.ID)
	if err != nil || len(qs) != 1 || qs[0].ContentID != damaged || qs[0].VerificationID != v.ID || len(qs[0].Points) != 2 {
		t.Fatalf("quarantined contents %+v %v", qs, err)
	}
	audit, _ := f.s.db.Audit(ctx, 0, 1000)
	if a := audit[len(audit)-1]; a.Action != "repository quarantine" || !strings.Contains(a.Detail, damaged) {
		t.Fatalf("audit %+v", a)
	}

	// The next backup of web1 (full, as demanded) stores the block again.
	p3 := addPoint(t, f.s, repo, vms[0], data)
	if err := f.s.db.ClearFullBackupReason(ctx, "system", vms[0].ID, "test"); err != nil {
		t.Fatal(err)
	}
	pd, _ := f.s.db.PointDisks(ctx, p3.ID)
	st, err := f.s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := st.Point(ctx, pd[0].StoreID)
	out := filepath.Join(t.TempDir(), "vda.raw")
	if _, err := st.Restore(ctx, sp, out, 2); err != nil {
		t.Fatal(err)
	}
	st.Close(ctx)
	if got, _ := os.ReadFile(out); !bytes.Equal(got, data) {
		t.Fatal("the new point does not restore byte for byte")
	}
	v2 := runVerification(t, f.s, repo, "full")
	if v2.State != "ok" || v2.PointsChecked != 1 || v2.PointsSkipped != 2 {
		t.Fatalf("verification after the new point: %+v", v2)
	}
	if qs, _ := f.s.db.QuarantinedContents(ctx, repo.ID); len(qs) != 1 {
		t.Fatalf("a second quarantine: %+v", qs)
	}
}

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// The OS class (ADR-007 C13 as C17 settled it): the os: tag first, then the check template's name, then the default
// (linux); there is no libvirt guest-information step. The basis says which.
func TestOSClassOrder(t *testing.T) {
	for _, c := range []struct{ tag, template, class, basis string }{
		{"windows", "linux-basic", "windows", "tag os:windows"},
		{"linux", "windows-basic", "linux", "tag os:linux"},
		{"Windows2022", "linux-basic", "windows", "tag os:Windows2022"},
		{"", "windows-basic", "windows", "check template windows-basic"},
		{"", "linux-boot", "linux", "check template linux-boot"},
		{"bsd", "linux-boot", "linux", "check template linux-boot (tag os:bsd is neither windows nor linux)"},
		{"", "custom-http", "linux", "check template custom-http"}, // the default
		{"windows", "", "windows", "tag os:windows"},               // the tag without a template
		{"", "", "linux", "check template "},                       // nothing to go by: the default
	} {
		if class, basis := osClass(c.tag, c.template); class != c.class || basis != c.basis {
			t.Errorf("tag %q template %q: %s (%s), want %s (%s)", c.tag, c.template, class, basis, c.class, c.basis)
		}
	}
}

// A restore reserves the full size of the VM's disks in the images directory (ADR-007 C13), beside what running tests
// hold; a restore that does not fit is refused, and a release gives the room back.
func TestRestoreReservesFullDisks(t *testing.T) {
	disks := []store.PointDisk{{Target: "vda", SizeBytes: 20 << 30, WrittenBytes: 1 << 20}, {Target: "vdb", SizeBytes: 5 << 30, WrittenBytes: 0}}
	if got := restoreReservation(disks); got != 25<<30 {
		t.Fatalf("reservation %d, want the disks' full size %d", got, int64(25<<30))
	}
	s := &Server{cfg: Config{SandboxImagesDir: t.TempDir()}}
	free, err := freeBytes(s.cfg.SandboxImagesDir)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.reserveImages(0, free/2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.reserveImages(0, free/2+free/4); err == nil || !strings.Contains(err.Error(), "held by running tests") {
		t.Fatalf("a second restore beyond the free room: %v", err)
	}
	release()
	r2, err := s.reserveImages(0, free/2+free/4)
	if err != nil {
		t.Fatalf("after the release: %v", err)
	}
	r2()
	if _, err := s.reserveImages(0, free+1<<40); err == nil {
		t.Fatal("a restore larger than the free room was reserved")
	}
}

// What-if (ADR-007 C13): every role may call it, it is not audited, and it is rate-limited per session: the 11th
// call in 10 s is 429 too_many_requests with reason rate_limited and Retry-After; another session is not affected.
func TestWhatIfRolesAuditRateLimit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	_, before, err := f.s.db.AuditNewest(ctx, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{store.RoleAdmin, store.RoleOperator, store.RoleAuditor} {
		var v CapacityView
		if code := f.do(f.tokens[r], "POST", "/api/v1/capacity/what-if", `{"slots":2}`, &v); code != 200 || v.Slots != 2 {
			t.Fatalf("%s: %d %+v", r, code, v.WhatIf)
		}
	}
	entries, after, err := f.s.db.AuditNewest(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries[:after-before] {
		if strings.Contains(e.Action, "what-if") {
			t.Fatalf("what-if audited: %+v", e)
		}
	}
	tok := f.tokens[store.RoleOperator] // one call made above
	for i := 2; i <= whatIfLimit; i++ {
		if code := f.do(tok, "POST", "/api/v1/capacity/what-if", `{}`, nil); code != 200 {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	req, _ := http.NewRequest("POST", f.ts.URL+"/api/v1/capacity/what-if", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var ae apiError
	if code := f.do(tok, "POST", "/api/v1/capacity/what-if", `{}`, &ae); code != http.StatusTooManyRequests || ae.Code != "too_many_requests" || ae.Reason != "rate_limited" {
		t.Fatalf("over the limit: %d %+v", code, ae)
	}
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("the 11th call: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if code := f.do(f.tokens[store.RoleAuditor], "POST", "/api/v1/capacity/what-if", `{}`, nil); code != 200 {
		t.Fatalf("another session: %d", code)
	}
}

// A queued test whose disks do not fit beside the running tests' waits for them instead of running and failing
// UNKNOWN (T29: on the lab's 4 slots, the Windows test and gaia-t23-uefi's started in the same minute and one was "not
// tested" every hour). The room is reserved when the test is sent and its restore takes that reservation, not a second
// one; a test that would not fit even alone is sent at once (it says why); a reservation no restore took is given back.
func TestImagesAdmission(t *testing.T) {
	s := &Server{cfg: Config{SandboxImagesDir: t.TempDir()}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	free, err := freeBytes(s.cfg.SandboxImagesDir)
	if err != nil {
		t.Fatal(err)
	}
	vm := int64(1)
	job := func(id int64) store.Job { return store.Job{ID: id, VMID: &vm} }
	if ok, _ := s.imagesAdmit(job(1), free/2, free, nil); !ok {
		t.Fatal("the first test did not fit an empty directory")
	}
	if ok, _ := s.imagesAdmit(job(2), free/2+free/4, free, nil); ok {
		t.Fatal("a test that does not fit beside the running one was sent")
	}
	held, waiting := s.imagesHeld()
	if held != free/2 || len(waiting) != 1 || waiting[0].JobID != 2 || waiting[0].NeedBytes != free/2+free/4 {
		t.Fatalf("held %d waiting %+v", held, waiting)
	}
	if ok, _ := s.imagesAdmit(job(3), free*3, free, nil); !ok {
		t.Fatal("a test larger than the directory even with nothing running waits instead of saying why")
	}
	// Job 1's restore takes the room reserved for it: still half the directory, not all of it.
	release, err := s.reserveImages(1, free/2)
	if err != nil {
		t.Fatal(err)
	}
	if held, _ := s.imagesHeld(); held != free/2 {
		t.Fatalf("held %d after the restore took its reservation, want %d", held, free/2)
	}
	// It waited: after imagesPassFor the dispatcher lets nothing pass it.
	s.images.mu.Lock()
	w := s.images.waiting[2]
	w.since = time.Now().Add(-imagesPassFor - time.Second)
	s.images.waiting[2] = w
	s.images.mu.Unlock()
	if ok, waited := s.imagesAdmit(job(2), free/2+free/4, free, nil); ok || waited < imagesPassFor {
		t.Fatalf("still held: ok %t waited %v", ok, waited)
	}
	release()
	if ok, _ := s.imagesAdmit(job(2), free/2+free/4, free, nil); !ok {
		t.Fatal("the waiting test was not sent once the running one ended")
	}
	if held, waiting := s.imagesHeld(); held != free/2+free/4 || len(waiting) != 0 {
		t.Fatalf("held %d waiting %+v", held, waiting)
	}
	s.imagesDrop(2) // its test ended before its restore took the room
	if held, _ := s.imagesHeld(); held != 0 {
		t.Fatalf("held %d after the drop", held)
	}
	// A wait for a test no longer queued is forgotten.
	s.imagesAdmit(job(1), free/2, free, nil)
	s.imagesAdmit(job(4), free, free, nil)
	s.imagesForget([]store.Job{job(1)})
	if _, waiting := s.imagesHeld(); len(waiting) != 0 {
		t.Fatalf("waiting %+v after job 4 left the queue", waiting)
	}
}

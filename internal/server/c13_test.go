package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
	release, err := s.reserveImages(free / 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.reserveImages(free/2 + free/4); err == nil || !strings.Contains(err.Error(), "held by running tests") {
		t.Fatalf("a second restore beyond the free room: %v", err)
	}
	release()
	r2, err := s.reserveImages(free/2 + free/4)
	if err != nil {
		t.Fatalf("after the release: %v", err)
	}
	r2()
	if _, err := s.reserveImages(free + 1<<40); err == nil {
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

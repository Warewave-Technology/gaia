package convert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pinned image must be an image ID; paths must be absolute.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.json")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	id := "sha256:" + strings.Repeat("ab", 32)
	c, err := LoadConfig(write(`{"image":"` + id + `"}`))
	if err != nil || c.Socket != "/run/gaia-convert/convert.sock" || c.VirtioWin != "/var/lib/gaia-convert/virtio-win-0.1.302.iso" || c.Timeout != "60m" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for body, want := range map[string]string{
		`{"image":"gaia-converter:latest"}`:               "the converter image's ID",
		`{"image":"` + id + `","jobs_dir":"jobs"}`:        "not an absolute path",
		`{"image":"` + id + `","timeout":"soon"}`:         "timeout",
		`{"image":"` + id + `","registry":"docker.io/x"}`: "unknown field",
	} {
		if _, err := LoadConfig(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

// A request names only what gaia server made: a job directory with in/, a .vmx directly in it, not a link; a job is
// converted once.
func TestCheckRequest(t *testing.T) {
	jobs := t.TempDir()
	s := &service{c: Config{JobsDir: jobs}}
	os.MkdirAll(filepath.Join(jobs, "mc1", "in"), 0o750)
	os.WriteFile(filepath.Join(jobs, "mc1", "in", "web.vmx"), []byte("x"), 0o640)
	os.Symlink("/etc/passwd", filepath.Join(jobs, "mc1", "in", "link.vmx"))
	if _, err := s.checkRequest(&Request{Job: "mc1", VMX: "web.vmx", Name: "web"}); err != nil {
		t.Fatalf("a good request: %v", err)
	}
	for _, r := range []Request{
		{Job: "../etc", VMX: "web.vmx", Name: "web"},
		{Job: "mc1", VMX: "../in/web.vmx", Name: "web"},
		{Job: "mc1", VMX: "web.txt", Name: "web"},
		{Job: "mc1", VMX: "web.vmx", Name: "-o /etc"},
		{Job: "mc1", VMX: "link.vmx", Name: "web"},
		{Job: "mc2", VMX: "web.vmx", Name: "web"},
	} {
		if _, err := s.checkRequest(&r); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	os.Mkdir(OutDir(jobs, "mc1"), 0o770)
	if _, err := s.checkRequest(&Request{Job: "mc1", VMX: "web.vmx", Name: "web"}); err == nil || !strings.Contains(err.Error(), "converted once") {
		t.Fatalf("a job converted twice: %v", err)
	}
}

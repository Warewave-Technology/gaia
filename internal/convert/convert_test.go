package convert

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestExcludeDriversRequest(t *testing.T) {
	jobs := t.TempDir()
	s := &service{c: Config{JobsDir: jobs}}
	os.MkdirAll(filepath.Join(jobs, "mc1", "in"), 0o750)
	os.WriteFile(filepath.Join(jobs, "mc1", "in", "web.vmx"), []byte("x"), 0o640)
	ok := Request{Job: "mc1", VMX: "web.vmx", Name: "web", Windows: true, ExcludeDrivers: []string{"smbus", "NetKVM"}}
	if _, err := s.checkRequest(&ok); err != nil {
		t.Fatalf("a good request: %v", err)
	}
	for _, ex := range [][]string{{"../smbus"}, {"smbus*"}, {"smbus", "SMBUS"}, {"a b"}, {""}, {"1", "2", "3", "4", "5", "6", "7", "8", "9"}} {
		r := ok
		r.ExcludeDrivers = ex
		if _, err := s.checkRequest(&r); err == nil {
			t.Errorf("exclude_drivers %q was accepted", ex)
		}
	}
	r := ok
	r.Windows = false
	if _, err := s.checkRequest(&r); err == nil || !strings.Contains(err.Error(), "only for a Windows guest") {
		t.Errorf("exclude_drivers for a Linux guest: %v", err)
	}
}

// TestExcludeScript runs the script the converter image runs ahead of virt-v2v, with its paths moved to a temporary
// directory and a guestfish that streams a small virtio-win tree as tar.
func TestExcludeScript(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	d := t.TempDir()
	iso, out, bin := filepath.Join(d, "iso"), filepath.Join(d, "out"), filepath.Join(d, "bin")
	for _, f := range []string{"smbus/2k22/amd64/smbus.inf", "smbus/2k22/amd64/smbus.cat", "amd64/2k22/smbus.sys",
		"amd64/2k22/viostor.inf", "viostor/2k22/amd64/viostor.inf", "NetKVM/2k22/amd64/netkvm.inf", "virtio-win_license.txt"} {
		os.MkdirAll(filepath.Join(iso, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(iso, f), []byte(f), 0o644)
	}
	// as on the ISO: read-only files and folders
	filepath.Walk(iso, func(p string, fi os.FileInfo, err error) error {
		if err == nil && p != iso {
			defer os.Chmod(p, map[bool]os.FileMode{true: 0o555, false: 0o444}[fi.IsDir()])
		}
		return err
	})
	t.Cleanup(func() {
		filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
			if err == nil {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	os.MkdirAll(out, 0o755)
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "guestfish"), []byte("#!/bin/sh\nexec tar -c -C '"+iso+"' .\n"), 0o755)
	script := strings.NewReplacer("/out/", out+"/", "/tmp/virtio-win", filepath.Join(d, "tree")).Replace(excludeScript)
	runWith := func(edit func(string) string, exclude string) (string, error) {
		exec.Command("chmod", "-R", "u+w", filepath.Join(d, "tree")).Run() // a run that failed left it read-only
		os.RemoveAll(filepath.Join(d, "tree"))
		cmd := exec.Command("bash", "-c", edit(script), "exclude", "sh", "-c", `echo "VIRTIO_WIN=$VIRTIO_WIN"; find "$VIRTIO_WIN" -type f | sed "s|^$VIRTIO_WIN/||" | sort`)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GAIA_EXCLUDE="+exclude)
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	run := func(exclude string) (string, error) { return runWith(func(sc string) string { return sc }, exclude) }
	got, err := run("smbus netkvm") // names match in any case, as Windows does
	if err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	for _, gone := range []string{"smbus.inf", "smbus.cat", "smbus.sys", "netkvm.inf"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s is still given to virt-v2v:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"amd64/2k22/viostor.inf", "viostor/2k22/amd64/viostor.inf", "virtio-win_license.txt"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s was left out too:\n%s", kept, got)
		}
	}
	paths, err := excludedPaths(filepath.Join(out, excludedList))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"NetKVM", "amd64/2k22/smbus.sys", "smbus"}; !slicesEqualSorted(paths, want) {
		t.Errorf("excluded paths %q, want %q", paths, want)
	}
	// a step before virt-v2v that fails is Gaia's, exit 66 (here: a tree left without write permission)
	if got, err := runWith(func(sc string) string { return strings.Replace(sc, `chmod -R u+w "$tree"`, "false", 1) }, "smbus"); err == nil ||
		!strings.Contains(got, "gaia: error: preparing virtio-win") {
		t.Errorf("a failed step: %v: %s", err, got)
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != exitPrepare {
		t.Errorf("a failed step exits %v, want %d", err, exitPrepare)
	}
	if got, err := run("smbus nosuchdriver"); err == nil || !strings.Contains(got, "virtio-win has no driver named nosuchdriver") {
		t.Errorf("an unknown driver: %v: %s", err, got)
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != exitNoSuchDriver {
		t.Errorf("an unknown driver exits %v, want %d", err, exitNoSuchDriver)
	}
}

func slicesEqualSorted(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func TestDecodeText(t *testing.T) {
	le := []byte{0xff, 0xfe}
	for _, r := range "pnputil: smbus.inf\r\nok\r\n" {
		le = append(le, byte(r), 0)
	}
	for in, want := range map[string]string{string(le): "pnputil: smbus.inf\nok\n", "a\r\nb\n": "a\nb\n", "\ufeffx\r\n": "x\n"} {
		if got := decodeText([]byte(in)); got != want {
			t.Errorf("decodeText(%q) = %q, want %q", in, got, want)
		}
	}
}

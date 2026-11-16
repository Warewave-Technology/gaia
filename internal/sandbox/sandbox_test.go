package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/verify"
)

func cfgIn(t *testing.T) (Config, string) {
	dir, err := os.MkdirTemp("", "sbx") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	images := filepath.Join(dir, "images")
	os.MkdirAll(filepath.Join(images, "sub"), 0o755)
	for _, f := range []string{"a.raw", "b.raw", "sub/c.raw"} {
		os.WriteFile(filepath.Join(images, f), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "outside.raw"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(dir, "outside.raw"), filepath.Join(images, "link.raw"))
	return Config{ImagesDir: images, Socket: filepath.Join(dir, "s.sock")}, images
}

func req(disks ...verify.DiskImage) Request {
	return Request{Config: verify.Config{VM: "vm", Template: "linux-boot", Machine: verify.Machine{MAC: "52:54:00:00:00:01"}}, Disks: disks}
}

func TestCheckRequest(t *testing.T) {
	c, images := cfgIn(t)
	ok := req(verify.DiskImage{Path: filepath.Join(images, "a.raw"), Format: "raw", Target: "vda"},
		verify.DiskImage{Path: filepath.Join(images, "b.raw"), Format: "raw", Target: "vdb"})
	if err := c.checkRequest(&ok); err != nil {
		t.Fatalf("a good request: %v", err)
	}
	for name, d := range map[string]verify.DiskImage{
		"outside the images dir": {Path: filepath.Join(filepath.Dir(images), "outside.raw"), Format: "raw", Target: "vda"},
		"a symlink":              {Path: filepath.Join(images, "link.raw"), Format: "raw", Target: "vda"},
		"a subdirectory":         {Path: filepath.Join(images, "sub", "c.raw"), Format: "raw", Target: "vda"},
		"dot-dot":                {Path: images + "/../images/a.raw", Format: "raw", Target: "vda"},
		"relative":               {Path: "a.raw", Format: "raw", Target: "vda"},
		"the directory":          {Path: filepath.Join(images, "sub"), Format: "raw", Target: "vda"},
		"missing":                {Path: filepath.Join(images, "none.raw"), Format: "raw", Target: "vda"},
		"bad format":             {Path: filepath.Join(images, "a.raw"), Format: "vmdk", Target: "vda"},
		"bad target":             {Path: filepath.Join(images, "a.raw"), Format: "raw", Target: "../x"},
	} {
		r := req(d)
		if err := c.checkRequest(&r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dup := req(verify.DiskImage{Path: filepath.Join(images, "a.raw"), Format: "raw", Target: "vda"},
		verify.DiskImage{Path: filepath.Join(images, "b.raw"), Format: "raw", Target: "vda"})
	if err := c.checkRequest(&dup); err == nil {
		t.Error("a repeated target was accepted")
	}
	bad := req(verify.DiskImage{Path: filepath.Join(images, "a.raw"), Format: "raw", Target: "vda"})
	bad.Config.Machine.MAC = ""
	if err := c.checkRequest(&bad); err == nil {
		t.Error("an invalid config was accepted")
	}
	if err := c.checkRequest(&Request{Config: ok.Config}); err == nil {
		t.Error("no disks was accepted")
	}
}

func TestTarRoundTripAndRefusals(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "shots"), 0o755)
	os.WriteFile(filepath.Join(src, "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
	os.WriteFile(filepath.Join(src, "shots", "boot.png"), []byte("png"), 0o644)
	var buf bytes.Buffer
	if err := writeTar(&buf, src); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := readTar(bytes.NewReader(buf.Bytes()), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "shots", "boot.png")); string(b) != "png" {
		t.Fatalf("round trip: %q", b)
	}
	for _, h := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/abs", Typeflag: tar.TypeReg},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		tw.WriteHeader(&h)
		tw.Close()
		if err := readTar(&b, t.TempDir()); err == nil {
			t.Errorf("entry %q (%c) was unpacked", h.Name, h.Typeflag)
		}
	}
}

// The client against the real handler on a unix socket: a refused request comes back as an error with the reason.
func TestClientRefusal(t *testing.T) {
	c, images := cfgIn(t)
	ln, err := net.Listen("unix", c.Socket)
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{c: c, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/verify", h.verify)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()
	r := req(verify.DiskImage{Path: filepath.Join(images, "link.raw"), Format: "raw", Target: "vda"})
	_, _, err = NewClient(c.Socket).Verify(context.Background(), r, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("refusal: %v", err)
	}
}

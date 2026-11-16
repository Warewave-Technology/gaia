package dirtar

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTarRoundTripAndRefusals(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "shots"), 0o755)
	os.WriteFile(filepath.Join(src, "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
	os.WriteFile(filepath.Join(src, "shots", "boot.png"), []byte("png"), 0o644)
	var buf bytes.Buffer
	if err := Write(&buf, src); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Read(bytes.NewReader(buf.Bytes()), dst); err != nil {
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
		if err := Read(&b, t.TempDir()); err == nil {
			t.Errorf("entry %q (%c) was unpacked", h.Name, h.Typeflag)
		}
	}
}

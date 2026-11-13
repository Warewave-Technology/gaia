package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"512": 512, "4KiB": 4096, "64KiB": 65536, "2MiB": 2 << 20, "1GiB": 1 << 30}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-1", "abc", "1TiB"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) succeeded, want error", bad)
		}
	}
}

// TestRoundTrip runs the real binary: gen -> copy -> cmp (identical)
// -> mutate -> cmp (must differ) -> hashes differ.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "imgtool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(args ...string) error {
		return exec.Command(bin, args...).Run()
	}
	img := filepath.Join(dir, "a.img")
	cp := filepath.Join(dir, "b.img")

	if err := run("gen", "-out", img, "-kind", "logs", "-size", "4MiB"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(img)
	if err != nil || len(data) != 4<<20 {
		t.Fatalf("gen produced %d bytes, err %v", len(data), err)
	}
	if err := os.WriteFile(cp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run("cmp", "-a", img, "-b", cp); err != nil {
		t.Fatalf("identical files reported as different: %v", err)
	}
	if err := run("mutate", "-file", cp, "-percent", "5", "-block", "4KiB"); err != nil {
		t.Fatal(err)
	}
	if err := run("cmp", "-a", img, "-b", cp); err == nil {
		t.Fatal("mutated file reported as identical")
	}
	ha, _ := hashFile(img)
	hb, _ := hashFile(cp)
	if ha.SHA256 == hb.SHA256 {
		t.Fatal("hashes equal after mutation")
	}
	if hb.Bytes != 4<<20 {
		t.Fatalf("mutate changed file size to %d", hb.Bytes)
	}
}

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

// TestMutateDeterministic: the same seed must change the same bytes the same way,
// or a measurement cannot be reproduced (CLAUDE.md rule 5).
func TestMutateDeterministic(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "imgtool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	zeros := make([]byte, 4<<20)
	var sums []string
	for _, name := range []string{"a.img", "b.img"} {
		img := filepath.Join(dir, name)
		if err := os.WriteFile(img, zeros, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "mutate", "-file", img, "-percent", "10", "-block", "4KiB", "-seed", "7").CombinedOutput(); err != nil {
			t.Fatalf("mutate: %v\n%s", err, out)
		}
		h, err := hashFile(img)
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, h.SHA256)
	}
	if sums[0] != sums[1] {
		t.Fatalf("same seed, different result: %s vs %s", sums[0], sums[1])
	}
}

func TestWriteRef(t *testing.T) {
	dir := t.TempDir()
	r, err := writeRef(dir, 10<<20+123)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bytes != 10<<20+123 || r.Seconds <= 0 || r.FsyncS < 0 {
		t.Fatalf("result %+v", r)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("the reference file was left behind: %v", left)
	}
}

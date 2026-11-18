package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// arMembers splits a .deb (an ar archive) into its members, in order.
func arMembers(t *testing.T, b []byte) ([]string, map[string][]byte) {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("!<arch>\n")) {
		t.Fatal("not an ar archive")
	}
	b = b[8:]
	var names []string
	data := map[string][]byte{}
	for len(b) > 0 {
		if len(b) < 60 || string(b[58:60]) != "`\n" {
			t.Fatalf("bad ar header %q", b[:min(len(b), 60)])
		}
		name := strings.TrimSpace(string(b[:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(b[48:58])))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		data[name] = b[60 : 60+size]
		b = b[60+size:]
		if size%2 == 1 {
			b = b[1:]
		}
	}
	return names, data
}

func tarList(t *testing.T, gz []byte) map[string]*tar.Header {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
}

// The .deb has dpkg's layout (debian-binary, control.tar.gz, data.tar.gz), the computed Installed-Size and md5sums,
// the maintainer scripts executable, files owned by root at the given time, and the same bytes when built twice.
func TestDeb(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755)
	os.MkdirAll(filepath.Join(root, "usr/share/doc/x"), 0o755)
	os.WriteFile(filepath.Join(root, "usr/bin/x"), bytes.Repeat([]byte("x"), 3000), 0o700)
	os.WriteFile(filepath.Join(root, "usr/share/doc/x/README"), []byte("hello\n"), 0o600)
	scripts := filepath.Join(dir, "scripts")
	os.MkdirAll(scripts, 0o755)
	os.WriteFile(filepath.Join(scripts, "postinst"), []byte("#!/bin/sh\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "control"), []byte("Package: x\nVersion: 1.0\nArchitecture: amd64\n"), 0o644)
	mtime := time.Unix(1_800_000_000, 0).UTC()
	var debs [][]byte
	for i := range 2 {
		out := filepath.Join(dir, strconv.Itoa(i)+".deb")
		if err := buildDeb(root, filepath.Join(dir, "control"), scripts, mtime, out); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(out)
		debs = append(debs, b)
	}
	if !bytes.Equal(debs[0], debs[1]) {
		t.Fatal("two builds differ")
	}
	names, members := arMembers(t, debs[0])
	if strings.Join(names, " ") != "debian-binary control.tar.gz data.tar.gz" || string(members["debian-binary"]) != "2.0\n" {
		t.Fatalf("members %v", names)
	}
	ctl := tarList(t, members["control.tar.gz"])
	if h := ctl["./postinst"]; h == nil || h.Mode != 0o755 {
		t.Fatalf("postinst %+v", h)
	}
	data := tarList(t, members["data.tar.gz"])
	for name, mode := range map[string]int64{"./": 0o755, "./usr/bin/": 0o755, "./usr/bin/x": 0o755, "./usr/share/doc/x/README": 0o644} {
		h := data[name]
		if h == nil || h.Mode != mode || h.Uname != "root" || h.Uid != 0 || !h.ModTime.Equal(mtime) {
			t.Errorf("%s: %+v", name, h)
		}
	}
	zr, _ := gzip.NewReader(bytes.NewReader(members["control.tar.gz"]))
	tr := tar.NewReader(zr)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	// 3000 bytes = 3 KiB, 6 bytes = 1 KiB, and 1 KiB for each of the five directories
	if !strings.Contains(files["./control"], "Installed-Size: 9\n") {
		t.Errorf("control %q", files["./control"])
	}
	if !strings.Contains(files["./md5sums"], "  usr/bin/x\n") || !strings.Contains(files["./md5sums"], "  usr/share/doc/x/README\n") {
		t.Errorf("md5sums %q", files["./md5sums"])
	}
}

// A tgz holds the files under the prefix directory.
func TestTgz(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gaia"), []byte("bin"), 0o755)
	out := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := buildTgz(dir, "gaia-1.0-linux-amd64", time.Unix(1_800_000_000, 0), out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	l := tarList(t, b)
	if h := l["gaia-1.0-linux-amd64/gaia"]; h == nil || h.Mode != 0o755 || len(l) != 2 || l["gaia-1.0-linux-amd64/"] == nil {
		t.Fatalf("%v", l)
	}
}

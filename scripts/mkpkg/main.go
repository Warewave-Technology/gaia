// Command mkpkg writes the release's packages with the standard library only, so the release (scripts/release.sh)
// builds on any machine with Go: a Debian binary package (.deb) and a tar.gz. Both are reproducible: every file is
// owned by root, has the time -mtime, and the archives list files in name order, so the same inputs give the same
// bytes.
//
//	mkpkg deb -root DIR -control FILE [-scripts DIR] -mtime UNIX -o OUT.deb
//	mkpkg tgz -root DIR -prefix NAME -mtime UNIX -o OUT.tar.gz
//
// DIR holds the files as they are installed (for a deb, DIR/usr/bin/gaia becomes /usr/bin/gaia), with their modes:
// a file with any execute bit is 0755, every other file 0644. FILE is the control file without Installed-Size, which
// mkpkg adds. -scripts holds the maintainer scripts (preinst, postinst, prerm, postrm) to include. mkpkg adds
// md5sums, as dpkg-deb packages have. A tgz holds DIR's files under the directory NAME.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "deb" && os.Args[1] != "tgz") {
		fmt.Fprintln(os.Stderr, "usage: mkpkg deb|tgz -h")
		os.Exit(3)
	}
	fl := flag.NewFlagSet("mkpkg "+os.Args[1], flag.ExitOnError)
	root := fl.String("root", "", "the files as installed")
	control := fl.String("control", "", "deb: the control file (no Installed-Size)")
	scripts := fl.String("scripts", "", "deb: directory of maintainer scripts (optional)")
	prefix := fl.String("prefix", "", "tgz: the directory the files are in")
	mtime := fl.Int64("mtime", 0, "modification time of every file (Unix seconds; the commit's time)")
	out := fl.String("o", "", "the package to write")
	fl.Parse(os.Args[2:])
	if *root == "" || *out == "" || *mtime <= 0 {
		fl.Usage()
		os.Exit(3)
	}
	t := time.Unix(*mtime, 0).UTC()
	var err error
	switch os.Args[1] {
	case "deb":
		if *control == "" {
			fl.Usage()
			os.Exit(3)
		}
		err = buildDeb(*root, *control, *scripts, t, *out)
	case "tgz":
		if *prefix == "" || strings.Contains(*prefix, "/") {
			fl.Usage()
			os.Exit(3)
		}
		err = buildTgz(*root, *prefix, t, *out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkpkg:", err)
		os.Exit(3)
	}
}

func buildTgz(root, prefix string, mtime time.Time, out string) error {
	files, err := walk(root)
	if err != nil {
		return err
	}
	for i := range files {
		files[i].name = prefix + "/" + files[i].name
	}
	files = append([]entry{{name: prefix + "/", mode: 0o755}}, files...)
	b, err := tarGz(files, mtime, false)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

// entry is one file or directory of an archive.
type entry struct {
	name string // without a leading "./"
	mode fs.FileMode
	data []byte // nil for a directory
}

func buildDeb(root, controlPath, scripts string, mtime time.Time, out string) error {
	files, err := walk(root)
	if err != nil {
		return err
	}
	var size int64
	var sums strings.Builder
	for _, e := range files {
		if e.data == nil {
			size++ // dpkg-deb counts a directory as 1 KiB, a file as its size in KiB rounded up
			continue
		}
		size += (int64(len(e.data)) + 1023) / 1024
		h := md5.Sum(e.data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), e.name)
	}
	ctl, err := os.ReadFile(controlPath)
	if err != nil {
		return err
	}
	if bytes.Contains(ctl, []byte("Installed-Size:")) {
		return fmt.Errorf("%s: Installed-Size is computed, leave it out", controlPath)
	}
	ctl = append(bytes.TrimRight(ctl, "\n"), fmt.Sprintf("\nInstalled-Size: %d\n", size)...)
	meta := []entry{{name: "control", mode: 0o644, data: ctl}, {name: "md5sums", mode: 0o644, data: []byte(sums.String())}}
	if scripts != "" {
		for _, n := range []string{"preinst", "postinst", "prerm", "postrm"} {
			b, err := os.ReadFile(filepath.Join(scripts, n))
			if os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			meta = append(meta, entry{name: n, mode: 0o755, data: b})
		}
	}
	slices.SortFunc(meta, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	controlTar, err := tarGz(meta, mtime, true)
	if err != nil {
		return err
	}
	dataTar, err := tarGz(files, mtime, true)
	if err != nil {
		return err
	}
	var deb bytes.Buffer
	deb.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", controlTar}, {"data.tar.gz", dataTar}} {
		// ar member header: name/16, mtime/12, uid/6, gid/6, mode/8 (octal), size/10, "`\n"
		fmt.Fprintf(&deb, "%-16s%-12d%-6d%-6d%-8o%-10d`\n", m.name, mtime.Unix(), 0, 0, 0o100644, len(m.data))
		deb.Write(m.data)
		if len(m.data)%2 == 1 {
			deb.WriteByte('\n')
		}
	}
	return os.WriteFile(out, deb.Bytes(), 0o644)
}

// walk reads the tree under root in name order: directories (other than root itself) and regular files only.
func walk(root string) ([]entry, error) {
	var es []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			es = append(es, entry{name: name + "/", mode: 0o755})
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			mode := fs.FileMode(0o644)
			if info.Mode()&0o111 != 0 {
				mode = 0o755
			}
			es = append(es, entry{name: name, mode: mode, data: b})
		default:
			return fmt.Errorf("%s: only directories and regular files go into the package", p)
		}
		return nil
	})
	return es, err // WalkDir visits in lexical order
}

// tarGz is a gzipped tar of es, owned by root, with time mtime; for a deb every name starts with "./" (as dpkg-deb
// writes them) after the entry "./" itself.
func tarGz(es []entry, mtime time.Time, deb bool) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.ModTime = mtime
	tw := tar.NewWriter(zw)
	hdr := func(name string, typ byte, mode fs.FileMode, size int) *tar.Header {
		return &tar.Header{Name: name, Typeflag: typ, Mode: int64(mode), Size: int64(size), ModTime: mtime, Uname: "root", Gname: "root",
			Format: tar.FormatGNU}
	}
	pre := ""
	if deb {
		pre = "./"
		if err := tw.WriteHeader(hdr("./", tar.TypeDir, 0o755, 0)); err != nil {
			return nil, err
		}
	}
	for _, e := range es {
		if e.data == nil {
			if err := tw.WriteHeader(hdr(pre+e.name, tar.TypeDir, e.mode, 0)); err != nil {
				return nil, err
			}
			continue
		}
		if err := tw.WriteHeader(hdr(pre+e.name, tar.TypeReg, e.mode, len(e.data))); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

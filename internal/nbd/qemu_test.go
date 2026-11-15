package nbd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These tests run the client against qemu-nbd, the server libvirt's pull-mode backup uses (through qemu).
// Without qemu-utils they skip, unless GAIA_REQUIRE_QEMU is set (CI sets it), then they fail.
func requireQemu(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"qemu-nbd", "qemu-img", "qemu-io"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("GAIA_REQUIRE_QEMU") != "" {
				t.Fatalf("%s not found and GAIA_REQUIRE_QEMU is set", bin)
			}
			t.Skipf("%s not found (install qemu-utils)", bin)
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// qemuNBD serves img read-only over a unix socket and returns the socket path.
func qemuNBD(t *testing.T, img string, args ...string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "nbd.sock")
	cmd := exec.Command("qemu-nbd", append([]string{"--read-only", "--persistent", "--export-name=disk",
		"--format=qcow2", "--socket=" + sock}, append(args, img)...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		if time.Now().After(deadline) {
			t.Fatalf("qemu-nbd did not create its socket: %s", stderr.String())
		}
	}
}

const (
	kiB = 1 << 10
	miB = 1 << 20
)

// A 64 MiB qcow2 with data written before and after a persistent dirty bitmap was added.
func testImage(t *testing.T) (img string, raw []byte) {
	t.Helper()
	dir := t.TempDir()
	img = filepath.Join(dir, "disk.qcow2")
	run(t, "qemu-img", "create", "-q", "-f", "qcow2", img, "64M")
	run(t, "qemu-io", "-f", "qcow2", "-c", "write -q -P 0x11 0 1M", "-c", "write -q -P 0x22 8M 512k", img)
	run(t, "qemu-img", "bitmap", "--add", img, "b0")
	run(t, "qemu-io", "-f", "qcow2", "-c", "write -q -P 0x33 16M 128k", "-c", "write -q -P 0x44 8M 64k", img)
	ref := filepath.Join(dir, "ref.raw")
	run(t, "qemu-img", "convert", "-f", "qcow2", "-O", "raw", img, ref)
	raw, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	return img, raw
}

func TestQemuReadAndExtents(t *testing.T) {
	requireQemu(t)
	img, raw := testImage(t)
	sock := qemuNBD(t, img, "--bitmap=b0")
	bitmap := DirtyBitmapPrefix + "b0"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, "unix", sock, Options{Export: "disk", MetaContexts: []string{ContextBaseAllocation, bitmap}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.Size() != 64*miB || !c.ReadOnly() {
		t.Fatalf("size %d, read-only %v", c.Size(), c.ReadOnly())
	}
	got := make([]byte, c.Size())
	if _, err := c.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("NBD read differs from qemu-img convert")
	}
	part := make([]byte, 100*kiB) // unaligned, across the 8M+64k boundary between 0x44 and 0x22
	if _, err := c.ReadAt(part, 8*miB+10*kiB); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(part, raw[8*miB+10*kiB:8*miB+110*kiB]) {
		t.Fatal("unaligned read differs")
	}

	dirty, err := c.Extents(bitmap, 0, c.Size())
	if err != nil {
		t.Fatal(err)
	}
	wantDirty := []Extent{
		{0, 8 * miB, 0},
		{8 * miB, 64 * kiB, StateDirty},
		{8*miB + 64*kiB, 8*miB - 64*kiB, 0},
		{16 * miB, 128 * kiB, StateDirty},
		{16*miB + 128*kiB, 48*miB - 128*kiB, 0},
	}
	if !equalExtents(dirty, wantDirty) {
		t.Fatalf("dirty extents\n got %v\nwant %v", dirty, wantDirty)
	}

	alloc, err := c.Extents(ContextBaseAllocation, 0, c.Size())
	if err != nil {
		t.Fatal(err)
	}
	hz := StateHole | StateZero
	wantAlloc := []Extent{
		{0, 1 * miB, 0},
		{1 * miB, 7 * miB, hz},
		{8 * miB, 512 * kiB, 0},
		{8*miB + 512*kiB, 8*miB - 512*kiB, hz},
		{16 * miB, 128 * kiB, 0},
		{16*miB + 128*kiB, 48*miB - 128*kiB, hz},
	}
	if !equalExtents(alloc, wantAlloc) {
		t.Fatalf("allocation extents\n got %v\nwant %v", alloc, wantAlloc)
	}
}

func TestQemuMissingBitmapFails(t *testing.T) {
	requireQemu(t)
	img, _ := testImage(t)
	sock := qemuNBD(t, img) // no --bitmap: the context does not exist
	_, err := Dial(context.Background(), "unix", sock, Options{Export: "disk",
		MetaContexts: []string{DirtyBitmapPrefix + "b0"}})
	if err == nil {
		t.Fatal("handshake succeeded without the dirty bitmap")
	}
}

func TestQemuWrongExportFails(t *testing.T) {
	requireQemu(t)
	img, _ := testImage(t)
	sock := qemuNBD(t, img)
	if _, err := Dial(context.Background(), "unix", sock, Options{Export: "nope"}); err == nil {
		t.Fatal("handshake succeeded for an unknown export")
	}
}

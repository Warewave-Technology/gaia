// Package sandbox is the privileged half of Gaia's restore tests (ADR-007 §5).
//
// Booting a restored VM in isolation needs root: a network namespace, a veth pair on the sandbox bridge, dnsmasq and
// the probes inside that namespace (package verify). gaia server runs without any capability, so a small helper,
// `gaia sandbox serve`, runs as root in its own systemd unit and does only this: it boots disk images that gaia server
// restored into one directory, runs the declared checks, and returns the evidence bundle unsigned. gaia server signs
// it with its own key, so the helper never holds a key and the server never holds root.
//
// The helper listens on a unix socket that only root and the gaia group can open (0660 in a 0750 directory). It
// serves over HTTP: POST /v1/verify with a Request, answered with a tar of the bundle once the test ran in a slot
// (slots.go: up to Slots at once, admitted by memory); GET /v1/capacity says what it can run and runs now.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/dirtar"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// Request asks the helper to boot restored disks and check them.
type Request struct {
	Config   verify.Config      `json:"config"`    // as declared (template, machine, checks); the helper resolves it
	Disks    []verify.DiskImage `json:"disks"`     // regular files directly in the helper's images directory, boot disk first
	RestoreS float64            `json:"restore_s"` // how long gaia server took to restore them (Proven RTO = restore + ready)
	// The migration check (ADR-003): the steps gaia server ran before (source, download, conversion), recorded ahead of
	// the guest's own checks; and, with no disks, why the guest could not be started (the bundle then records only that).
	PreChecks  []verify.CheckResult `json:"pre_checks,omitempty"`
	SkipReason string               `json:"skip_reason,omitempty"`
	// KeepDisks: when the guest's own checks do not all pass, what it wrote to its disks is kept in the images
	// directory (Result.KeptDisks) for gaia server to read and remove: a migration check reads a converted Windows'
	// first-boot log there (ADR-005 C, driver_install_loop).
	KeepDisks bool `json:"keep_disks,omitempty"`
	// FirstBoot: the guest boots for the first time (a converted VM): restarts before its stability window are
	// expected (ADR-005 F); a restore test leaves it false.
	FirstBoot bool `json:"first_boot,omitempty"`
}

// Config is the helper's own configuration.
type Config struct {
	Socket        string // e.g. /run/gaia-sandbox/sandbox.sock
	Group         string // the group that may use the socket (gaia server's)
	ImagesDir     string // gaia server restores the disks here; the helper reads nothing else
	WorkDir       string // overlays (qemu must reach it), e.g. /var/lib/libvirt/images/gaia-sandbox
	SerialDir     string // serial logs, e.g. /var/log/libvirt/qemu
	BundleDir     string // unsigned bundles while they are sent, e.g. /var/lib/gaia-sandbox/bundles
	LibvirtSocket string // /run/libvirt/libvirt-sock
	Self          string // this binary, for `gaia probe` inside the sandbox network
	// Parallel tests (ADR-007 C4, slots.go): 0 takes the default from the host's memory and CPUs.
	Slots     int // at most verify.MaxSlots
	MemoryMiB int // the guests' memory limit, overhead included
}

var targetRE = regexp.MustCompile(`^(vd|sd|hd)[a-z]$`)

// checkRequest validates a request against the helper's configuration. Paths must be plain files directly in
// ImagesDir: no symlinks, no subdirectories, no other place on the host.
func (c Config) checkRequest(r *Request) error {
	switch {
	case len(r.Disks) == 0 && r.SkipReason == "":
		return errors.New("no disks")
	case len(r.Disks) > 0 && r.SkipReason != "":
		return errors.New("disks and a skip reason")
	case len(r.SkipReason) > 1024 || len(r.PreChecks) > 32:
		return errors.New("skip reason or pre-checks too long")
	}
	for i, k := range r.PreChecks {
		if k.Name == "" || len(k.Name) > 64 || len(k.Type) > 64 || len(k.Detail) > 8192 || verify.Worst(k.Verdict) != k.Verdict {
			return fmt.Errorf("pre-check %d: name, type, detail or verdict %q not allowed", i, k.Verdict)
		}
	}
	seen := map[string]bool{}
	for i, d := range r.Disks {
		if !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path || filepath.Dir(d.Path) != filepath.Clean(c.ImagesDir) {
			return fmt.Errorf("disk %d: %q is not a file directly in %s", i, d.Path, c.ImagesDir)
		}
		fi, err := os.Lstat(d.Path)
		if err != nil {
			return fmt.Errorf("disk %d: %w", i, err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("disk %d: %s is not a regular file", i, d.Path)
		}
		if d.Format != "raw" && d.Format != "qcow2" {
			return fmt.Errorf("disk %d: format %q (raw or qcow2)", i, d.Format)
		}
		if !targetRE.MatchString(d.Target) || seen[d.Target] {
			return fmt.Errorf("disk %d: target %q missing, invalid or repeated", i, d.Target)
		}
		seen[d.Target] = true
	}
	if r.RestoreS < 0 {
		return errors.New("restore_s < 0")
	}
	return r.Config.Validate()
}

// Serve runs the helper until ctx ends.
func Serve(ctx context.Context, c Config, log *slog.Logger) error {
	for _, d := range []string{c.ImagesDir, c.WorkDir, c.BundleDir} {
		if !filepath.IsAbs(d) {
			return fmt.Errorf("%q: not an absolute path", d)
		}
	}
	if err := os.MkdirAll(c.WorkDir, 0o755); err != nil {
		return err
	}
	g, err := user.LookupGroup(c.Group)
	if err != nil {
		return fmt.Errorf("group %s: %w", c.Group, err)
	}
	gid, _ := strconv.Atoi(g.Gid)
	// The images directory: gaia server writes it (group), qemu reads the files in it through libvirt (others may
	// only traverse it), nobody else lists it.
	parent := filepath.Dir(c.ImagesDir)
	if err := os.MkdirAll(parent, 0o711); err != nil {
		return err
	}
	if fi, err := os.Stat(parent); err != nil {
		return err
	} else if fi.Mode().Perm()&0o001 == 0 {
		return fmt.Errorf("%s is %v: gaia server and qemu must be able to pass through it to %s (chmod o+x)", parent, fi.Mode().Perm(), c.ImagesDir)
	}
	if err := os.MkdirAll(c.ImagesDir, 0o771); err != nil {
		return err
	}
	if err := os.Chown(c.ImagesDir, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(c.ImagesDir, 0o771); err != nil {
		return err
	}
	if err := os.MkdirAll(c.BundleDir, 0o700); err != nil { // after the images directory: it may share its parent
		return err
	}
	dir := filepath.Dir(c.Socket)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return err
	}
	if err := os.Remove(c.Socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", c.Socket)
	if err != nil {
		return err
	}
	if err := os.Chown(c.Socket, 0, gid); err != nil {
		ln.Close()
		return err
	}
	if err := os.Chmod(c.Socket, 0o660); err != nil {
		ln.Close()
		return err
	}
	n, limit, basis, err := defaultSlots(c.Slots, c.MemoryMiB)
	if err != nil {
		ln.Close()
		return err
	}
	h := &handler{c: c, log: log, slots: newSlots(n, limit, basis)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/verify", h.verify)
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /v1/capacity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h.slots.capacity())
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Info("sandbox helper listening", "socket", c.Socket, "group", c.Group, "images", c.ImagesDir, "slots", n, "memory_limit_mib", limit, "basis", basis)
	// Only now does systemd start gaia.service (After=, Type=notify): its ReadWritePaths needs the images directory
	// to exist when its namespace is made, or the directory stays read-only for the server.
	if err := notifyReady(); err != nil {
		log.Warn("telling systemd the helper is ready", "error", err)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type handler struct {
	c     Config
	log   *slog.Logger
	slots *slots
}

func (h *handler) verify(w http.ResponseWriter, r *http.Request) {
	var req Request
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.c.checkRequest(&req); err != nil {
		h.log.Warn("request refused", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slot := 0
	if len(req.Disks) > 0 { // a request without disks boots no guest and needs no slot
		mem := req.Config.Machine.MemoryMiB
		if mem <= 0 {
			mem = verify.DefaultMemoryMiB
		}
		t0 := time.Now()
		var err error
		if slot, err = h.slots.acquire(r.Context(), req.Config.VM, mem); err != nil {
			h.log.Warn("not admitted", "vm", req.Config.VM, "memory_mib", mem, "error", err)
			http.Error(w, "admission: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer h.slots.release(slot)
		h.log.Info("admitted", "vm", req.Config.VM, "slot", slot, "memory_mib", mem, "waited_s", time.Since(t0).Seconds())
		w.Header().Set("X-Gaia-Slot", strconv.Itoa(slot))
	}
	var progress bytes.Buffer
	h.log.Info("verifying", "vm", req.Config.VM, "disks", len(req.Disks), "slot", slot)
	keep := ""
	if req.KeepDisks {
		keep = h.c.ImagesDir
	}
	res, err := verify.Run(r.Context(), verify.Options{Config: &req.Config, Disks: req.Disks, RestoreS: req.RestoreS,
		PreChecks: req.PreChecks, SkipReason: req.SkipReason, KeepDisks: keep, FirstBoot: req.FirstBoot,
		WorkDir: h.c.WorkDir, SerialDir: h.c.SerialDir, EvidenceDir: h.c.BundleDir, Socket: h.c.LibvirtSocket,
		Self: h.c.Self, Log: &progress, Slot: slot})
	if res != nil && res.BundleDir != "" {
		defer os.RemoveAll(res.BundleDir)
	}
	if err != nil {
		h.log.Error("verify", "vm", req.Config.VM, "error", err)
		http.Error(w, "verify: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.log.Info("verified", "vm", req.Config.VM, "bundle", res.BundleID, "verdict", res.Verdict)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("X-Gaia-Bundle-ID", res.BundleID)
	if err := dirtar.Write(w, res.BundleDir); err != nil {
		h.log.Error("sending the bundle", "bundle", res.BundleID, "error", err)
	}
}

// Client talks to the helper.
type Client struct {
	Socket string
	http   *http.Client
}

// ErrUnavailable marks a request the sandbox helper never received: its socket did not answer (the helper is stopped).
var ErrUnavailable = errors.New("the sandbox helper does not answer")

// unavailable is an error that is ErrUnavailable and keeps its own message.
type unavailable struct{ error }

func (u unavailable) Is(target error) bool { return target == ErrUnavailable }
func (u unavailable) Unwrap() error        { return u.error }

// NewClient returns a client for the helper at socket.
func NewClient(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}}}
}

// Capacity asks the helper what it can run and runs now.
func (c *Client) Capacity(ctx context.Context) (Capacity, error) {
	var out Capacity
	hr, err := http.NewRequestWithContext(ctx, "GET", "http://sandbox/v1/capacity", nil)
	if err != nil {
		return out, err
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		err = fmt.Errorf("sandbox helper at %s: %w", c.Socket, err)
		if op := (*net.OpError)(nil); errors.As(err, &op) && op.Op == "dial" {
			err = unavailable{err}
		}
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("sandbox helper: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return out, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

// Verify sends a request and unpacks the returned bundle into dest/<bundle id>. It returns that directory and the
// bundle's result.json. The bundle is not sealed yet.
func (c *Client) Verify(ctx context.Context, req Request, dest string) (string, *verify.Result, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", "http://sandbox/v1/verify", bytes.NewReader(b))
	if err != nil {
		return "", nil, err
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		err = fmt.Errorf("sandbox helper at %s: %w", c.Socket, err)
		if op := (*net.OpError)(nil); errors.As(err, &op) && op.Op == "dial" {
			err = unavailable{err}
		}
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", nil, fmt.Errorf("sandbox helper: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	id := resp.Header.Get("X-Gaia-Bundle-ID")
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", nil, fmt.Errorf("sandbox helper: bad bundle id %q", id)
	}
	dir := filepath.Join(dest, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", nil, err
	}
	if err := dirtar.Read(resp.Body, dir); err != nil {
		return dir, nil, fmt.Errorf("sandbox helper: bundle %s: %w", id, err)
	}
	rb, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return dir, nil, fmt.Errorf("sandbox helper: bundle %s has no result.json: %w", id, err)
	}
	var res verify.Result
	if err := json.Unmarshal(rb, &res); err != nil {
		return dir, nil, fmt.Errorf("sandbox helper: bundle %s: result.json: %w", id, err)
	}
	return dir, &res, nil
}

// notifyReady sends READY=1 to systemd's notification socket ($NOTIFY_SOCKET), if there is one (sd_notify(3)).
func notifyReady() error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if path[0] == '@' {
		path = "\x00" + path[1:] // an abstract socket
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("READY=1"))
	return err
}

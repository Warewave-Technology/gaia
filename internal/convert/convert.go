// Package convert is Gaia's converter service (ADR-005 §1): `gaia convert serve` runs as its own unprivileged user
// (gaia-convert), never as gaia server and never as root (ADR-007 B1), and converts a VM the migration check
// downloaded with virt-v2v inside the pinned converter image (packaging/converter/Containerfile): rootless podman,
// no network, no capabilities, /dev/kvm for libguestfs.
//
// gaia server reaches it on one unix socket (group gaia-convert, which only gaia server's user shares). A job is a
// directory in JobsDir that gaia server made: JOB/in holds the source VM's downloaded files (gaia server writes
// them), JOB/out receives virt-v2v's disks, domain XML and log (the converter writes them). The service reads
// nothing else of gaia server's: no keys, no database.
package convert

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// virtio-win, pinned (ADR-005 §2): the version Gaia was tested with, its sha256, and where the operator gets it.
// Gaia does not redistribute it.
const (
	VirtioWinVersion = "0.1.302"
	VirtioWinSHA256  = "303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d"
	VirtioWinURL     = "https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-0.1.302-1/virtio-win-0.1.302.iso"
)

// Config is the service's configuration (/etc/gaia/convert.json).
type Config struct {
	Socket    string `json:"socket"`     // default /run/gaia-convert/convert.sock
	Group     string `json:"group"`      // the group that may use the socket; default gaia-convert
	JobsDir   string `json:"jobs_dir"`   // default /var/lib/gaia-convert/jobs
	Image     string `json:"image"`      // the converter image's ID (sha256:...), as `podman image inspect` prints it
	VirtioWin string `json:"virtio_win"` // the operator's virtio-win ISO; default /var/lib/gaia-convert/virtio-win-<version>.iso
	Podman    string `json:"podman"`     // default podman
	Timeout   string `json:"timeout"`    // Go duration per conversion; default 60m
}

// LoadConfig reads the configuration and applies defaults; unknown fields are an error.
func LoadConfig(path string) (Config, error) {
	var c Config
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	def(&c.Socket, "/run/gaia-convert/convert.sock")
	def(&c.Group, "gaia-convert")
	def(&c.JobsDir, "/var/lib/gaia-convert/jobs")
	def(&c.VirtioWin, "/var/lib/gaia-convert/virtio-win-"+VirtioWinVersion+".iso")
	def(&c.Podman, "podman")
	def(&c.Timeout, "60m")
	if _, err := time.ParseDuration(c.Timeout); err != nil {
		return c, fmt.Errorf("timeout: %w", err)
	}
	if !imageRE.MatchString(c.Image) {
		return c, fmt.Errorf("image %q: the converter image's ID, sha256:<64 hex> (podman image inspect gaia-converter --format {{.Id}})", c.Image)
	}
	for _, p := range []string{c.Socket, c.JobsDir, c.VirtioWin} {
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("%q: not an absolute path", p)
		}
	}
	return c, nil
}

var (
	imageRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	jobRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// VirtioWin is the state of the operator's virtio-win ISO.
type VirtioWin struct {
	Path    string `json:"path"`
	Version string `json:"version"` // the pinned version
	State   string `json:"state"`   // ok, missing, mismatch
	SHA256  string `json:"sha256,omitempty"`
	Want    string `json:"want_sha256"`
	URL     string `json:"url"`
	Detail  string `json:"detail"`
}

// Info is what the service says about itself (GET /v1/info).
type Info struct {
	Image        string    `json:"image"`         // the pinned image ID
	ImagePresent bool      `json:"image_present"` // podman has exactly that image
	ImageDetail  string    `json:"image_detail"`
	Versions     string    `json:"versions"` // virt-v2v, libguestfs, mingw32-srvany, qemu, kernel in the image
	VirtioWin    VirtioWin `json:"virtio_win"`
}

// Request asks for one conversion.
type Request struct {
	Job  string `json:"job"`  // a directory directly in JobsDir, made by gaia server
	VMX  string `json:"vmx"`  // the source VM's .vmx, a file directly in JOB/in
	Name string `json:"name"` // the converted guest's name (virt-v2v -on)
	// Windows says the source is a Windows guest (from its guest OS): it needs virtio-win, and without a checked ISO
	// it is not converted at all (ADR-005 §2: UNKNOWN, never a guest without drivers).
	Windows bool `json:"windows"`
}

// Result is the outcome of one conversion. The files are in JOB/out.
type Result struct {
	State     string     `json:"state"`  // converted, failed (virt-v2v refused the guest), not_run (it could not run)
	Detail    string     `json:"detail"` // virt-v2v's own error line, or why it did not run
	Seconds   float64    `json:"seconds"`
	Image     string     `json:"image"`
	Versions  string     `json:"versions"`
	VirtioWin *VirtioWin `json:"virtio_win,omitempty"` // when it was given to virt-v2v
	Disks     []string   `json:"disks"`                // file names in JOB/out, boot disk first (NAME-sda, ...)
	DomainXML string     `json:"domain_xml"`           // file name in JOB/out
	Log       string     `json:"log"`                  // file name in JOB/out
	Command   []string   `json:"command"`              // the podman command, as run
}

// Serve runs the service until ctx ends.
func Serve(ctx context.Context, c Config, log *slog.Logger) error {
	// podman's temporary files ($TMPDIR, inside the service's own directory: the rest of the system is read-only)
	for _, d := range []string{c.JobsDir, os.TempDir()} {
		if err := os.MkdirAll(d, 0o2770); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.Socket), 0o750); err != nil {
		return err
	}
	os.Remove(c.Socket)
	l, err := net.Listen("unix", c.Socket)
	if err != nil {
		return err
	}
	defer l.Close()
	g, err := user.LookupGroup(c.Group)
	if err != nil {
		return fmt.Errorf("group %s: %w", c.Group, err)
	}
	gid, _ := strconv.Atoi(g.Gid)
	if err := os.Chown(c.Socket, -1, gid); err != nil {
		return err
	}
	if err := os.Chmod(c.Socket, 0o660); err != nil {
		return err
	}
	s := &service{c: c, log: log}
	info := s.info(ctx)
	log.Info("converter service ready", "socket", c.Socket, "image", c.Image, "image_present", info.ImagePresent, "virtio_win", info.VirtioWin.State)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.info(r.Context())) })
	mux.HandleFunc("POST /v1/convert", s.convert)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	notifyReady()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type service struct {
	c   Config
	log *slog.Logger
	mu  sync.Mutex // one conversion at a time

	vwMu   sync.Mutex
	vwStat os.FileInfo // the ISO as last hashed
	vw     VirtioWin

	verMu    sync.Mutex
	versions string // read from the pinned image once it ran
}

func (s *service) info(ctx context.Context) Info {
	in := Info{Image: s.c.Image, VirtioWin: s.virtioWin()}
	id, err := s.podman(ctx, "image", "inspect", "--format", "{{.Id}}", s.c.Image)
	switch {
	case err != nil:
		in.ImageDetail = "podman has no image " + s.c.Image + ": " + err.Error()
	case "sha256:"+strings.TrimPrefix(id, "sha256:") != s.c.Image:
		in.ImageDetail = "podman's image is " + id + ", not the pinned " + s.c.Image
	default:
		in.ImagePresent, in.ImageDetail = true, "present"
		s.verMu.Lock()
		defer s.verMu.Unlock()
		if s.versions == "" {
			v, err := s.podman(ctx, "run", "--rm", "--network=none", "--cgroups=disabled", "--cap-drop=all", s.c.Image,
				"cat", "/usr/share/gaia-converter/versions.txt")
			if err != nil {
				in.ImagePresent, in.ImageDetail = false, "the image does not run: "+err.Error()
				return in
			}
			s.versions = v
		}
		in.Versions = s.versions
	}
	return in
}

// virtioWin hashes the ISO when it changed since the last look.
func (s *service) virtioWin() VirtioWin {
	s.vwMu.Lock()
	defer s.vwMu.Unlock()
	v := VirtioWin{Path: s.c.VirtioWin, Version: VirtioWinVersion, Want: VirtioWinSHA256, URL: VirtioWinURL}
	fi, err := os.Stat(s.c.VirtioWin)
	if err != nil {
		v.State, v.Detail = "missing", "no virtio-win ISO at "+s.c.VirtioWin+": download it from "+VirtioWinURL
		s.vwStat = nil
		return v
	}
	if s.vwStat != nil && fi.Size() == s.vwStat.Size() && fi.ModTime().Equal(s.vwStat.ModTime()) {
		return s.vw
	}
	f, err := os.Open(s.c.VirtioWin)
	if err != nil {
		v.State, v.Detail = "missing", err.Error()
		return v
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		v.State, v.Detail = "missing", err.Error()
		return v
	}
	v.SHA256 = hex.EncodeToString(h.Sum(nil))
	if v.SHA256 != VirtioWinSHA256 {
		v.State, v.Detail = "mismatch", fmt.Sprintf("%s has sha256 %s, not virtio-win %s's %s", s.c.VirtioWin, v.SHA256, VirtioWinVersion, VirtioWinSHA256)
	} else {
		v.State, v.Detail = "ok", "virtio-win "+VirtioWinVersion+", sha256 matches"
	}
	s.vwStat, s.vw = fi, v
	return v
}

func (s *service) podman(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, s.c.Podman, append([]string{"--cgroup-manager=cgroupfs"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// checkRequest validates a request: the job directory and the vmx must be what gaia server made, directly where they
// belong, and not links.
func (s *service) checkRequest(r *Request) (job string, err error) {
	if !jobRE.MatchString(r.Job) || !nameRE.MatchString(r.Name) || filepath.Base(r.VMX) != r.VMX || !strings.HasSuffix(r.VMX, ".vmx") {
		return "", errors.New("job, name or vmx not allowed")
	}
	job = filepath.Join(s.c.JobsDir, r.Job)
	for _, p := range []string{job, filepath.Join(job, "in"), filepath.Join(job, "out")} {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("%s: not a directory", p)
		}
	}
	fi, err := os.Lstat(filepath.Join(job, "in", r.VMX))
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file in the job's in/", r.VMX)
	}
	return job, nil
}

func (s *service) convert(w http.ResponseWriter, r *http.Request) {
	var req Request
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "request: "+err.Error(), http.StatusBadRequest)
		return
	}
	job, err := s.checkRequest(&req)
	if err != nil {
		s.log.Warn("request refused", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := s.run(r.Context(), req, job)
	s.log.Info("conversion", "job", req.Job, "vm", req.Name, "state", res.State, "seconds", res.Seconds, "detail", res.Detail)
	writeJSON(w, res)
}

func (s *service) run(ctx context.Context, req Request, job string) Result {
	start := time.Now()
	res := Result{State: "not_run", Image: s.c.Image, Disks: []string{}, Log: "virt-v2v.log"}
	in := s.info(ctx)
	res.Versions = in.Versions
	if !in.ImagePresent {
		res.Detail = "the converter image is not usable: " + in.ImageDetail
		return res
	}
	out := filepath.Join(job, "out")
	logPath := filepath.Join(out, res.Log)
	lf, err := os.Create(logPath)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	defer lf.Close()
	// --init: virt-v2v must not be PID 1, which inherits orphaned processes and confuses virt-v2v's own waits
	args := []string{"--cgroup-manager=cgroupfs", "run", "--rm", "--init", "--network=none", "--cgroups=disabled", "--cap-drop=all",
		"--security-opt=no-new-privileges", "--userns=keep-id", "--group-add=keep-groups", "--device=/dev/kvm",
		"--volume=" + filepath.Join(job, "in") + ":/in:ro", "--volume=" + out + ":/out"}
	if req.Windows {
		vw := in.VirtioWin
		if vw.State != "ok" {
			res.Detail = "a Windows guest needs virtio-win " + VirtioWinVersion + " (ADR-005 §2): " + vw.Detail
			fmt.Fprintln(lf, res.Detail)
			return res
		}
		res.VirtioWin = &vw
		args = append(args, "--volume="+vw.Path+":/virtio-win.iso:ro", "--env=VIRTIO_WIN=/virtio-win.iso")
	}
	args = append(args, s.c.Image, "virt-v2v", "-i", "vmx", "/in/"+req.VMX, "-o", "local", "-os", "/out", "-of", "qcow2", "-on", req.Name)
	res.Command = append([]string{s.c.Podman}, args...)
	fmt.Fprintf(lf, "$ %s\n%s\n", strings.Join(res.Command, " "), in.Versions)
	if res.VirtioWin != nil {
		fmt.Fprintf(lf, "virtio-win %s, sha256 %s\n", res.VirtioWin.Version, res.VirtioWin.SHA256)
	}
	timeout, _ := time.ParseDuration(s.c.Timeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, s.c.Podman, args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		res.Detail = "podman could not be started: " + err.Error()
		return res
	}
	runErr := cmd.Wait()
	res.Seconds = time.Since(start).Seconds()
	if runErr != nil {
		res.State, res.Detail = "failed", fmt.Sprintf("virt-v2v: %v (%s)", runErr, errorLine(logPath))
		if cctx.Err() != nil {
			res.State, res.Detail = "not_run", fmt.Sprintf("the conversion did not end within %s", timeout)
		}
		return res
	}
	for _, t := range []string{"sda", "sdb", "sdc", "sdd", "sde", "sdf", "sdg", "sdh"} {
		n := req.Name + "-" + t
		if fi, err := os.Lstat(filepath.Join(out, n)); err == nil && fi.Mode().IsRegular() {
			res.Disks = append(res.Disks, n)
		}
	}
	res.DomainXML = req.Name + ".xml"
	if len(res.Disks) == 0 {
		res.State, res.Detail = "failed", "virt-v2v finished but wrote no disk"
		return res
	}
	if _, err := os.Lstat(filepath.Join(out, res.DomainXML)); err != nil {
		res.State, res.Detail = "failed", "virt-v2v finished but wrote no domain XML"
		return res
	}
	res.State, res.Detail = "converted", fmt.Sprintf("%d disk(s) in %.1f s", len(res.Disks), res.Seconds)
	return res
}

// errorLine returns virt-v2v's own error message from its log, or the last line when there is none (after an error
// virt-v2v prints how to report a bug, so the last line is rarely the reason).
func errorLine(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	var last, errLine string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			last = l
			if strings.HasPrefix(l, "virt-v2v: error:") {
				errLine = l
			}
		}
	}
	if errLine != "" {
		return errLine
	}
	return "last line: " + last
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// notifyReady tells systemd (Type=notify) that the socket exists.
func notifyReady() {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	if conn, err := net.Dial("unixgram", addr); err == nil {
		conn.Write([]byte("READY=1"))
		conn.Close()
	}
}

// Client is gaia server's side.
type Client struct {
	Socket string
}

func (c Client) http() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", c.Socket)
	}}}
}

// Info asks the service about its image and virtio-win.
func (c Client) Info(ctx context.Context) (Info, error) {
	var in Info
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://convert/v1/info", nil)
	resp, err := c.http().Do(req)
	if err != nil {
		return in, fmt.Errorf("converter service at %s: %w", c.Socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return in, fmt.Errorf("converter service: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return in, json.NewDecoder(resp.Body).Decode(&in)
}

// Convert runs one conversion and waits for it.
func (c Client) Convert(ctx context.Context, r Request) (Result, error) {
	var res Result
	b, _ := json.Marshal(r)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://convert/v1/convert", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return res, fmt.Errorf("converter service at %s: %w", c.Socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return res, fmt.Errorf("converter service: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return res, json.NewDecoder(resp.Body).Decode(&res)
}

package ansible

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Host is one server in the inventory. Name is its UID.
type Host struct {
	Name    string
	Address string
	Vars    map[string]any
}

// Job is one ansible-runner run: a playbook of a project on some hosts.
type Job struct {
	Name     string // container name, unique among running jobs
	Image    string // execution environment
	Project  func(dir string) error
	Playbook string // relative to the project
	Hosts    []Host
	User     string
	SSHKey   string // private key
	Forks    int
	Timeout  time.Duration
	// Secrets are values that must not appear in events. The SSH key is
	// always one.
	Secrets []string
}

// Event is one event of ansible-runner's JSON stream, masked.
type Event struct {
	Counter int
	UUID    string
	Type    string // runner_on_ok, playbook_on_task_start...
	Host    string // UID, empty for events that are not about a host
	Task    string
	Changed bool
	Failed  bool
	Stdout  string
	Data    json.RawMessage // event_data
	Created time.Time
}

// Stats are the play recap: per host counts.
type Stats struct {
	OK, Changed, Failures, Unreachable, Skipped map[string]int
}

// Result is the outcome of a job.
type Result struct {
	RC    int
	Stats Stats
}

// Failed lists the hosts that failed or were unreachable.
func (r Result) Failed() []string {
	var out []string
	for h, n := range r.Stats.Failures {
		if n > 0 {
			out = append(out, h)
		}
	}
	for h, n := range r.Stats.Unreachable {
		if n > 0 && r.Stats.Failures[h] == 0 {
			out = append(out, h)
		}
	}
	return out
}

// Podman runs jobs with ansible-runner inside the execution environment
// container. Rootless podman maps the container's user to the caller.
type Podman struct {
	Command string // default "podman"
	// TempDir holds the job's private data directory; default /dev/shm
	// when it exists, so that the key never reaches a disk.
	TempDir string
}

// Run runs the job, calling on for every event in order. The private data
// directory, with the key in it, is removed when Run returns.
func (p Podman) Run(ctx context.Context, j Job, on func(Event) error) (Result, error) {
	bin := p.Command
	if bin == "" {
		bin = "podman"
	}
	dir, err := os.MkdirTemp(p.tempDir(), "gaia-run-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := writeDataDir(dir, j); err != nil {
		return Result{}, err
	}

	if j.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, j.Timeout)
		defer cancel()
	}
	args := []string{"run", "--rm", "--name", j.Name, "-v", dir + ":/runner:Z"}
	if os.Geteuid() == 0 {
		args = append(args, "--user", "0")
	} else {
		args = append(args, "--userns", "keep-id")
	}
	args = append(args, j.Image, "ansible-runner", "run", "/runner", "-p", j.Playbook, "-j")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Removing the container ends ansible-runner and its SSH sessions;
		// killing podman alone would leave the container running.
		_ = exec.Command(bin, "rm", "-f", "-t", "10", j.Name).Run() //nolint:gosec // our own container name
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 30 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 64 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", bin, err)
	}

	m := newMasker(append([]string{j.SSHKey}, j.Secrets...))
	res, readErr := read(stdout, m, on)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return res, fmt.Errorf("ansible-runner: %w", ctx.Err())
	}
	if readErr != nil {
		return res, readErr
	}
	var exit *exec.ExitError
	switch {
	case errors.As(waitErr, &exit):
		res.RC = exit.ExitCode()
	case waitErr != nil:
		return res, waitErr
	}
	if res.Stats.OK == nil {
		return res, fmt.Errorf("ansible-runner exited %d without a play recap: %s", res.RC, m.mask(strings.TrimSpace(stderr.String())))
	}
	return res, nil
}

func (p Podman) tempDir() string {
	if p.TempDir != "" {
		return p.TempDir
	}
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		return "/dev/shm"
	}
	return os.TempDir()
}

// writeDataDir lays out ansible-runner's private data directory.
func writeDataDir(dir string, j Job) error {
	for _, d := range []string{"env", "inventory", "project"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			return err
		}
	}
	if err := j.Project(filepath.Join(dir, "project")); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	hosts := map[string]any{}
	for _, h := range j.Hosts {
		v := map[string]any{"ansible_host": h.Address, "ansible_user": j.User}
		for k, x := range h.Vars {
			v[k] = x
		}
		hosts[h.Name] = v
	}
	inv, err := json.Marshal(map[string]any{"all": map[string]any{"hosts": hosts}})
	if err != nil {
		return err
	}
	// New servers have new host keys; Gaia does not know them yet (PoC).
	envvars, _ := json.Marshal(map[string]string{"ANSIBLE_HOST_KEY_CHECKING": "False", "ANSIBLE_NOCOLOR": "True"})
	files := map[string][]byte{
		"inventory/hosts.json": inv,
		"env/envvars":          envvars,
		"env/ssh_key":          []byte(strings.TrimRight(j.SSHKey, "\n") + "\n"),
	}
	if j.Forks > 0 {
		files["env/cmdline"] = []byte("--forks " + strconv.Itoa(j.Forks))
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// maxEventData caps what is kept of one event's data.
const maxEventData = 64 << 10

type rawEvent struct {
	UUID      string          `json:"uuid"`
	Counter   int             `json:"counter"`
	Event     string          `json:"event"`
	Stdout    string          `json:"stdout"`
	Created   string          `json:"created"`
	EventData json.RawMessage `json:"event_data"`
}

type eventData struct {
	Host string `json:"host"`
	Task string `json:"task"`
	Res  struct {
		Changed bool `json:"changed"`
	} `json:"res"`
	OK          map[string]int `json:"ok"`
	Changed     map[string]int `json:"changed"`
	Failures    map[string]int `json:"failures"`
	Dark        map[string]int `json:"dark"`
	Skipped     map[string]int `json:"skipped"`
	IgnoreError bool           `json:"ignore_errors"`
}

// read parses the JSON stream. Lines that are not events (ssh-agent's
// messages) are skipped.
func read(r io.Reader, m *masker, on func(Event) error) (Result, error) {
	var res Result
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e rawEvent
		if err := json.Unmarshal(line, &e); err != nil || e.Event == "" {
			continue
		}
		var d eventData
		_ = json.Unmarshal(e.EventData, &d)
		ev := Event{
			Counter: e.Counter, UUID: e.UUID, Type: e.Event, Host: d.Host, Task: d.Task,
			Changed: d.Res.Changed,
			Failed:  (e.Event == "runner_on_failed" && !d.IgnoreError) || e.Event == "runner_on_unreachable",
			Stdout:  m.mask(e.Stdout),
			Data:    m.maskJSON(e.EventData),
		}
		if len(ev.Data) > maxEventData {
			ev.Data, _ = json.Marshal(map[string]any{"truncated": len(ev.Data)})
		}
		ev.Created, _ = time.Parse("2006-01-02T15:04:05.999999", e.Created)
		if e.Event == "playbook_on_stats" {
			res.Stats = Stats{OK: d.OK, Changed: d.Changed, Failures: d.Failures, Unreachable: d.Dark, Skipped: d.Skipped}
			if res.Stats.OK == nil {
				res.Stats.OK = map[string]int{}
			}
		}
		if on != nil {
			if err := on(ev); err != nil {
				return res, err
			}
		}
	}
	return res, sc.Err()
}

// limited writes at most n bytes to w and drops the rest.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// Image returns ref pinned to the digest of the local image, so that the
// run uses, and its record names, exactly that image. The image must be
// present: Gaia does not pull during a run.
func (p Podman) Image(ctx context.Context, ref string) (string, error) {
	bin := p.Command
	if bin == "" {
		bin = "podman"
	}
	out, err := exec.CommandContext(ctx, bin, "image", "inspect", "--format", "{{.Digest}}", ref).Output() //nolint:gosec // the Platform's image
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s: %s", ref, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	digest := strings.TrimSpace(string(out))
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("%s: no digest (%q)", ref, digest)
	}
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	return ref + "@" + digest, nil
}

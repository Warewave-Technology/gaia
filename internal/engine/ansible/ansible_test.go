package ansible

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePodman is a podman that copies the private data directory to
// $FAKE_COPY and prints $FAKE_EVENTS, or sleeps when $FAKE_SLEEP is set.
const fakePodman = `#!/bin/sh
[ "$1" = rm ] && exit 0
while [ $# -gt 0 ]; do
  if [ "$1" = -v ]; then dir="${2%%:*}"; fi
  shift
done
cp -R "$dir" "$FAKE_COPY"
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
echo "Identity added: /runner/artifacts/x/ssh_key_data (gaia@lab)"
cat "$FAKE_EVENTS"
exit "${FAKE_RC:-0}"
`

const key = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\n-----END OPENSSH PRIVATE KEY-----\n"

func events(lines ...map[string]any) string {
	var b strings.Builder
	for i, l := range lines {
		l["counter"] = i + 1
		l["uuid"] = "u" + string(rune('a'+i))
		l["created"] = "2026-10-24T17:50:01.123456"
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

func setup(t *testing.T, stream string) (Podman, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "podman")
	if err := os.WriteFile(bin, []byte(fakePodman), 0o755); err != nil {
		t.Fatal(err)
	}
	ev := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(ev, []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "copy")
	t.Setenv("FAKE_EVENTS", ev)
	t.Setenv("FAKE_COPY", cp)
	return Podman{Command: bin, TempDir: t.TempDir()}, cp
}

func job() Job {
	return Job{
		Name: "gaia-test", Image: "ee:dev", Playbook: "baseline/linux.yml", User: "gaia", SSHKey: key, Forks: 5,
		Hosts:   []Host{{Name: "uid1", Address: "10.0.0.5", Vars: map[string]any{"gaia_hostname": "web01"}}},
		Secrets: []string{"şifre-çok-gizli"},
		Project: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "site.yml"), []byte("- hosts: all"), 0o600)
		},
	}
}

func TestRunWritesTheDataDirAndReadsEvents(t *testing.T) {
	p, cp := setup(t, events(
		map[string]any{"event": "playbook_on_start", "event_data": map[string]any{}},
		map[string]any{"event": "runner_on_ok", "stdout": "changed: [uid1]",
			"event_data": map[string]any{"host": "uid1", "task": "Hostname", "res": map[string]any{"changed": true}}},
		map[string]any{"event": "runner_on_ok", "stdout": "ok: [uid1] => şifre-çok-gizli",
			"event_data": map[string]any{"host": "uid1", "task": "Debug", "res": map[string]any{"msg": "pw şifre-çok-gizli", "key": key}}},
		map[string]any{"event": "playbook_on_stats", "stdout": "PLAY RECAP",
			"event_data": map[string]any{"ok": map[string]int{"uid1": 3}, "changed": map[string]int{"uid1": 1}, "failures": map[string]int{}, "dark": map[string]int{}}},
	))
	var got []Event
	res, err := p.Run(context.Background(), job(), func(e Event) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatal(err)
	}

	inv, err := os.ReadFile(filepath.Join(cp, "inventory", "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		All struct {
			Hosts map[string]map[string]any `json:"hosts"`
		} `json:"all"`
	}
	if err := json.Unmarshal(inv, &doc); err != nil {
		t.Fatal(err)
	}
	h := doc.All.Hosts["uid1"]
	if h["ansible_host"] != "10.0.0.5" || h["ansible_user"] != "gaia" || h["gaia_hostname"] != "web01" {
		t.Errorf("inventory host = %v", h)
	}
	if k, _ := os.ReadFile(filepath.Join(cp, "env", "ssh_key")); string(k) != key {
		t.Errorf("ssh_key = %q", k)
	}
	if c, _ := os.ReadFile(filepath.Join(cp, "env", "cmdline")); string(c) != "--forks 5" {
		t.Errorf("cmdline = %q", c)
	}
	if _, err := os.Stat(filepath.Join(cp, "project", "site.yml")); err != nil {
		t.Error(err)
	}
	left, _ := os.ReadDir(p.TempDir)
	if len(left) != 0 {
		t.Errorf("the private data directory was left behind: %v", left)
	}

	if len(got) != 4 {
		t.Fatalf("%d events, want 4 (the ssh-agent line is not one)", len(got))
	}
	if e := got[1]; e.Host != "uid1" || e.Task != "Hostname" || !e.Changed || e.Counter != 2 || e.Created.IsZero() {
		t.Errorf("event = %+v", e)
	}
	masked := got[2]
	all := masked.Stdout + string(masked.Data)
	for _, s := range []string{"şifre", `ş`, "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ", "PRIVATE KEY-----\\n"} {
		if strings.Contains(all, s) {
			t.Errorf("%q is not masked: %s", s, all)
		}
	}
	if !strings.Contains(masked.Stdout, Masked) || !strings.Contains(string(masked.Data), Masked) {
		t.Errorf("no mask in %s", all)
	}
	if res.Stats.OK["uid1"] != 3 || res.Stats.Changed["uid1"] != 1 || len(res.Failed()) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestFailedAndUnreachableHosts(t *testing.T) {
	p, _ := setup(t, events(
		map[string]any{"event": "runner_on_failed", "event_data": map[string]any{"host": "uid1", "task": "x"}},
		map[string]any{"event": "runner_on_failed", "event_data": map[string]any{"host": "uid2", "task": "y", "ignore_errors": true}},
		map[string]any{"event": "runner_on_unreachable", "event_data": map[string]any{"host": "uid3"}},
		map[string]any{"event": "playbook_on_stats", "event_data": map[string]any{
			"ok": map[string]int{"uid2": 1}, "failures": map[string]int{"uid1": 1}, "dark": map[string]int{"uid3": 1}}},
	))
	t.Setenv("FAKE_RC", "2")
	var failed []string
	res, err := p.Run(context.Background(), job(), func(e Event) error {
		if e.Failed {
			failed = append(failed, e.Host)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(failed, ",") != "uid1,uid3" {
		t.Errorf("failed events for %v", failed)
	}
	if res.RC != 2 || len(res.Failed()) != 2 {
		t.Errorf("result rc %d, failed %v", res.RC, res.Failed())
	}
}

func TestNoRecapIsAnError(t *testing.T) {
	p, _ := setup(t, "")
	t.Setenv("FAKE_RC", "1")
	if _, err := p.Run(context.Background(), job(), nil); err == nil || !strings.Contains(err.Error(), "without a play recap") {
		t.Errorf("err = %v", err)
	}
}

func TestTimeoutStopsTheJob(t *testing.T) {
	p, _ := setup(t, "")
	t.Setenv("FAKE_SLEEP", "30")
	j := job()
	j.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := p.Run(context.Background(), j, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestKnownHostsTurnOnHostKeyChecking(t *testing.T) {
	recap := map[string]any{"event": "playbook_on_stats", "event_data": map[string]any{"ok": map[string]int{"uid1": 1}, "changed": map[string]int{}, "failures": map[string]int{}, "dark": map[string]int{}}}
	p, cp := setup(t, events(recap))
	j := job()
	if _, err := p.Run(context.Background(), j, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(cp, "env", "envvars"))
	if !strings.Contains(string(env), `"ANSIBLE_HOST_KEY_CHECKING":"False"`) {
		t.Errorf("without known hosts: %s", env)
	}

	p, cp = setup(t, events(recap))
	j.KnownHosts = "10.0.0.5 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA==\n"
	if _, err := p.Run(context.Background(), j, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	env, _ = os.ReadFile(filepath.Join(cp, "env", "envvars"))
	kh, _ := os.ReadFile(filepath.Join(cp, "known_hosts"))
	if !strings.Contains(string(env), `"ANSIBLE_HOST_KEY_CHECKING":"True"`) || !strings.Contains(string(env), "UserKnownHostsFile=/runner/known_hosts") ||
		!strings.Contains(string(env), "StrictHostKeyChecking=yes") || string(kh) != j.KnownHosts {
		t.Errorf("with known hosts: env %s, known_hosts %q", env, kh)
	}
}

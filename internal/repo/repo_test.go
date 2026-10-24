package repo

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
)

func TestMirrorFollowsTheBranch(t *testing.T) {
	ctx := context.Background()
	up := gittest.New(t)
	up.Write("a.yaml", "a: 1")
	first := up.Commit("first")
	m := &Mirrors{Dir: t.TempDir()}
	r := Repo{Name: "platform", URL: "file://" + up.Dir, Ref: "master"}
	if r.Local() {
		t.Fatal("a file:// URL is mirrored")
	}
	got, err := m.Sync(ctx, r)
	if err != nil || got != first {
		t.Fatalf("first sync: %s %v", got, err)
	}
	up.Write("a.yaml", "a: 2")
	second := up.Commit("second")
	if got, _ := m.Sync(ctx, r); got != second {
		t.Errorf("second sync: %s, want %s", got, second)
	}
	if err := m.Ensure(ctx, r, first); err != nil {
		t.Errorf("an older commit: %v", err)
	}
	if err := m.Ensure(ctx, r, strings.Repeat("ab", 20)); err == nil {
		t.Error("a commit that does not exist")
	}
	// A local path is read in place.
	local := Repo{Name: "dev", URL: up.Dir}
	if got, _ := m.Sync(ctx, local); got != second || m.Path(local) != up.Dir {
		t.Errorf("local: %s at %s", got, m.Path(local))
	}
}

const platform = `apiVersion: gaia/v1alpha1
kind: Platform
metadata: { name: platform }
spec:
  secrets: bao
  engines:
    provisioning: { tool: pulumi, state: state }
    configuration: { tool: ansible, executor: ansible-runner, executionEnvironment: ee:1, source: config }
  contracts:
    - { provider: contracts, paths: [teams] }
`

func providers(contracts, config string) string {
	return `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: bao }
spec: { type: openbao, endpoint: http://bao:8200 }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: state }
spec: { type: postgres, endpoint: "postgres://db/pulumi", credentials: "bao://kv/state" }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: contracts }
spec: { type: git, url: "file://` + contracts + `", ref: master }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: config }
spec: { type: git, url: "file://` + config + `", ref: master, path: ansible }
`
}

func TestHeadFollowsThePlatformToTheOtherRepositories(t *testing.T) {
	ctx := context.Background()
	contracts := gittest.New(t)
	contracts.Write("teams/README.md", "no objects yet", "other/x.yaml", "not: read")
	cc := contracts.Commit("contracts")
	config := gittest.New(t)
	config.Write("ansible/site.yml", "- hosts: all")
	cfg := config.Commit("config")
	plat := gittest.New(t)
	plat.Write("platform/platform.yaml", platform, "platform/providers.yaml", providers(contracts.Dir, config.Dir))
	pc := plat.Commit("platform")

	m := &Mirrors{Dir: t.TempDir()}
	s, err := m.Head(ctx, Rev{Name: "platform", URL: "file://" + plat.Dir, Ref: "master", Paths: []string{"platform"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Platform.Commit != pc || len(s.Contracts) != 1 || s.Contracts[0].Commit != cc || s.Config == nil || s.Config.Commit != cfg || s.Config.Paths[0] != "ansible" {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Label() != pc+"+contracts@"+cc {
		t.Errorf("label = %s", s.Label())
	}
	g, err := m.Graph(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if g.Catalog.Commit != s.Label() {
		t.Errorf("graph commit = %s", g.Catalog.Commit)
	}
}

func TestHostKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	otherKey, _ := ssh.NewPublicKey(other)
	line := func(host string) string { return host + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) }
	cb, err := hostKeys(line("github.com") + "\n" + line("[git.corp.local]:2222") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 22}
	if err := cb("github.com:22", addr, key); err != nil {
		t.Errorf("known host: %v", err)
	}
	if err := cb("git.corp.local:2222", addr, key); err != nil {
		t.Errorf("known host on another port: %v", err)
	}
	if err := cb("github.com:22", addr, otherKey); err == nil {
		t.Error("a changed host key was accepted")
	}
	if err := cb("gitlab.com:22", addr, key); err == nil {
		t.Error("an unknown host was accepted")
	}
	if _, err := hostKeys(""); err == nil {
		t.Error("no host keys accepted")
	}
}

type secrets map[string]map[string]string

func (s secrets) Read(_ context.Context, ref string) (map[string]string, error) { return s[ref], nil }

func TestCredentials(t *testing.T) {
	m := &Mirrors{Secrets: secrets{
		"bao://kv/git/https": {"token": "t"},
		"bao://kv/git/ssh":   {"private_key": "not a key"},
	}}
	ctx := context.Background()
	a, err := m.auth(ctx, Repo{Name: "x", URL: "https://github.com/org/r.git", Credentials: "bao://kv/git/https"})
	if err != nil || a.String() == "" {
		t.Errorf("https: %v %v", a, err)
	}
	if _, err := m.auth(ctx, Repo{Name: "x", URL: "git@github.com:org/r.git", Credentials: "bao://kv/git/ssh"}); err == nil || !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("ssh without host keys: %v", err)
	}
	if _, err := (&Mirrors{}).auth(ctx, Repo{Name: "x", URL: "https://x", Credentials: "bao://kv/a"}); err == nil {
		t.Error("credentials without OpenBao")
	}
}

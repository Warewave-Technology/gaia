package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
)

const teamSource = `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: corp-ad }
spec: { type: activedirectory, endpoint: "%s", credentials: bao://kv/platform/ad }
---
apiVersion: gaia/v1alpha1
kind: TeamSource
metadata: { name: corp }
spec:
  provider: corp-ad
  select: 'group.dn.endsWith(",OU=Teams,DC=gaia,DC=dev") && group.cn.startsWith("SG-Team-") && !group.cn.endsWith("-Approvers")'
  naming: 'group.cn.lowerAscii().replace("sg-team-", "team-")'
  map:
    displayName: group.description
    approvers: 'group.cn + "-Approvers"'
    secretsPrefix: '"bao://kv/apps/" + name + "/"'
  membership: { recursive: true }
  output: platform/generated/teams
`

func TestTeamSourceWritesTeams(t *testing.T) {
	itest.Require(t)
	ctx := context.Background()
	samba := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	// The lab catalog with the TeamSource, in a bare repository.
	src := gittest.New(t)
	root := filepath.Join("..", "..", "examples", "lab")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		content := string(data)
		if rel == filepath.Join("platform", "providers", "config-repo.yaml") {
			content = strings.Replace(strings.Replace(content, "url: /home/labadm/gaia", "url: "+src.Dir, 1), "ref: main", "ref: HEAD", 1)
		}
		src.Write(filepath.ToSlash(rel), content)
		return nil
	})
	src.Write("platform/teams.yaml", strings.Replace(teamSource, "%s", samba.URL, 1))
	src.Commit("lab with teams")
	bare := t.TempDir()
	if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: src.Dir}); err != nil {
		t.Fatal(err)
	}
	platform := repo.Rev{Name: "platform", URL: "file://" + bare, Ref: "master"}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	m := &repo.Mirrors{Dir: t.TempDir()}
	s := &Syncer{Store: st, Repos: m, Writer: &repo.Writer{Mirrors: m, Signer: signature.SSHSigner{Key: key}, Name: "Gaia", Email: "gaia@example.com"},
		Secrets: func(_ context.Context, _, ref string) (map[string]string, error) {
			return map[string]string{"username": "gaia-reader@gaia.dev", "password": samba.Password("gaia-reader"), "ca": string(samba.CA), "server_name": "dc1.gaia.dev"}, nil
		}}
	run := func() Report {
		t.Helper()
		snap, err := m.Head(ctx, platform)
		if err != nil {
			t.Fatal(err)
		}
		g, err := m.Graph(ctx, snap)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(ctx, snap, g); err != nil {
			t.Fatal(err)
		}
		rows, _ := st.Q().SourceReports(ctx)
		if len(rows) != 1 {
			t.Fatalf("reports = %+v", rows)
		}
		return Report{Status: rows[0].Status, Commit: rows[0].Commit.String, Error: rows[0].Error.String, Source: string(rows[0].Detail)}
	}

	r := run()
	if r.Status != "committed" || !strings.Contains(r.Source, "platform/generated/teams/team-platform.yaml") || !strings.Contains(r.Source, "team-web.yaml") {
		t.Fatalf("first run: %+v", r)
	}
	snap, _ := m.Head(ctx, platform)
	g, err := m.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	web := g.Team("team-web")
	if web == nil || web.DisplayName != "SG-Team-Web" || web.ApproversGroup == nil || web.ApproversGroup.DN != "CN=SG-Team-Web-Approvers,OU=Teams,DC=gaia,DC=dev" ||
		web.Secrets.Prefix != "bao://kv/apps/team-web/" || web.ADGroup.Provider != "corp-ad" {
		t.Errorf("team-web = %+v", web)
	}
	if o, _ := g.Catalog.Get(catalog.KindTeam, "team-web"); !o.Generated() {
		t.Error("team-web is not marked generated")
	}

	if r := run(); r.Status != "unchanged" {
		t.Errorf("second run: %+v", r)
	}

	// A new team in AD is added; a removed one is removed.
	samba.Exec(t, "group", "add", "SG-Team-Data", "--groupou=OU=Teams", "--description=Data")
	samba.Exec(t, "group", "delete", "SG-Team-Web")
	if r := run(); r.Status != "committed" || !strings.Contains(r.Source, `"added": ["platform/generated/teams/team-data.yaml"]`) || !strings.Contains(r.Source, `"removed": ["platform/generated/teams/team-web.yaml"]`) {
		t.Errorf("after AD changed: %+v", r)
	}

	// The team that owns poc-web cannot go: the catalog would not load.
	samba.Exec(t, "group", "delete", "SG-Team-Platform")
	if r := run(); r.Status != "refused" || !strings.Contains(r.Error, `Team "team-platform" not found`) {
		t.Errorf("removing an owner: %+v", r)
	}
}

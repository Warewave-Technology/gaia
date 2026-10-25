package settings

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

func lab(t *testing.T) (*gittest.Repo, repo.Rev) {
	t.Helper()
	r := gittest.New(t)
	root := filepath.Join("..", "..", "examples", "lab")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		content := string(data)
		if rel == filepath.Join("platform", "providers", "config-repo.yaml") {
			// The config repository is this repository.
			content = strings.Replace(content, "url: /home/labadm/gaia", "url: "+r.Dir, 1)
			content = strings.Replace(content, "ref: main", "ref: HEAD", 1)
		}
		r.Write(filepath.ToSlash(rel), content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Commit("lab")
	return r, repo.Rev{Name: "platform", URL: r.Dir, Paths: []string{"platform", "contracts"}}
}

func open(t *testing.T) *store.Store {
	t.Helper()
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestImportFromGitAndVersions(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	st := &Store{DB: s}
	r, platform := lab(t)
	m := &repo.Mirrors{Dir: t.TempDir(), Settings: st}

	v, err := ImportGit(ctx, st, m, platform)
	if err != nil || v != 1 {
		t.Fatalf("import: %d %v", v, err)
	}
	files, _ := st.Files(ctx, 1)
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	all := strings.Join(paths, " ")
	for _, want := range []string{"settings:Network/lab-poc.yaml", "settings:Platform/", "settings:Provider/pve-lab.yaml", "settings:Policy/", "settings:Size/small.yaml", "settings:Size/medium.yaml"} {
		if !strings.Contains(all, want) {
			t.Errorf("%s not imported: %v", want, paths)
		}
	}
	if strings.Contains(all, "Baseline") || strings.Contains(all, "Server") {
		t.Errorf("Git kinds imported: %v", paths)
	}
	if v, err := ImportGit(ctx, st, m, platform); v != 0 || err != nil {
		t.Errorf("a second import: %d %v", v, err)
	}
	log, _ := s.Q().AuditLog(ctx, db.AuditLogParams{MaxRows: 10})
	if len(log) != 1 || log[0].Action != "settings.import" || log[0].Actor != "gaia" || log[0].Object.String != "settings@1" {
		t.Errorf("audit = %+v", log)
	}

	// Git's copy of a setting no longer counts.
	r.Write("platform/catalog/network.yaml", strings.Replace(read(t, "network.yaml"), "gateway: 10.50.0.1", "gateway: 10.50.0.254", 1))
	r.Commit("an edit in Git")
	snap, err := m.Head(ctx, platform)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Settings != 1 || !strings.HasSuffix(snap.Label(), "+settings@1") {
		t.Errorf("snapshot = %+v %s", snap, snap.Label())
	}
	g, err := m.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if gw := gateway(t, g); gw != "10.50.0.1" {
		t.Errorf("gateway from Git: %s", gw)
	}
	ignored := false
	for _, ig := range g.Catalog.Ignored {
		ignored = ignored || ig.Key.String() == "Network/lab-poc"
	}
	if !ignored {
		t.Errorf("ignored = %+v", g.Catalog.Ignored)
	}

	// A new version changes it; the old snapshot keeps the old one.
	net := strings.Replace(string(fileData(files, "settings:Network/lab-poc.yaml")), "gateway: 10.50.0.1", "gateway: 10.50.0.2", 1)
	if _, err := st.Write(ctx, Write{Base: 0, Changes: map[string][]byte{"settings:Network/lab-poc.yaml": []byte(net)}, Author: "ayse"}); !errors.Is(err, ErrStale) {
		t.Errorf("a stale write: %v", err)
	}
	if _, err := st.Write(ctx, Write{Base: 1, Changes: map[string][]byte{"settings:Baseline/x.yaml": []byte("x")}, Author: "ayse"}); err == nil {
		t.Error("a Git kind written to the settings")
	}
	d, err := st.Write(ctx, Write{Base: 1, Changes: map[string][]byte{"settings:Network/lab-poc.yaml": []byte(net)}, Author: "ayse", Reason: "gateway"})
	if err != nil || d.Version != 2 || strings.Join(d.Changed, ",") != "settings:Network/lab-poc.yaml" {
		t.Fatalf("write: %+v %v", d, err)
	}
	if d, err := st.Write(ctx, Write{Base: 2, Changes: map[string][]byte{"settings:Network/lab-poc.yaml": []byte(net)}, Author: "ayse"}); err != nil || d.Version != 0 {
		t.Errorf("a write that changes nothing: %+v %v", d, err)
	}
	snap2, _ := m.Head(ctx, platform)
	g2, err := m.Graph(ctx, snap2)
	if err != nil {
		t.Fatal(err)
	}
	g1, _ := m.Graph(ctx, snap)
	if gateway(t, g2) != "10.50.0.2" || gateway(t, g1) != "10.50.0.1" {
		t.Errorf("gateways: %s then %s", gateway(t, g1), gateway(t, g2))
	}

	// Versions are append-only, for the owner too.
	if _, err := s.Pool().Exec(ctx, "update settings_objects set data = 'x' where version = 1"); err == nil {
		t.Error("a version was changed")
	}
	if _, err := s.Pool().Exec(ctx, "delete from audit_log"); err == nil {
		t.Error("the audit log was deleted")
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "lab", "platform", "catalog", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileData(fs []catalog.File, p string) []byte {
	for _, f := range fs {
		if f.Path == p {
			return f.Data
		}
	}
	return nil
}

func gateway(t *testing.T, g *catalog.Graph) string {
	t.Helper()
	o, ok := g.Catalog.Get(catalog.KindNetwork, "lab-poc")
	if !ok {
		t.Fatal("no lab-poc")
	}
	return o.Spec.(*catalog.NetworkSpec).Gateway
}

func TestSplit(t *testing.T) {
	got, err := Split([]catalog.File{
		{Path: "a.yaml", Data: []byte("apiVersion: gaia/v1alpha1\nkind: Size\nmetadata: { name: s }\nspec: { cpu: 1, memoryGiB: 1, diskGiB: 1 } # tiny\n---\napiVersion: gaia/v1alpha1\nkind: Baseline\nmetadata: { name: b }\nspec: { os: linux, playbook: b.yml }\n")},
		{Path: "b.yaml", Data: []byte("apiVersion: gaia/v1alpha1\nkind: Network\nmetadata: { name: n, annotations: { gaia/mode: patch } }\nspec: { gateway: 10.0.0.1 }\n")},
		{Path: "README.md", Data: []byte("kind: Size")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(string(got["settings:Size/s.yaml"]), "# tiny") || got["settings:Network/n.patch.yaml"] == nil {
		t.Errorf("split = %v", got)
	}
}

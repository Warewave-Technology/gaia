package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// labRepo puts the lab catalog and extra files in a bare repository and
// returns how to follow it.
func labRepo(t *testing.T, extra map[string]string) repo.Rev {
	t.Helper()
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
	for p, c := range extra {
		src.Write(p, c)
	}
	src.Commit("lab")
	bare := t.TempDir()
	if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: src.Dir}); err != nil {
		t.Fatal(err)
	}
	return repo.Rev{Name: "platform", URL: "file://" + bare, Ref: "master", Paths: []string{"platform", "contracts"}}
}

func TestNetworkSourceWritesNetworks(t *testing.T) {
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	// gaia-ipam with pools, in the same PostgreSQL.
	if err := ipam.Migrate(ctx, strings.Replace(dsn, "/gaia?", "/postgres?", 1)); err != nil {
		t.Fatal(err)
	}
	ipsrv, err := ipam.NewServer(ctx, strings.Replace(dsn, "/gaia?", "/postgres?", 1), "ipam-token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ipsrv.Close)
	hs := httptest.NewServer(ipsrv.Handler())
	defer hs.Close()
	ic := ipamclient.New(hs.URL, "ipam-token")
	for _, p := range []ipam.Pool{
		{CIDR: "10.20.1.0/24", Gateway: "10.20.1.1", RangeStart: "10.20.1.10", RangeEnd: "10.20.1.200", Labels: map[string]string{"site": "lab", "env": "test", "zone": "app", "vlan": "120"}, Tags: []string{"gaia-managed"}},
		{CIDR: "10.20.2.0/24", RangeStart: "10.20.2.10", RangeEnd: "10.20.2.20", Labels: map[string]string{"site": "lab", "env": "test", "zone": "db"}},
		{CIDR: "10.20.3.0/24", RangeStart: "10.20.3.10", RangeEnd: "10.20.3.20", Labels: map[string]string{"site": "nowhere", "env": "test", "zone": "dmz", "vlan": "130"}, Tags: []string{"gaia-managed"}},
	} {
		if err := ic.SetPool(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	platform := labRepo(t, map[string]string{
		"platform/networks.yaml": `apiVersion: gaia/v1alpha1
kind: NetworkSource
metadata: { name: ipam-lab }
spec:
  provider: ipam
  select: '"gaia-managed" in subnet.tags'
  naming: 'subnet.site + "-" + subnet.env + "-" + subnet.zone'
  map:
    site: subnet.site
    attachment: '{"bridge": "vmbr0", "vlan": int(subnet.vlan)}'
    allowedEnvironments: '[subnet.env]'
    labels: '{"zone": subnet.zone}'
  output: platform/generated/networks
`,
		"platform/providers/ipam.yaml": "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: ipam }\nspec: { type: ipam, endpoint: \"" + hs.URL + "\", credentials: bao://kv/platform/ipam }\n",
	})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	m := &repo.Mirrors{Dir: t.TempDir()}
	s := &Syncer{Store: st, Repos: m, Writer: &repo.Writer{Mirrors: m, Signer: signature.SSHSigner{Key: key}, Name: "Gaia", Email: "gaia@example.com"},
		Secrets: func(context.Context, string, string) (map[string]string, error) {
			return map[string]string{"token": "ipam-token"}, nil
		}}
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
	if len(rows) != 1 || rows[0].Status != "committed" {
		t.Fatalf("report = %+v", rows)
	}
	detail := string(rows[0].Detail)
	// The pool in an unknown site is left out, and said so; the other
	// one is written.
	if !strings.Contains(detail, `"added": ["platform/generated/networks/lab-test-app.yaml"]`) || !strings.Contains(detail, `Site \"nowhere\" not found`) {
		t.Errorf("detail = %s", detail)
	}
	snap, _ = m.Head(ctx, platform)
	g, err = m.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := g.Catalog.Get(catalog.KindNetwork, "lab-test-app")
	if !ok {
		t.Fatal("no lab-test-app")
	}
	n := o.Spec.(*catalog.NetworkSpec)
	if n.Site != "lab" || n.Attachment.Bridge != "vmbr0" || n.Attachment.VLAN != 120 || n.IPAM.Pool != "10.20.1.0/24" || n.IPAM.Provider != "ipam" ||
		n.Gateway != "10.20.1.1" || strings.Join(n.AllowedEnvironments, ",") != "test" || o.Metadata.Labels["zone"] != "app" || !o.Generated() {
		t.Errorf("network = %+v, labels %v", n, o.Metadata.Labels)
	}
}

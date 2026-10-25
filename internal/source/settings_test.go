package source

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/settings"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// With the settings store in use, a NetworkSource writes its networks
// there as a new version, not to Git (design 17.6).
func TestNetworkSourceWritesSettings(t *testing.T) {
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
	pool := ipam.Pool{CIDR: "10.20.1.0/24", Gateway: "10.20.1.1", RangeStart: "10.20.1.10", RangeEnd: "10.20.1.200", Labels: map[string]string{"site": "lab", "env": "test", "zone": "app", "vlan": "120"}, Tags: []string{"gaia-managed"}}
	if err := ic.SetPool(ctx, pool); err != nil {
		t.Fatal(err)
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
  output: platform/generated/networks
`,
		"platform/providers/ipam.yaml": "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: ipam }\nspec: { type: ipam, endpoint: \"" + hs.URL + "\", credentials: bao://kv/platform/ipam }\n",
	})
	sets := &settings.Store{DB: st}
	m := &repo.Mirrors{Dir: t.TempDir(), Settings: sets}
	if v, err := settings.ImportGit(ctx, sets, m, platform); err != nil || v != 1 {
		t.Fatalf("import: %d %v", v, err)
	}
	s := &Syncer{Store: st, Repos: m, Settings: sets, // no Writer: nothing goes to Git
		Secrets: func(context.Context, string, string) (map[string]string, error) {
			return map[string]string{"token": "ipam-token"}, nil
		}}
	run := func() (db.SourceReport, *catalog.Graph) {
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
		return rows[0], g
	}
	rep, _ := run()
	if rep.Status != "stored" || !strings.Contains(string(rep.Detail), `"version": 2`) || !strings.Contains(string(rep.Detail), "settings:Network/lab-test-app.yaml") {
		t.Fatalf("first run: %s %s %s", rep.Status, rep.Detail, rep.Error.String)
	}
	_, g := run()
	o, ok := g.Catalog.Get(catalog.KindNetwork, "lab-test-app")
	if !ok || o.Source.File != "settings:Network/lab-test-app.yaml" || o.Metadata.Annotations[catalog.AnnotationGeneratedBy] != "NetworkSource/ipam-lab" {
		t.Fatalf("lab-test-app = %+v", o)
	}
	if rep, _ := run(); rep.Status != "unchanged" {
		t.Errorf("third run: %s %s", rep.Status, rep.Detail)
	}
	v, _ := st.Q().SettingsVersion(ctx, 2)
	if v.Author != "source/ipam-lab" {
		t.Errorf("version 2 = %+v", v)
	}
	log, _ := st.Q().AuditLog(ctx, db.AuditLogParams{MaxRows: 10, Action: pgText("settings.source")})
	if len(log) != 1 || log[0].Actor != "source/ipam-lab" {
		t.Errorf("audit = %+v", log)
	}

	// The pool is no longer Gaia's: its network goes, in a new version.
	pool.Tags = nil
	if err := ic.SetPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if rep, _ := run(); rep.Status != "stored" || !strings.Contains(string(rep.Detail), `"removed": ["settings:Network/lab-test-app.yaml"]`) {
		t.Errorf("after the pool went: %s %s %s", rep.Status, rep.Detail, rep.Error.String)
	}
}

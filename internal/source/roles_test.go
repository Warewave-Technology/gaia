package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func TestAnsibleRoleSourceWritesRoles(t *testing.T) {
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
	coll := "ansible/collections/ansible_collections/corp/web/roles/"
	platform := labRepo(t, map[string]string{
		"platform/roles.yaml": `apiVersion: gaia/v1alpha1
kind: AnsibleRoleSource
metadata: { name: corp-roles }
spec:
  provider: config-repo
  paths: ["collections/ansible_collections/*/*/roles/*", "roles/*"]
  exclude: ["*/roles/_*"]
  naming: 'role.collection == "" ? role.name.replace("_", "-") : role.collection + "." + role.name'
  map:
    os: 'has(meta.galaxy_info) && "platforms" in meta.galaxy_info ? meta.galaxy_info.platforms.map(p, p.name.lowerAscii()) : []'
    labels: '{"category": role.collection == "" ? "base" : role.collection}'
  output: platform/generated/ansible-roles
`,
		coll + "nginx/tasks/main.yml": "- ansible.builtin.debug: { msg: hi }\n",
		coll + "nginx/meta/main.yml":  "galaxy_info:\n  platforms:\n    - name: EL\n      versions: ['9']\n",
		coll + "nginx/meta/argument_specs.yml": `argument_specs:
  main:
    options:
      server_name: { type: str, required: true }
      worker_processes: { type: str, default: auto }
`,
		coll + "nginx/meta/gaia.yml":                   "monitoring: [nginx]\ncheckMode: supported\nparams:\n  server_name: { gaia: { pattern: '^[a-z.]+$' } }\n",
		"ansible/roles/gaia_linux_base/tasks/main.yml": "- ansible.builtin.debug: { msg: base }\n",
		coll + "_internal/tasks/main.yml":              "- ansible.builtin.debug: { msg: internal }\n",
		coll + "broken/tasks/main.yml":                 "- ansible.builtin.debug: { msg: broken }\n",
		coll + "broken/meta/argument_specs.yml":        "argument_specs:\n  main:\n    options:\n      blob: { type: raw }\n",
	})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	m := &repo.Mirrors{Dir: t.TempDir()}
	s := &Syncer{Store: st, Repos: m, Writer: &repo.Writer{Mirrors: m, Signer: signature.SSHSigner{Key: key}, Name: "Gaia", Email: "gaia@example.com"}}
	run := func() (string, string) {
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
		return rows[0].Status, string(rows[0].Detail) + rows[0].Error.String
	}
	status, detail := run()
	if status != "committed" || !strings.Contains(detail, "web.nginx.yaml") || !strings.Contains(detail, "gaia-linux-base.yaml") ||
		strings.Contains(detail, "internal") || !strings.Contains(detail, `blob.type: \"raw\" is not allowed`) {
		t.Fatalf("first run: %s %s", status, detail)
	}
	snap, _ := m.Head(ctx, platform)
	g, err := m.Graph(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := g.Catalog.Get(catalog.KindAnsibleRole, "web.nginx")
	if !ok {
		t.Fatal("no web.nginx")
	}
	r := o.Spec.(*catalog.AnsibleRoleSpec)
	if r.Source.Role != "corp.web.nginx" || r.Source.Provider != "config-repo" || strings.Join(r.OS, ",") != "el" || strings.Join(r.Monitoring, ",") != "nginx" ||
		r.CheckMode != "supported" || !r.Params["server_name"].Required || r.Params["server_name"].Gaia == nil || r.Params["server_name"].Gaia.Pattern != "^[a-z.]+$" ||
		o.Metadata.Labels["category"] != "web" || !strings.HasPrefix(o.Metadata.Annotations[catalog.AnnotationSourceRevision], "tree:") {
		t.Errorf("web.nginx = %+v labels %v", r, o.Metadata.Labels)
	}
	if base, ok := g.Catalog.Get(catalog.KindAnsibleRole, "gaia-linux-base"); !ok || base.Spec.(*catalog.AnsibleRoleSpec).Source.Role != "gaia_linux_base" {
		t.Errorf("gaia-linux-base = %+v", base)
	}
	if status, _ := run(); status != "unchanged" {
		t.Errorf("second run: %s", status)
	}
}

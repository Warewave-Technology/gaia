package catalog

import (
	"errors"
	"strings"
	"testing"
)

// base is a small valid catalog: platform, a Proxmox and a vSphere site,
// one network on each, an image with templates for both, roles and a
// server.
var base = map[string]string{
	"platform.yaml": `apiVersion: gaia/v1alpha1
kind: Platform
metadata: { name: platform }
spec:
  secrets: bao
  engines:
    provisioning: { tool: pulumi, state: state }
    configuration: { tool: ansible, executor: ansible-runner, executionEnvironment: ee:1, source: repo }
`,
	"policy.yaml": `apiVersion: gaia/v1alpha1
kind: Policy
metadata: { name: default }
spec:
  requiredLabels: [env, owner]
  environments: { test: { approval: none }, prod: { approval: required } }
`,
	"providers.yaml": `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: bao }
spec: { type: openbao, endpoint: http://bao }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: state }
spec: { type: s3, endpoint: http://s3, bucket: st }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: repo }
spec: { type: git, url: git@example.com:x.git }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: ipam }
spec: { type: ipam, endpoint: http://ipam }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: pve }
spec: { type: proxmox, endpoint: https://pve:8006 }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: vc }
spec: { type: vsphere, endpoint: https://vc }
`,
	"sites.yaml": `apiVersion: gaia/v1alpha1
kind: Site
metadata: { name: lab }
spec:
  compute: { provider: pve, nodes: [n1], vmids: { start: 9400, end: 9499 } }
---
apiVersion: gaia/v1alpha1
kind: Site
metadata: { name: ank }
spec:
  compute: { provider: vc, datacenter: DC, cluster: CL }
`,
	"networks.yaml": `apiVersion: gaia/v1alpha1
kind: Network
metadata: { name: lab-app }
spec:
  site: lab
  attachment: { vnet: v1 }
  ipam: { provider: ipam, pool: 10.0.0.0/24 }
  allowedEnvironments: [test]
---
apiVersion: gaia/v1alpha1
kind: Network
metadata: { name: ank-app }
spec:
  site: ank
  attachment: { portgroup: pg120 }
  ipam: { provider: ipam, pool: 10.1.0.0/24 }
`,
	"image.yaml": `apiVersion: gaia/v1alpha1
kind: Image
metadata: { name: rocky9 }
spec:
  os: linux
  connection: ssh
  templates:
    - { provider: pve, template: { vmid: 9300 } }
    - { provider: vc, template: tpl-rocky9 }
`,
	"misc.yaml": `apiVersion: gaia/v1alpha1
kind: Size
metadata: { name: small }
spec: { cpu: 2, memoryGiB: 2, diskGiB: 20 }
---
apiVersion: gaia/v1alpha1
kind: Baseline
metadata: { name: linux-base }
spec: { os: linux, playbook: baseline/linux.yml }
`,
	"roles.yaml": `apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.nginx }
spec:
  source: { provider: repo, role: corp.web.nginx }
  os: [linux]
  conflicts: [web.apache]
  params:
    worker_processes: { type: str, default: auto }
    server_name: { type: str, required: true }
---
apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.apache }
spec:
  source: { provider: repo, role: corp.web.apache }
`,
	"server.yaml": `apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: web
  labels: { env: test, owner: team-web }
spec:
  replicas: 2
  image: rocky9
  size: small
  network: lab-app
  baseline: linux-base
  ansibleRoles:
    - name: web.nginx
      params: { server_name: web.example.com }
`,
}

// catalogWith returns the base files with replacements applied: each pair
// is (file, old, new); old "" replaces the whole file, file not in base
// adds it.
func catalogWith(t *testing.T, edits ...string) []File {
	t.Helper()
	m := map[string]string{}
	for k, v := range base {
		m[k] = v
	}
	for i := 0; i+2 < len(edits); i += 3 {
		f, old, new := edits[i], edits[i+1], edits[i+2]
		switch {
		case old == "":
			m[f] = new
		case !strings.Contains(m[f], old):
			t.Fatalf("%s does not contain %q", f, old)
		default:
			m[f] = strings.Replace(m[f], old, new, 1)
		}
	}
	var out []File
	for k, v := range m {
		out = append(out, File{Path: k, Data: []byte(v)})
	}
	return out
}

func resolveErrors(t *testing.T, fs []File) []string {
	t.Helper()
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = Resolve(cat)
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Resolve succeeded, want errors")
	}
	return strings.Split(errs.Error(), "\n")
}

func TestResolveBase(t *testing.T) {
	cat, err := Load("c0ffee", catalogWith(t))
	if err != nil {
		t.Fatal(err)
	}
	g, err := Resolve(cat)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	web := Key{KindServer, "web"}
	got := map[string]Key{}
	for _, r := range g.Refs(web) {
		got[r.Field] = r.To
	}
	want := map[string]Key{
		"spec.image":           {KindImage, "rocky9"},
		"spec.size":            {KindSize, "small"},
		"spec.network":         {KindNetwork, "lab-app"},
		"spec.baseline":        {KindBaseline, "linux-base"},
		"spec.ansibleRoles[0]": {KindAnsibleRole, "web.nginx"},
	}
	for f, k := range want {
		if got[f] != k {
			t.Errorf("Refs(web)[%s] = %v, want %v", f, got[f], k)
		}
	}
	// Reverse edges: the Proxmox provider is used by a site and an image.
	var from []string
	for _, r := range g.UsedBy(Key{KindProvider, "pve"}) {
		from = append(from, r.From.String()+"."+r.Field)
	}
	if s := strings.Join(from, ","); !strings.Contains(s, "Site/lab.spec.compute.provider") || !strings.Contains(s, "Image/rocky9.spec.templates[0].provider") {
		t.Errorf("UsedBy(pve) = %s", s)
	}
	if g.Platform().Metadata.Name != "platform" || g.Policy().Metadata.Name != "default" {
		t.Error("Platform/Policy accessors")
	}
}

func TestMissingReferenceSuggestsAName(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "server.yaml", "network: lab-app", "network: lab-ap"))
	wantError(t, lines, "server.yaml:10:", "Server/web", `spec.network: Network "lab-ap" not found (did you mean "lab-app"?)`)
}

func TestReferenceLinesPointAtTheField(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "image.yaml", "provider: vc,", "provider: vcc,"))
	wantError(t, lines, "image.yaml:9:", "Image/rocky9", `spec.templates[1].provider: Provider "vcc" not found (did you mean "vc"?)`)
}

func TestProviderTypeMustFit(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "platform.yaml", "state: state", "state: ipam"))
	wantError(t, lines, "Platform/platform", `spec.engines.provisioning.state: Provider "ipam" has type ipam, want postgres or s3`)
}

func TestMonitoring(t *testing.T) {
	m := "    configuration: { tool: ansible, executor: ansible-runner, executionEnvironment: ee:1, source: repo }\n"
	lines := resolveErrors(t, catalogWith(t, "platform.yaml", m,
		m+"  monitoring:\n    provider: state\n    hostGroups: [\"'Gaia/' + nope\"]\n"))
	wantError(t, lines, "Platform/platform", `spec.monitoring.provider: Provider "state" has type`)
	wantError(t, lines, "Platform/platform", "spec.monitoring.hostGroups[0]: ")
}

func TestHypervisorSpecificFields(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t,
		"sites.yaml", "{ provider: pve, nodes: [n1], vmids: { start: 9400, end: 9499 } }", "{ provider: pve, datacenter: DC }",
		"networks.yaml", "{ portgroup: pg120 }", "{ vnet: v2 }",
		"image.yaml", "template: tpl-rocky9", "template: { vmid: 1 }",
	))
	wantError(t, lines, "Site/lab", "spec.compute.nodes: at least one node for Proxmox")
	wantError(t, lines, "Site/lab", "datacenter and folder are vSphere fields")
	wantError(t, lines, "Site/lab", "spec.compute.vmids: the VM ID range Gaia may use is required for Proxmox")
	wantError(t, lines, "Network/ank-app", `Site "ank" is on vSphere: use portgroup`)
	wantError(t, lines, "Image/rocky9", "spec.templates[1].template: vSphere templates are named")
}

func TestImageNeedsATemplateForTheSite(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "image.yaml", "    - { provider: pve, template: { vmid: 9300 } }\n", ""))
	wantError(t, lines, "Server/web", `spec.network: Image "rocky9" has no template for provider "pve" (Site "lab" of Network "lab-app")`)
}

func TestEnvironmentRules(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "server.yaml", "env: test", "env: prod"))
	wantError(t, lines, "Server/web", `spec.network: Network "lab-app" does not allow environment "prod" (test)`)

	lines = resolveErrors(t, catalogWith(t, "server.yaml", "env: test", "env: staging"))
	wantError(t, lines, "Server/web", `environment "staging" is not defined in Policy "default" (prod, test)`)

	lines = resolveErrors(t, catalogWith(t, "server.yaml", ", owner: team-web", ""))
	wantError(t, lines, "Server/web", `label "owner" is required by Policy "default"`)
}

func TestTopologyChecksEveryPlacement(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "server.yaml",
		"  replicas: 2\n", "",
		"server.yaml", "  network: lab-app\n", "  topology:\n    - { network: lab-app, replicas: 1 }\n    - { network: ist-app, replicas: 1 }\n"))
	wantError(t, lines, "Server/web", `spec.topology[1].network: Network "ist-app" not found`)
}

func TestRoleRules(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "server.yaml",
		"    - name: web.nginx\n      params: { server_name: web.example.com }\n",
		"    - name: web.nginx\n      params: { server_nam: x }\n    - web.apache\n"))
	wantError(t, lines, "Server/web", `AnsibleRole "web.nginx" has no param "server_nam" (did you mean "server_name"?)`)
	wantError(t, lines, "Server/web", `AnsibleRole "web.nginx" requires param "server_name"`)
	wantError(t, lines, "Server/web", `AnsibleRole "web.nginx" conflicts with "web.apache"`)
}

func TestOSMustMatch(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "misc.yaml", "{ os: linux, playbook", "{ os: windows, playbook"))
	wantError(t, lines, "Server/web", `spec.baseline: Baseline "linux-base" is for windows, Image "rocky9" is linux`)
}

func TestSingletons(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "platform.yaml", "", "# no platform\n"))
	wantError(t, lines, "no Platform object")

	second := strings.Replace(base["platform.yaml"], "name: platform", "name: platform2", 1)
	lines = resolveErrors(t, catalogWith(t, "zz-platform.yaml", "", second))
	wantError(t, lines, "Platform/platform2", "only one Platform is allowed")
}

func TestLineFallsBackToTheNearestField(t *testing.T) {
	cat, err := Load("c0ffee", catalogWith(t))
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := cat.Get(KindServer, "web")
	for field, want := range map[string]int{
		"spec.ansibleRoles[0]":        13,
		"spec.ansibleRoles[0].params": 14,
		"spec.nothing":                6,
		"spec.ansibleRoles[9]":        12,
	} {
		if got := srv.Line(field); got != want {
			t.Errorf("Line(%s) = %d, want %d", field, got, want)
		}
	}
}

func TestNaming(t *testing.T) {
	fs := catalogWith(t, "policy.yaml", "  requiredLabels:", "  naming: site.name + env.substring(0, 1) + labels.owner.replace(\"team-\", \"\") + pad(seq, 2)\n  requiredLabels:")
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := cat.Get(KindServer, "web")
	if got, err := g.Hostname(srv, "lab", 3); err != nil || got != "labtweb03" {
		t.Errorf("Hostname = %q, %v; want labtweb03", got, err)
	}

	// Without a naming expression the default applies.
	g, err = Resolve(mustLoad(t, catalogWith(t)))
	if err != nil {
		t.Fatal(err)
	}
	srv, _ = g.Catalog.Get(KindServer, "web")
	if got, _ := g.Hostname(srv, "lab", 1); got != "web-01" {
		t.Errorf("default Hostname = %q, want web-01", got)
	}
}

func mustLoad(t *testing.T, fs []File) *Catalog {
	t.Helper()
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestNamingErrors(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t, "policy.yaml", "  requiredLabels:", "  naming: site.name + nosuch\n  requiredLabels:"))
	wantError(t, lines, "policy.yaml:5:", "Policy/default", "spec.naming: column 13: undeclared reference to 'nosuch'")

	lines = resolveErrors(t, catalogWith(t, "policy.yaml", "  requiredLabels:", "  naming: labels.owner + \"_\" + pad(seq, 2)\n  requiredLabels:"))
	wantError(t, lines, "Server/web", `spec.network: naming`, `"team-web_01" is not a valid hostname`)
}

const tomcat = `apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.tomcat }
spec:
  source: { provider: repo, role: corp.web.tomcat }
  params:
    heap_mib: { type: int, default: 1024, gaia: { min: 256, max: 16384 } }
    version: { type: str, required: true, gaia: { format: semver } }
    admin_password: { type: str, no_log: true, gaia: { secret: true } }
    connector: { type: str, default: http, choices: [http, ajp] }
    context: { type: str, default: /, gaia: { pattern: "^/" } }
    users:
      type: list
      elements: dict
      options:
        name: { type: str, required: true }
        roles: { type: list, elements: str }
  rules:
    - rule: params.heap_mib * 4 <= size.memoryGiB * 1024 * 3
      message: heap must stay under 75% of the server's memory
`

func serverWith(roles string) string {
	return strings.Replace(base["server.yaml"],
		"    - name: web.nginx\n      params: { server_name: web.example.com }\n", roles, 1)
}

func TestParamValues(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t,
		"tomcat.yaml", "", tomcat,
		"server.yaml", "", serverWith(`    - name: web.tomcat
      params:
        heap_mib: 100
        version: "2.x"
        admin_password: hunter2
        connector: smtp
        context: app
        users:
          - { name: alice, roles: [admin] }
          - { roles: [7] }
`)))
	wantError(t, lines, "params.heap_mib", "value 100 is less than 256")
	wantError(t, lines, "params.version", `"2.x" is not a valid semver`)
	wantError(t, lines, "params.admin_password", "a secret must be a bao:// reference, not a value")
	wantError(t, lines, "params.connector", "smtp is not one of [http ajp]")
	wantError(t, lines, "params.context", `"app" does not match ^/`)
	wantError(t, lines, "params.users", `users[1]: missing required key "name"`)
	wantError(t, lines, "params.users", "users[1].roles[0]: want a string, got a number")
}

func TestRolesRulesAndValidValues(t *testing.T) {
	good := serverWith(`    - name: web.tomcat
      params: { version: 9.0.1, admin_password: "bao://kv/apps/web/tomcat", heap_mib: 1536 }
`)
	if _, err := Resolve(mustLoad(t, catalogWith(t, "tomcat.yaml", "", tomcat, "server.yaml", "", good))); err != nil {
		t.Fatalf("valid params: %v", err)
	}

	// small is 2 GiB: 1537 MiB is over 75%.
	tooBig := strings.Replace(good, "heap_mib: 1536", "heap_mib: 1537", 1)
	lines := resolveErrors(t, catalogWith(t, "tomcat.yaml", "", tomcat, "server.yaml", "", tooBig))
	wantError(t, lines, "Server/web", `spec.ansibleRoles[0]: AnsibleRole "web.tomcat": heap must stay under 75% of the server's memory`)

	badRule := strings.Replace(tomcat, "params.heap_mib * 4 <=", "params.heap_mib * nosuch <=", 1)
	lines = resolveErrors(t, catalogWith(t, "tomcat.yaml", "", badRule, "server.yaml", "", good))
	wantError(t, lines, "tomcat.yaml:19:", "AnsibleRole/web.tomcat", "spec.rules[0].rule:", "undeclared reference to 'nosuch'")

	// params is dynamic: mixing int and double compiles and fails when run.
	mixed := strings.Replace(tomcat, "params.heap_mib * 4 <=", "params.heap_mib * 4.0 <=", 1)
	lines = resolveErrors(t, catalogWith(t, "tomcat.yaml", "", mixed, "server.yaml", "", good))
	wantError(t, lines, "Server/web", `AnsibleRole "web.tomcat" rule 0: no such overload`)
}

const notifyProviders = `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: slack-web }
spec: { type: slack, credentials: bao://kv/platform/slack-web }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: mail }
spec: { type: smtp, endpoint: "mail:25", from: gaia@example.com }
`

func TestTeamsAndNotifications(t *testing.T) {
	team := `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec:
  displayName: Web
  notifications:
    - { provider: slack-web, triggers: [change.pending, change.failed] }
    - { provider: mail, triggers: [change.applied], to: [web@example.com] }
`
	fs := catalogWith(t, "notify.yaml", "", notifyProviders, "team.yaml", "", team,
		"platform.yaml", "  secrets: bao\n", "  secrets: bao\n  notifications: { email: mail }\n")
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Resolve(cat)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tm := g.Team("team-web"); tm == nil || len(tm.Notifications) != 2 {
		t.Fatalf("team = %+v", tm)
	}
	// Once there are teams, an owner must be one.
	lines := resolveErrors(t, catalogWith(t, "notify.yaml", "", notifyProviders, "team.yaml", "", team, "server.yaml", "owner: team-web", "owner: team-nope"))
	wantError(t, lines, "Server/web", `metadata.labels.owner: Team "team-nope" not found`)

	bad := `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec:
  notifications:
    - { provider: mail, triggers: [change.applied] }
    - { provider: slack-web, triggers: [change.done], to: [a@example.com] }
    - { provider: bao, triggers: [change.failed] }
`
	lines = loadOrResolveErrors(t, catalogWith(t, "notify.yaml", "", notifyProviders, "team.yaml", "", bad))
	wantError(t, lines, "Team/team-web", `"change.done" is unknown`)
}

func TestNotificationChannelRules(t *testing.T) {
	team := `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec:
  notifications:
    - { provider: mail, triggers: [change.applied] }
    - { provider: slack-web, triggers: [change.failed], to: [a@example.com] }
    - { provider: bao, triggers: [change.failed] }
`
	lines := resolveErrors(t, catalogWith(t, "notify.yaml", "", notifyProviders, "team.yaml", "", team))
	wantError(t, lines, "Team/team-web", "spec.notifications[0].to: an smtp channel needs addresses")
	wantError(t, lines, "Team/team-web", "spec.notifications[1].to: only for smtp channels")
	wantError(t, lines, "Team/team-web", `spec.notifications[2].provider: Provider "bao" has type openbao`)

	providers := `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: s }
spec: { type: slack, endpoint: https://hooks.slack.com/x }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: m }
spec: { type: smtp, endpoint: mail }
---
apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: w }
spec: { type: webhook, endpoint: "ftp://x" }
`
	lines = loadOrResolveErrors(t, catalogWith(t, "notify.yaml", "", providers))
	wantError(t, lines, "Provider/s", "credentials: required for type slack")
	wantError(t, lines, "Provider/s", "endpoint: not used for type slack")
	wantError(t, lines, "Provider/m", `endpoint: "mail": want host:port`)
	wantError(t, lines, "Provider/m", `from: "": want an email address`)
	wantError(t, lines, "Provider/w", `endpoint: "ftp://x"`)
}

// loadOrResolveErrors returns the errors of whichever step fails first.
func loadOrResolveErrors(t *testing.T, fs []File) []string {
	t.Helper()
	cat, err := Load("c0ffee", fs)
	if err == nil {
		_, err = Resolve(cat)
	}
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("want errors, got %v", err)
	}
	var out []string
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

func generatedTeam(t *testing.T) string {
	t.Helper()
	b, err := Render(KindTeam, "team-web", nil, &TeamSpec{
		DisplayName: "Web", ADGroup: &ADGroup{Provider: "corp-ad", DN: "CN=SG-Team-Web,OU=Teams,DC=gaia,DC=dev"},
		Secrets: &TeamSecrets{Prefix: "bao://kv/apps/team-web/"},
	}, "TeamSource/corp", "usn-42")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const adProvider = `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: corp-ad }
spec: { type: activedirectory, endpoint: "ldaps://dc1:636", credentials: bao://kv/platform/ad }
`

func TestGeneratedObjectsAndPatches(t *testing.T) {
	team := generatedTeam(t)
	patch := `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web, annotations: { gaia/mode: patch } }
spec:
  displayName: Web Ekibi
  notifications:
    - { provider: slack-web, triggers: [change.failed] }
`
	fs := catalogWith(t, "ad.yaml", "", adProvider, "notify.yaml", "", notifyProviders, "generated/teams/team-web.yaml", "", team, "patches/team-web.yaml", "", patch)
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Resolve(cat)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	tm := g.Team("team-web")
	if tm.DisplayName != "Web Ekibi" || len(tm.Notifications) != 1 || tm.ADGroup.DN == "" || tm.Secrets.Prefix != "bao://kv/apps/team-web/" {
		t.Errorf("merged team = %+v", tm)
	}
	o, _ := cat.Get(KindTeam, "team-web")
	if len(o.Patches) != 1 || o.Patches[0].File != "patches/team-web.yaml" {
		t.Errorf("patches = %+v", o.Patches)
	}

	// An edit by hand shows.
	edited := strings.Replace(team, "displayName: Web", "displayName: Hacked", 1)
	wantError(t, loadErrors(t, catalogWith(t, "ad.yaml", "", adProvider, "generated/teams/team-web.yaml", "", edited)), "edited by hand")

	// A patch may not touch what the source owns, nor patch what is not
	// generated.
	bad := strings.Replace(patch, "  displayName: Web Ekibi\n", "  adGroup: { dn: \"CN=Other\" }\n", 1)
	wantError(t, loadErrors(t, catalogWith(t, "ad.yaml", "", adProvider, "notify.yaml", "", notifyProviders, "generated/teams/team-web.yaml", "", team, "p.yaml", "", bad)),
		"spec.adGroup belongs to TeamSource/corp; a patch may set displayName, secrets, notifications")
	hand := "apiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-web }\nspec: { displayName: Web }\n"
	wantError(t, loadErrors(t, catalogWith(t, "team.yaml", "", hand, "notify.yaml", "", notifyProviders, "p.yaml", "", patch)), "is written by hand")
	wantError(t, loadErrors(t, catalogWith(t, "notify.yaml", "", notifyProviders, "p.yaml", "", patch)), "no generated Team/team-web to patch")
}

func TestTeamSourceChecks(t *testing.T) {
	src := `apiVersion: gaia/v1alpha1
kind: TeamSource
metadata: { name: corp }
spec:
  provider: corp-ad
  select: 'group.dn.endsWith(",OU=Teams,DC=gaia,DC=dev")'
  naming: 'group.cn.lowerAscii().replace("sg-team-", "team-")'
  map: { displayName: group.description, approvers: 'group.cn + "-Approvers"', secretsPrefix: '"bao://kv/apps/" + name + "/"' }
  membership: { recursive: true, sync: { every: 5m } }
  output: generated/teams
`
	cat, err := Load("c0ffee", catalogWith(t, "ad.yaml", "", adProvider, "src.yaml", "", src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(cat); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	bad := strings.Replace(strings.Replace(src, "group.cn.lowerAscii()", "group.cn.lowerAscii() + 1", 1), "provider: corp-ad", "provider: bao", 1)
	lines := resolveErrors(t, catalogWith(t, "ad.yaml", "", adProvider, "src.yaml", "", bad))
	wantError(t, lines, "TeamSource/corp", "spec.naming: ")
	wantError(t, lines, "TeamSource/corp", `spec.provider: Provider "bao" has type openbao`)
	wantError(t, loadErrors(t, catalogWith(t, "src.yaml", "", strings.Replace(src, "every: 5m", "every: 10s", 1))), "at least 1m")
}

const groups = `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-web }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-dba }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Group
metadata: { name: dbas }
spec: { teams: [team-dba], owner: team-dba, privileged: true }
---
apiVersion: gaia/v1alpha1
kind: Group
metadata: { name: web-admins }
spec: { teams: [team-web], groups: [dbas], owner: team-web }
`

func TestGroupsAndAccess(t *testing.T) {
	fs := catalogWith(t, "groups.yaml", "", groups, "server.yaml", "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { group: web-admins, sudo: true }\n    - { team: team-web }\n")
	cat, err := Load("c0ffee", fs)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Resolve(cat)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	teams, privileged := g.GroupTeams("web-admins")
	if strings.Join(teams, ",") != "team-dba,team-web" || !privileged {
		t.Errorf("web-admins = %v %v", teams, privileged)
	}

	cycle := strings.Replace(groups, "spec: { teams: [team-dba], owner: team-dba, privileged: true }", "spec: { teams: [team-dba], groups: [web-admins], owner: team-dba }", 1)
	lines := resolveErrors(t, catalogWith(t, "groups.yaml", "", cycle))
	wantError(t, lines, "Group/dbas", `contains dbas again`)

	bad := catalogWith(t, "groups.yaml", "", groups, "server.yaml", "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { group: nope }\n    - { team: team-web, group: dbas }\n")
	wantError(t, loadOrResolveErrors(t, bad), "Server/web", "access[1]: name a group or a team")
	bad = catalogWith(t, "groups.yaml", "", groups, "server.yaml", "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { group: nope }\n")
	wantError(t, resolveErrors(t, bad), "Server/web", `spec.access[0].group: Group "nope" not found`)
}

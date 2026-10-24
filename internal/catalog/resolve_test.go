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
  compute: { provider: pve, nodes: [n1] }
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
	wantError(t, lines, "Platform/platform", `spec.engines.provisioning.state: Provider "ipam" has type ipam, want s3`)
}

func TestHypervisorSpecificFields(t *testing.T) {
	lines := resolveErrors(t, catalogWith(t,
		"sites.yaml", "{ provider: pve, nodes: [n1] }", "{ provider: pve, datacenter: DC }",
		"networks.yaml", "{ portgroup: pg120 }", "{ vnet: v2 }",
		"image.yaml", "template: tpl-rocky9", "template: { vmid: 1 }",
	))
	wantError(t, lines, "Site/lab", "spec.compute.nodes: at least one node for Proxmox")
	wantError(t, lines, "Site/lab", "datacenter and folder are vSphere fields")
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

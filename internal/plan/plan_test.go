package plan

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// labFiles reads the lab example, with extra files added or replacing.
func labFiles(t *testing.T, extra ...string) []catalog.File {
	t.Helper()
	m := map[string]string{}
	root := filepath.Join("..", "..", "examples", "lab")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		m[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	var out []catalog.File
	for k, v := range m {
		out = append(out, catalog.File{Path: k, Data: []byte(v)})
	}
	return out
}

func build(t *testing.T, names []string, extra ...string) *Plan {
	return buildWith(t, NoState{}, names, extra...)
}

func buildWith(t *testing.T, st State, names []string, extra ...string) *Plan {
	t.Helper()
	p, err := tryBuildWith(t, st, names, extra...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// tryBuild returns the plan's error; catalog errors fail the test.
func tryBuild(t *testing.T, names []string, extra ...string) (*Plan, error) {
	t.Helper()
	return tryBuildWith(t, NoState{}, names, extra...)
}

func tryBuildWith(t *testing.T, st State, names []string, extra ...string) (*Plan, error) {
	t.Helper()
	cat, err := catalog.Load("c0ffee1234567890", labFiles(t, extra...))
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	return Build(context.Background(), g, names, st)
}

const role = `apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.nginx }
spec:
  source: { provider: config-repo, role: corp.web.nginx }
  os: [linux]
  params:
    worker_processes: { type: str, default: auto }
    server_name: { type: str, required: true }
    api_key: { type: str, no_log: true, gaia: { secret: true } }
`

const web = `apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: poc-api
  labels: { app: api, env: test, owner: team-platform }
spec:
  replicas: 3
  image: rocky9
  size: medium
  network: lab-poc
  disks:
    - { sizeGiB: 50, mount: /data }
  baseline: linux-base
  ansibleRoles:
    - name: web.nginx
      params: { server_name: api.lab.test, api_key: "bao://kv/apps/api/key" }
`

func TestPlanLab(t *testing.T) {
	p := build(t, nil)
	if p.Commit != "c0ffee1234567890" || len(p.Contracts) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	c := p.Contracts[0]
	if c.Name != "poc-web" || c.Approval != "none" || len(c.Servers) != 1 {
		t.Fatalf("contract = %+v", c)
	}
	s := c.Servers[0]
	want := Server{
		Action: "create", Hostname: "labtpoc01", Seq: 1, Site: "lab", Network: "lab-poc",
		Compute: Compute{Provider: "pve-lab", Type: "proxmox", Template: "vmid 9300", Customization: "cloud-init",
			Nodes: []string{"proxmox"}, Pool: "gaia", Storage: "local-lvm", Attachment: "vnet gaiapoc"},
		IPAM:       IPAM{Provider: "ipam", Pool: "10.50.0.0/24", Gateway: "10.50.0.1"},
		HostGroups: []string{"Gaia", "Gaia/env/test", "Gaia/app/poc", "Gaia/site/lab", "Gaia/contract/poc-web"},
	}
	if got, w := fmtServer(s), fmtServer(want); got != w {
		t.Errorf("server\n got %s\nwant %s", got, w)
	}
	if c.Baseline == nil || c.Baseline.Playbook != "baseline/linux.yml" {
		t.Errorf("baseline = %+v", c.Baseline)
	}
	if m := c.Monitoring; m == nil || m.Provider != "zabbix" || strings.Join(m.Templates, ",") != "ICMP Ping" {
		t.Errorf("monitoring = %+v", m)
	}
	if !strings.HasPrefix(p.Hash, "sha256:") || len(p.Hash) != len("sha256:")+64 {
		t.Errorf("hash = %q", p.Hash)
	}
}

func fmtServer(s Server) string { return strings.ReplaceAll(strings.TrimSpace(toJSON(s)), "\n", "") }

func TestPlanServersRolesAndMasking(t *testing.T) {
	p := build(t, []string{"poc-api"}, "platform/catalog/role.yaml", role, "contracts/poc-api.yaml", web)
	if len(p.Contracts) != 1 || p.Contracts[0].Name != "poc-api" {
		t.Fatalf("contracts = %+v", p.Contracts)
	}
	c := p.Contracts[0]
	var hosts []string
	for _, s := range c.Servers {
		hosts = append(hosts, s.Hostname)
	}
	if got := strings.Join(hosts, ","); got != "labtapi01,labtapi02,labtapi03" {
		t.Errorf("hostnames = %s", got)
	}
	r := c.Roles[0]
	if r.Params["worker_processes"] != "auto" || r.Params["server_name"] != "api.lab.test" || r.Params["api_key"] != "bao://kv/apps/api/key" {
		t.Errorf("role params = %v", r.Params)
	}
}

func TestPlanHashIsStableAndFollowsInput(t *testing.T) {
	a := build(t, nil)
	b := build(t, nil)
	if a.Hash != b.Hash {
		t.Errorf("same input, hashes %s and %s", a.Hash, b.Hash)
	}
	more := strings.Replace(readLab(t, "contracts/poc-web.yaml"), "replicas: 1", "replicas: 2", 1)
	c := build(t, nil, "contracts/poc-web.yaml", more)
	if c.Hash == a.Hash {
		t.Error("two replicas hash like one")
	}
	// Another commit with the same content plans the same.
	cat, err := catalog.Load("another-commit", labFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := catalog.Resolve(cat)
	d, err := Build(context.Background(), g, nil, NoState{})
	if err != nil || d.Hash != a.Hash || d.Commit != "another-commit" {
		t.Errorf("another commit: %s (%s), %v", d.Hash, d.Commit, err)
	}
}

func TestPlanErrors(t *testing.T) {
	cat, err := catalog.Load("c0ffee", labFiles(t, "platform/catalog/role.yaml", role, "contracts/poc-api.yaml", web,
		// a second contract whose names collide with poc-web
		"contracts/poc-web2.yaml", strings.Replace(readLab(t, "contracts/poc-web.yaml"), "name: poc-web", "name: poc-web2", 1)))
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), g, []string{"nope"}, NoState{}); err == nil || !strings.Contains(err.Error(), `no Server contract "nope"`) {
		t.Errorf("unknown contract: %v", err)
	}
	if _, err := Build(context.Background(), g, nil, NoState{}); err == nil || !strings.Contains(err.Error(), "hostname labtpoc01 is planned for poc-web and poc-web2") {
		t.Errorf("collision: %v", err)
	}
	if _, err := Build(context.Background(), g, []string{"poc-api"}, NoState{}); err != nil {
		t.Errorf("one contract alone: %v", err)
	}
}

func TestDecommissionedPlansNoServers(t *testing.T) {
	gone := readLab(t, "contracts/poc-web.yaml") + "  lifecycle: { state: decommissioned }\n"
	p := build(t, nil, "contracts/poc-web.yaml", gone)
	if n := len(p.Contracts[0].Servers); n != 0 {
		t.Errorf("%d servers planned", n)
	}
}

func TestWriteText(t *testing.T) {
	p := build(t, nil, "platform/catalog/role.yaml", role, "contracts/poc-api.yaml", web)
	var b bytes.Buffer
	if err := WriteText(&b, p); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Server/poc-api  env test  approval none",
		"+ labtapi01",
		"lab / lab-poc · proxmox pve-lab · template vmid 9300 · vnet gaiapoc",
		"medium: 4 vCPU, 8 GiB, 40 GiB disk + 50 GiB /data · IP from 10.50.0.0/24 (ipam)",
		"baseline linux-base (baseline/linux.yml)",
		"role web.nginx (corp.web.nginx) api_key=bao://kv/apps/api/key server_name=api.lab.test worker_processes=auto",
		"ipam       +4 =0 (ipam)",
		"provision  +4 =0 (pve-lab)",
		"monitoring +4 =0 (zabbix)",
		"  monitoring zabbix\n    groups Gaia, Gaia/env/test, Gaia/app/api, Gaia/site/lab, Gaia/contract/poc-api\n    templates ICMP Ping\n",
		"plan " + p.Hash,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMonitoringTemplatesOfRoles(t *testing.T) {
	monitored := strings.Replace(role, "spec:\n", "spec:\n  monitoring: [nginx]\n", 1)
	if _, err := tryBuild(t, nil, "platform/catalog/role.yaml", monitored, "contracts/poc-api.yaml", web); err == nil ||
		!strings.Contains(err.Error(), `role web.nginx: monitoring "nginx" has no templates in the Platform`) {
		t.Errorf("err = %v", err)
	}
	platform := strings.Replace(readLab(t, "platform/platform.yaml"), "linux: [ICMP Ping]", "linux: [ICMP Ping]\n      nginx: [Nginx by Zabbix agent, ICMP Ping]", 1)
	p, err := tryBuild(t, nil, "platform/catalog/role.yaml", monitored, "contracts/poc-api.yaml", web, "platform/platform.yaml", platform)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Contracts[0].Monitoring.Templates, ","); got != "ICMP Ping,Nginx by Zabbix agent" {
		t.Errorf("templates = %s", got)
	}
}

func readLab(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "lab", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func toJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestPlanDNS(t *testing.T) {
	ad := "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: corp-ad }\nspec: { type: activedirectory, endpoint: ldaps://dc1, credentials: bao://kv/ad, writeCredentials: bao://kv/ad-w }\n"
	net := strings.Replace(readLab(t, "platform/catalog/network.yaml"), "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1], provider: corp-ad, zone: lab.test, reverse: true }", 1)
	p := build(t, nil, "platform/providers/corp-ad.yaml", ad, "platform/catalog/network.yaml", net)
	if d := p.Contracts[0].Servers[0].DNS; d == nil || *d != (DNS{Provider: "corp-ad", Zone: "lab.test", Reverse: true, TTL: 3600}) {
		t.Errorf("DNS = %+v", d)
	}
	var b bytes.Buffer
	if err := WriteText(&b, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dns labtpoc01.lab.test A and PTR (corp-ad)", "dns        +1 =0 (corp-ad)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
}

func TestPlanDomain(t *testing.T) {
	ad := "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: corp-ad }\nspec: { type: activedirectory, endpoint: ldaps://dc1, credentials: bao://kv/ad, writeCredentials: bao://kv/ad-w }\n"
	net := strings.Replace(readLab(t, "platform/catalog/network.yaml"), "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1] }\n  domain: { provider: corp-ad, ou: \"OU=Servers,DC=lab,DC=test\" }", 1)
	p := build(t, nil, "platform/providers/corp-ad.yaml", ad, "platform/catalog/network.yaml", net)
	if d := p.Contracts[0].Servers[0].Domain; d == nil || *d != (Domain{Provider: "corp-ad", OU: "OU=Servers,DC=lab,DC=test"}) {
		t.Errorf("domain = %+v", d)
	}
	var b bytes.Buffer
	_ = WriteText(&b, p)
	for _, want := range []string{"computer account in OU=Servers,DC=lab,DC=test (corp-ad)", "computers  +1 =0 (corp-ad)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	policy := strings.Replace(readLab(t, "platform/policy.yaml"), "pad(seq, 2)", "'-long-name' + pad(seq, 2)", 1)
	if _, err := tryBuild(t, nil, "platform/providers/corp-ad.yaml", ad, "platform/catalog/network.yaml", net, "platform/policy.yaml", policy); err == nil ||
		!strings.Contains(err.Error(), "hostname labtpoc-long-name01 is longer than 15 characters") {
		t.Errorf("long name: %v", err)
	}
}

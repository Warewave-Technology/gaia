package catalog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func files(kv ...string) []File {
	var out []File
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, File{Path: kv[i], Data: []byte(kv[i+1])})
	}
	return out
}

// loadErrors loads and returns the error lines; it fails the test if the
// load succeeds.
func loadErrors(t *testing.T, fs []File) []string {
	t.Helper()
	_, err := Load("c0ffee", fs)
	var errs Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Load succeeded, want errors")
	}
	return strings.Split(errs.Error(), "\n")
}

func wantError(t *testing.T, lines []string, parts ...string) {
	t.Helper()
	for _, l := range lines {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("no error with %q in:\n%s", parts, strings.Join(lines, "\n"))
}

func TestLoadLabExamples(t *testing.T) {
	root := filepath.Join("..", "..", "examples", "lab")
	var fsFiles []File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		fsFiles = append(fsFiles, File{Path: filepath.ToSlash(rel), Data: data})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	cat, err := Load("c0ffee", fsFiles)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]int{
		KindPlatform: 1, KindPolicy: 1, KindProvider: 7, KindSite: 1, KindNetwork: 1,
		KindImage: 1, KindSize: 2, KindBaseline: 1, KindServer: 1,
	}
	total := 0
	for kind, n := range want {
		if got := len(cat.List(kind)); got != n {
			t.Errorf("%s: %d objects, want %d", kind, got, n)
		}
		total += n
	}
	if cat.Len() != total {
		t.Errorf("Len = %d, want %d", cat.Len(), total)
	}

	net, ok := cat.Get(KindNetwork, "lab-poc")
	if !ok {
		t.Fatal("Network/lab-poc missing")
	}
	ns := net.Spec.(*NetworkSpec)
	if ns.Attachment.VNet != "gaiapoc" || ns.IPAM.Pool != "10.50.0.0/24" || net.Metadata.Labels["site"] != "lab" {
		t.Errorf("Network spec = %+v, labels %v", ns, net.Metadata.Labels)
	}
	if net.Source.File != "platform/catalog/network.yaml" || net.Source.Line != 1 {
		t.Errorf("Source = %v", net.Source)
	}
	img, _ := cat.Get(KindImage, "rocky9")
	if got := img.Spec.(*ImageSpec).Templates[0].Template.VMID; got != 9300 {
		t.Errorf("template vmid = %d, want 9300", got)
	}
	// Second document of a multi-document file.
	if _, ok := cat.Get(KindProvider, "run-records"); !ok {
		t.Error("Provider/run-records (second document in s3.yaml) missing")
	}
	srv, _ := cat.Get(KindServer, "poc-web")
	if r := srv.Spec.(*ServerSpec).Replicas; r == nil || *r != 1 {
		t.Errorf("replicas = %v, want 1", r)
	}
}

const network = `apiVersion: gaia/v1alpha1
kind: Network
metadata:
  name: lab-poc
spec:
  site: lab
  attachment: { vnet: gaiapoc }
  ipam: { provider: ipam, pool: 10.50.0.0/24 }
`

func TestUnknownFieldHasPositionAndPath(t *testing.T) {
	bad := strings.Replace(network, "{ vnet: gaiapoc }", "{ vnett: gaiapoc }", 1)
	lines := loadErrors(t, files("n.yaml", bad))
	wantError(t, lines, "n.yaml:7:", "Network/lab-poc", `spec.attachment: unknown field "vnett"`)
}

func TestMissingRequiredField(t *testing.T) {
	bad := strings.Replace(network, "  site: lab\n", "", 1)
	lines := loadErrors(t, files("n.yaml", bad))
	wantError(t, lines, "n.yaml:6:3:", `spec: missing required field "site"`)
}

func TestTypeErrorKeepsLine(t *testing.T) {
	lines := loadErrors(t, files("s.yaml", `apiVersion: gaia/v1alpha1
kind: Size
metadata: { name: small }
spec:
  cpu: two
  memoryGiB: 2
  diskGiB: 20
`))
	wantError(t, lines, "s.yaml:5:", "Size/small", "cannot unmarshal")
}

func TestDuplicateAcrossFiles(t *testing.T) {
	lines := loadErrors(t, files("a/n.yaml", network, "b/n.yaml", network))
	wantError(t, lines, "b/n.yaml:1:", "Network/lab-poc", "defined more than once (also at a/n.yaml:1)")
}

func TestEnvelopeErrors(t *testing.T) {
	lines := loadErrors(t, files("x.yaml", `apiVersion: gaia/v2
kind: Gadget
metadata: { name: Bad_Name }
spec: {}
---
kind: Size
metadata: { name: small }
spec: { cpu: 1, memoryGiB: 1, diskGiB: 1 }
`))
	wantError(t, lines, "x.yaml:1:", `apiVersion "gaia/v2" is not supported`)
	wantError(t, lines, "x.yaml:1:", `unknown kind "Gadget"`)
	wantError(t, lines, "x.yaml:1:", `metadata.name "Bad_Name"`)
	wantError(t, lines, "x.yaml:6:", `missing required field "apiVersion"`)
}

func TestEveryProblemIsReported(t *testing.T) {
	a := strings.Replace(network, "  site: lab\n", "", 1)
	b := strings.Replace(network, "lab-poc", "other", 1)
	b = strings.Replace(b, "{ vnet: gaiapoc }", "{ vnet: gaiapoc, portgroup: pg1 }", 1)
	lines := loadErrors(t, files("a.yaml", a, "b.yaml", b))
	wantError(t, lines, "a.yaml:", `missing required field "site"`)
	wantError(t, lines, "b.yaml:", "Network/other", "spec.attachment: exactly one of portgroup, bridge or vnet")
}

func TestKindValidation(t *testing.T) {
	lines := loadErrors(t, files(
		"server.yaml", `apiVersion: gaia/v1alpha1
kind: Server
metadata: { name: web }
spec:
  image: rocky9
  size: small
  network: lab-poc
  topology:
    - { network: lab-poc, replicas: 1 }
`,
		"role.yaml", `apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.nginx }
spec:
  source: { provider: config-repo, role: corp.web.nginx }
  params:
    raw_thing: { type: raw }
    vhosts: { type: dict }
    hosts: { type: list }
    token: { type: int, gaia: { secret: true } }
    open: { type: dict, gaia: { freeform: true } }
`,
		"provider.yaml", `apiVersion: gaia/v1alpha1
kind: Provider
metadata: { name: state }
spec: { type: s3, endpoint: http://s3, credentials: kv/plain }
`))
	wantError(t, lines, "Server/web", "network and topology: use one of them")
	wantError(t, lines, "AnsibleRole/web.nginx", `params.raw_thing.type: "raw" is not allowed`)
	wantError(t, lines, "AnsibleRole/web.nginx", "params.vhosts: dict needs options")
	wantError(t, lines, "AnsibleRole/web.nginx", "params.hosts: list needs elements")
	wantError(t, lines, "AnsibleRole/web.nginx", "params.token: a secret param must be str")
	wantError(t, lines, "Provider/state", "bucket: required for type s3")
	wantError(t, lines, "Provider/state", "credentials: must be a bao:// reference")
	for _, l := range lines {
		if strings.Contains(l, "params.open") {
			t.Errorf("freeform dict rejected: %s", l)
		}
	}
}

func TestRoleRefForms(t *testing.T) {
	srv := `apiVersion: gaia/v1alpha1
kind: Server
metadata: { name: web }
spec:
  image: rocky9
  size: small
  network: lab-poc
  ansibleRoles:
    - web.nginx
    - name: web.tomcat
      params: { tomcat_heap_mib: 4096 }
`
	cat, err := Load("c0ffee", files("s.yaml", srv))
	if err != nil {
		t.Fatal(err)
	}
	o, _ := cat.Get(KindServer, "web")
	roles := o.Spec.(*ServerSpec).AnsibleRoles
	if len(roles) != 2 || roles[0].Name != "web.nginx" || roles[1].Name != "web.tomcat" || roles[1].Params["tomcat_heap_mib"] != 4096 {
		t.Errorf("roles = %+v", roles)
	}

	bad := strings.Replace(srv, "params: {", "parms: {", 1)
	wantError(t, loadErrors(t, files("s.yaml", bad)), "s.yaml:11:", `unknown field "parms"`)
}

func TestSkipsOtherFilesAndEmptyDocuments(t *testing.T) {
	cat, err := Load("c0ffee", files(
		"README.md", "not yaml",
		".github/workflows/ci.yml", "on: push",
		"n.yaml", "---\n# only a comment\n---\n"+network+"---\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if cat.Len() != 1 || cat.Commit != "c0ffee" {
		t.Errorf("Len = %d, Commit = %q", cat.Len(), cat.Commit)
	}
}

func TestParseError(t *testing.T) {
	lines := loadErrors(t, files("bad.yaml", "kind: Size\nmetadata: [unclosed\n"))
	wantError(t, lines, "bad.yaml:")
}

func TestPatchNotSupportedYet(t *testing.T) {
	p := strings.Replace(network, "  name: lab-poc\n", "  name: lab-poc\n  annotations: { gaia/mode: patch }\n", 1)
	wantError(t, loadErrors(t, files("p.yaml", p)), "patch objects")
}

package apply

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

type fakeSecrets map[string]map[string]string

func (f fakeSecrets) Read(_ context.Context, ref string) (map[string]string, error) {
	if v, ok := f[ref]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%s: no such secret", ref)
}
func (fakeSecrets) Addr() string  { return "http://bao:8200" }
func (fakeSecrets) Token() string { return "bao-token" }

// fakeCompute records stacks and answers with the VM IDs it was given.
type fakeCompute struct {
	stacks []pulumi.Stack
	noIP   bool // the guest never reports its address
}

func (f *fakeCompute) provision(_ context.Context, b pulumi.Backend, st pulumi.Stack, _ io.Writer) (map[string]pulumi.Result, pulumi.Changes, error) {
	if b.URL != "postgres://gaia_owner:pw@192.168.1.25:5432/pulumi?sslmode=disable" || b.Env["VAULT_TOKEN"] != "bao-token" {
		return nil, nil, fmt.Errorf("backend = %+v", b)
	}
	f.stacks = append(f.stacks, st)
	res := map[string]pulumi.Result{}
	for _, v := range st.VMs {
		ip := strings.SplitN(v.Address, "/", 2)[0]
		if f.noIP {
			ip = ""
		}
		res[v.UID] = pulumi.Result{VMID: v.VMID, IPv4: []string{"127.0.0.1", ip}}
	}
	return res, pulumi.Changes{"create": len(st.VMs)}, nil
}

type env struct {
	t       *testing.T
	store   *store.Store
	ipam    string // endpoint
	compute *fakeCompute
	deps    Deps
}

func setup(t *testing.T) *env {
	t.Helper()
	itest.Require(t)
	ctx := context.Background()

	gaiaDSN := itest.StartPostgres(t)
	if err := store.Migrate(ctx, gaiaDSN); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, gaiaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	ipamDSN := itest.StartPostgres(t)
	if err := ipam.Migrate(ctx, ipamDSN); err != nil {
		t.Fatal(err)
	}
	srv, err := ipam.NewServer(ctx, ipamDSN, "ipam-token", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	if err := ipamclient.New(hs.URL, "ipam-token").SetPool(ctx, ipam.Pool{CIDR: "10.50.0.0/24", Gateway: "10.50.0.1", RangeStart: "10.50.0.100", RangeEnd: "10.50.0.199"}); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, store: s, ipam: hs.URL, compute: &fakeCompute{}}
	e.deps = Deps{
		Store: s,
		Secrets: fakeSecrets{
			"bao://kv/platform/pulumi-state": {"username": "gaia_owner", "password": "pw"},
			"bao://kv/platform/ssh":          {"public": "ssh-ed25519 AAAA gaia@lab\n", "private": "x"},
			"bao://kv/platform/ipam":         {"token": "ipam-token"},
			"bao://kv/platform/proxmox/lab":  {"token_id": "gaia@pve!poc", "token_secret": "pve", "insecure": "true"},
		},
		Provision: e.compute.provision,
		UsedVMIDs: func(context.Context, proxmox.Credentials) (map[int]bool, error) {
			return map[int]bool{9300: true, 9400: true}, nil
		},
		Out: io.Discard,
	}
	return e
}

// graph is the lab example with IPAM pointing at the test server and the
// contract changed by edit.
func (e *env) graph(edit func(string) string) *catalog.Graph {
	t := e.t
	t.Helper()
	root := filepath.Join("..", "..", "examples", "lab")
	var files []catalog.File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		content := string(data)
		switch rel {
		case "platform/providers/ipam.yaml":
			content = regexp.MustCompile(`endpoint: \S+`).ReplaceAllString(content, "endpoint: "+e.ipam)
		case "contracts/poc-web.yaml":
			if edit != nil {
				content = edit(content)
			}
		}
		files = append(files, catalog.File{Path: rel, Data: []byte(content)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load("c0ffee", files)
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (e *env) apply(edit func(string) string) (*plan.Plan, error) {
	e.t.Helper()
	ctx := context.Background()
	g := e.graph(edit)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		e.t.Fatal(err)
	}
	_, err = Run(ctx, g, p, e.deps)
	return p, err
}

func TestApplyCreatesThenKeeps(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()

	p, err := e.apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Contracts[0].Servers[0].Action; a != plan.Create {
		t.Fatalf("first plan: %s", a)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	if len(hosts) != 1 || hosts[0].Hostname != "labtpoc01" || hosts[0].Status != db.HostStatusActive || hosts[0].Network != "lab-poc" {
		t.Fatalf("hosts = %+v", hosts)
	}
	uid := hosts[0].Uid
	ids := map[string]string{}
	got, _ := q.ExternalIDs(ctx, uid)
	for _, x := range got {
		ids[x.Provider+"/"+x.Kind] = x.ExternalID
	}
	want := map[string]string{"ipam/ip": "10.50.0.100", "pve-lab/vm": "9401", "pve-lab/smbios": SMBIOSUUID(uid)}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("external IDs = %v, want %v", ids, want)
	}

	if len(e.compute.stacks) != 1 {
		t.Fatalf("%d stacks", len(e.compute.stacks))
	}
	st := e.compute.stacks[0]
	vm := st.VMs[0]
	if st.Name != "poc-web-lab" || st.Proxmox.Endpoint != "https://192.168.1.99:8006" || st.Proxmox.APIToken() != "gaia@pve!poc=pve" {
		t.Errorf("stack = %+v", st)
	}
	wantVM := pulumi.VM{
		UID: uid, Hostname: "labtpoc01", VMID: 9401, Node: "proxmox", Pool: "gaia", Storage: "local-lvm",
		Template: 9300, CPU: 2, MemoryMiB: 2048, DiskGiB: 20, Bridge: "gaiapoc",
		Address: "10.50.0.100/24", Gateway: "10.50.0.1", DNS: []string{"1.1.1.1"}, User: "gaia",
		SSHKeys:     []string{"ssh-ed25519 AAAA gaia@lab"},
		Tags:        []string{"gaia", "gaia-uid_" + uid, "contract_poc-web", "app_poc", "env_test", "owner_team-platform"},
		Description: "Managed by Gaia.\ngaia.uid=" + uid + "\ncontract=poc-web\n",
		SMBIOSUUID:  SMBIOSUUID(uid),
	}
	if !reflect.DeepEqual(vm, wantVM) {
		t.Errorf("VM\n got %+v\nwant %+v", vm, wantVM)
	}

	// The second apply keeps the server and asks for exactly the same VM.
	p, err = e.apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Contracts[0].Servers[0]; s.Action != plan.Keep || s.UID != uid {
		t.Errorf("second plan: %+v", s)
	}
	if len(e.compute.stacks) != 2 || !reflect.DeepEqual(e.compute.stacks[1].VMs, st.VMs) {
		t.Errorf("second apply changed the VM:\n%+v\n%+v", e.compute.stacks[1].VMs, st.VMs)
	}
	if hosts, _ := q.LiveHosts(ctx, "poc-web"); len(hosts) != 1 {
		t.Errorf("hosts after second apply: %+v", hosts)
	}

	// Scaling up adds the next name, address and VM ID.
	if _, err := e.apply(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) }); err != nil {
		t.Fatal(err)
	}
	last := e.compute.stacks[len(e.compute.stacks)-1].VMs
	if len(last) != 2 || last[1].Hostname != "labtpoc02" || last[1].Address != "10.50.0.101/24" || last[1].VMID != 9402 {
		t.Errorf("scaled up: %+v", last)
	}
}

func TestApplyRefusesDeletesForNow(t *testing.T) {
	e := setup(t)
	if _, err := e.apply(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.apply(nil); err == nil || !strings.Contains(err.Error(), "the plan deletes labtpoc02: decommission is not supported yet") {
		t.Errorf("err = %v", err)
	}
}

func TestFailedRunIsRecorded(t *testing.T) {
	e := setup(t)
	e.deps.Provision = func(context.Context, pulumi.Backend, pulumi.Stack, io.Writer) (map[string]pulumi.Result, pulumi.Changes, error) {
		return nil, nil, fmt.Errorf("proxmox is down")
	}
	ctx := context.Background()
	g := e.graph(nil)
	p, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	run, err := Run(ctx, g, p, e.deps)
	if err == nil || !strings.Contains(err.Error(), "proxmox is down") {
		t.Fatalf("err = %v", err)
	}
	r, _ := e.store.Q().Run(ctx, run)
	if r.Status != "failed" || !strings.Contains(r.Error.String, "proxmox is down") || r.PlanHash != p.Hash {
		t.Errorf("run = %+v", r)
	}
	// The host stays in provisioning; the next plan keeps it and retries.
	h, _ := e.store.Q().LiveHosts(ctx, "poc-web")
	if len(h) != 1 || h[0].Status != db.HostStatusProvisioning {
		t.Errorf("hosts = %+v", h)
	}
	p2, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if s := p2.Contracts[0].Servers[0]; s.Action != plan.Keep || s.Status != "provisioning" {
		t.Errorf("retry plan: %+v", s)
	}
}

func TestTagsAndSMBIOS(t *testing.T) {
	got := Tags("abc", "Poc Web", map[string]string{"owner": "Team:Web", "env": "test"})
	want := []string{"gaia", "gaia-uid_abc", "contract_poc-web", "env_test", "owner_team-web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tags = %v", got)
	}
	u := SMBIOSUUID("abc")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) || u != SMBIOSUUID("abc") || u == SMBIOSUUID("abd") {
		t.Errorf("SMBIOSUUID = %s", u)
	}
}

func TestAVMWithoutItsAddressIsNotActive(t *testing.T) {
	e := setup(t)
	e.compute.noIP = true
	_, err := e.apply(nil)
	if err == nil || !strings.Contains(err.Error(), "not 10.50.0.100; the VM is not configured") {
		t.Fatalf("err = %v", err)
	}
	h, _ := e.store.Q().LiveHosts(context.Background(), "poc-web")
	if len(h) != 1 || h[0].Status != db.HostStatusProvisioning {
		t.Errorf("hosts = %+v", h)
	}
}

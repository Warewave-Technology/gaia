package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/zabbix"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
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
	noIP   bool           // the guest never reports its address
	vms    map[string]int // stack -> VM IDs it holds, as a set key "stack/id"
}

// used is the cluster's VM IDs: the template and every stack's VMs.
func (f *fakeCompute) used(context.Context, proxmox.Credentials) (map[int]bool, error) {
	u := map[int]bool{9300: true}
	for _, id := range f.vms {
		u[id] = true
	}
	return u, nil
}

func (f *fakeCompute) provision(_ context.Context, b pulumi.Backend, st pulumi.Stack, _ io.Writer) (map[string]pulumi.Result, pulumi.Changes, error) {
	if b.URL != "postgres://gaia_owner:pw@192.168.1.25:5432/pulumi?sslmode=disable" || b.Env["VAULT_TOKEN"] != "bao-token" {
		return nil, nil, fmt.Errorf("backend = %+v", b)
	}
	f.stacks = append(f.stacks, st)
	if f.vms == nil {
		f.vms = map[string]int{}
	}
	for k := range f.vms {
		if strings.HasPrefix(k, st.Name+"/") {
			delete(f.vms, k)
		}
	}
	for _, v := range st.VMs {
		f.vms[st.Name+"/"+v.UID] = v.VMID
	}
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

// fakeConfigurer records jobs, with the project as it was written, and
// sends each host one changed task and a recap.
type fakeConfigurer struct {
	jobs     []ansible.Job
	projects []map[string]string // path -> content
	fail     string              // UID whose task fails
}

func (f *fakeConfigurer) Image(_ context.Context, ref string) (string, error) {
	return ref + "@sha256:ee", nil
}

func (f *fakeConfigurer) Run(_ context.Context, j ansible.Job, on func(ansible.Event) error) (ansible.Result, error) {
	f.jobs = append(f.jobs, j)
	dir, err := os.MkdirTemp("", "project-")
	if err != nil {
		return ansible.Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := j.Project(dir); err != nil {
		return ansible.Result{}, err
	}
	files := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			files[filepath.ToSlash(rel)] = string(data)
		}
		return err
	})
	f.projects = append(f.projects, files)

	res := ansible.Result{Stats: ansible.Stats{OK: map[string]int{}, Changed: map[string]int{}, Failures: map[string]int{}, Unreachable: map[string]int{}}}
	for _, h := range j.Hosts {
		e := ansible.Event{Type: "runner_on_ok", Host: h.Name, Task: "Hostname", Changed: true, Stdout: "changed: [" + h.Name + "]", Data: []byte(`{"host":"` + h.Name + `"}`)}
		if h.Name == f.fail {
			e.Type, e.Changed, e.Failed = "runner_on_failed", false, true
			res.Stats.Failures[h.Name] = 1
			res.RC = 2
		} else {
			res.Stats.OK[h.Name], res.Stats.Changed[h.Name] = 1, 1
		}
		if err := on(e); err != nil {
			return res, err
		}
	}
	return res, on(ansible.Event{Type: "playbook_on_stats", Data: []byte(`{}`)})
}

// fakeMonitor keeps hosts by UID.
type fakeMonitor struct {
	endpoint, token string
	hosts           map[string]zabbix.Host
	ids             map[string]string
	deleted         []string
	userGroups      map[string]userGroup
	maintenances    []string // "start name ids" and "end id"
}

func (f *fakeMonitor) Maintenance(_ context.Context, name string, ids []string, _ time.Time) (string, error) {
	id := fmt.Sprintf("m%d", len(f.maintenances))
	f.maintenances = append(f.maintenances, "start "+name+" "+strings.Join(ids, ","))
	return id, nil
}

func (f *fakeMonitor) EndMaintenance(_ context.Context, id string) error {
	f.maintenances = append(f.maintenances, "end "+id)
	return nil
}

type userGroup struct {
	rights map[string]int
	users  []string
}

func (f *fakeMonitor) UserGroup(_ context.Context, name string, rights map[string]int, users []string) ([]string, error) {
	if f.userGroups == nil {
		f.userGroups = map[string]userGroup{}
	}
	f.userGroups[name] = userGroup{rights: rights, users: users}
	return nil, nil
}

func (f *fakeMonitor) Register(_ context.Context, h zabbix.Host, hostID string) (zabbix.Result, error) {
	id, ok := f.ids[h.UID]
	switch {
	case ok && hostID != "" && hostID != id:
		return zabbix.Result{}, fmt.Errorf("host ID %s, want %s", hostID, id)
	case !ok:
		id = fmt.Sprintf("%d", 10000+len(f.ids))
		f.ids[h.UID], f.hosts[h.UID] = id, h
		return zabbix.Result{HostID: id, Action: "create"}, nil
	case !reflect.DeepEqual(f.hosts[h.UID], h):
		f.hosts[h.UID] = h
		return zabbix.Result{HostID: id, Action: "update"}, nil
	}
	return zabbix.Result{HostID: id, Action: "same"}, nil
}

func (f *fakeMonitor) Delete(_ context.Context, uid, _ string) (bool, error) {
	if _, ok := f.hosts[uid]; !ok {
		return false, nil
	}
	delete(f.hosts, uid)
	f.deleted = append(f.deleted, uid)
	return true, nil
}

// memArchive keeps archived records.
type memArchive struct {
	at      string // endpoint/bucket it was opened for
	objects map[string][]byte
	until   map[string]time.Time
}

func (m *memArchive) Put(_ context.Context, key string, data []byte, until time.Time) (string, error) {
	m.objects[key], m.until[key] = data, until
	return "mem://" + key, nil
}

type env struct {
	t         *testing.T
	store     *store.Store
	ipam      string // endpoint
	config    string // config repository
	compute   *fakeCompute
	configure *fakeConfigurer
	monitor   *fakeMonitor
	archive   *memArchive
	deps      Deps
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

	cr := gittest.New(t)
	cr.Write("ansible/baseline/linux.yml", "- hosts: all", "ansible/roles/gaia_linux_base/tasks/main.yml", "- ping: {}")
	cr.Commit("config")

	e := &env{t: t, store: s, ipam: hs.URL, config: cr.Dir, compute: &fakeCompute{vms: map[string]int{"not-gaia/vm": 9400}}, configure: &fakeConfigurer{},
		monitor: &fakeMonitor{hosts: map[string]zabbix.Host{}, ids: map[string]string{}},
		archive: &memArchive{objects: map[string][]byte{}, until: map[string]time.Time{}}}
	e.deps = Deps{
		Store: s,
		Secrets: fakeSecrets{
			"bao://kv/platform/pulumi-state": {"username": "gaia_owner", "password": "pw"},
			"bao://kv/platform/ssh":          {"public": "ssh-ed25519 AAAA gaia@lab\n", "private": "private-key"},
			"bao://kv/platform/ipam":         {"token": "ipam-token"},
			"bao://kv/platform/proxmox/lab":  {"token_id": "gaia@pve!poc", "token_secret": "pve", "insecure": "true"},
			"bao://kv/platform/zabbix":       {"token": "zbx"},
			"bao://kv/platform/seaweedfs":    {"access_key": "ak", "secret_key": "sk"},
		},
		Provision: e.compute.provision,
		UsedVMIDs: e.compute.used,
		Configure: e.configure,
		Monitor: func(endpoint, token string) Monitor {
			e.monitor.endpoint, e.monitor.token = endpoint, token
			return e.monitor
		},
		Archive: func(endpoint, bucket, ak, sk string) (runrecord.Archive, error) {
			if ak != "ak" || sk != "sk" {
				return nil, fmt.Errorf("credentials %s/%s", ak, sk)
			}
			e.archive.at = endpoint + "/" + bucket
			return e.archive, nil
		},
		RequestedBy: "ops@lab",
		Out:         io.Discard,
	}
	return e
}

// graph is the lab example with IPAM pointing at the test server and the
// contract changed by edit.
func (e *env) files(edit func(string) string) []catalog.File {
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
		case "platform/providers/config-repo.yaml":
			content = regexp.MustCompile(`url: \S+`).ReplaceAllString(content, "url: "+e.config)
			content = strings.Replace(content, "ref: main", "ref: HEAD", 1)
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
	return files
}

func (e *env) graph(edit func(string) string) *catalog.Graph {
	t := e.t
	t.Helper()
	cat, err := catalog.Load("c0ffee", e.files(edit))
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// graphErr is graph for catalogs expected not to load or resolve.
func (e *env) graphErr(edit func(string) string) error {
	e.t.Helper()
	files := e.files(edit)
	cat, err := catalog.Load("c0ffee", files)
	if err != nil {
		return err
	}
	_, err = catalog.Resolve(cat)
	return err
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
	want := map[string]string{"ipam/ip": "10.50.0.100", "pve-lab/vm": "9401", "pve-lab/smbios": SMBIOSUUID(uid), "zabbix/host": "10000"}
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

func TestDecommission(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	replicas := func(n string) func(string) string {
		return func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: "+n, 1) }
	}
	if _, err := e.apply(replicas("2")); err != nil {
		t.Fatal(err)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	first, second := hosts[0].Uid, hosts[1].Uid

	// Scaling down deletes the highest seq: its Zabbix host, VM, address
	// and name.
	p, err := e.apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Contracts[0].Servers; len(s) != 2 || s[1].Action != plan.Delete || s[1].Hostname != "labtpoc02" {
		t.Fatalf("plan = %+v", s)
	}
	if last := e.compute.stacks[len(e.compute.stacks)-1]; len(last.VMs) != 1 || last.VMs[0].UID != first {
		t.Errorf("stack after scale down: %+v", last.VMs)
	}
	if strings.Join(e.monitor.deleted, ",") != second {
		t.Errorf("zabbix deleted %v", e.monitor.deleted)
	}
	gone, _ := q.Host(ctx, second)
	if gone.Status != db.HostStatusDeleted {
		t.Errorf("host = %+v", gone)
	}
	if ids, _ := q.ExternalIDs(ctx, second); len(ids) != 0 {
		t.Errorf("external IDs left: %+v", ids)
	}
	if holder, err := q.NameHolder(ctx, "labtpoc02"); err == nil {
		t.Errorf("labtpoc02 still held by %s", holder)
	}
	if a, _ := ipamclient.New(e.ipam, "ipam-token").Allocations(ctx, second); len(a) != 0 {
		t.Errorf("allocations left: %+v", a)
	}

	// replicas: 0 empties the stack.
	if _, err := e.apply(replicas("0")); err != nil {
		t.Fatal(err)
	}
	if last := e.compute.stacks[len(e.compute.stacks)-1]; last.Name != "poc-web-lab" || len(last.VMs) != 0 {
		t.Errorf("stack after replicas 0: %+v", last)
	}
	if live, _ := q.LiveHosts(ctx, "poc-web"); len(live) != 0 {
		t.Errorf("live hosts: %+v", live)
	}

	// The name and the VM ID come back; the address is in quarantine.
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	live, _ := q.LiveHosts(ctx, "poc-web")
	if len(live) != 1 || live[0].Hostname != "labtpoc01" || live[0].Uid == first {
		t.Fatalf("recreated: %+v", live)
	}
	vm := e.compute.stacks[len(e.compute.stacks)-1].VMs[0]
	if vm.VMID != 9401 || vm.Address != "10.50.0.102/24" {
		t.Errorf("recreated VM %d at %s", vm.VMID, vm.Address)
	}
}

func TestDecommissionedContract(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	p, err := e.apply(func(c string) string { return c + "  lifecycle: { state: decommissioned }\n" })
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Contracts[0].Servers; len(s) != 1 || s[0].Action != plan.Delete {
		t.Fatalf("plan = %+v", s)
	}
	if live, _ := e.store.Q().LiveHosts(ctx, "poc-web"); len(live) != 0 {
		t.Errorf("live hosts: %+v", live)
	}
	if n := len(e.configure.jobs); n != 1 {
		t.Errorf("%d configure jobs; the decommission ran one", n)
	}
}

func TestApprovalIsRefused(t *testing.T) {
	p := &plan.Plan{Contracts: []plan.Contract{{Name: "prod-web", Approval: "required", Servers: []plan.Server{{Action: plan.Create, Hostname: "x"}}}}}
	if _, err := Run(context.Background(), nil, p, Deps{}); err == nil || !strings.Contains(err.Error(), "needs approval") {
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

func TestApplyConfiguresServers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	g := e.graph(nil)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Run(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid

	if len(e.configure.jobs) != 1 {
		t.Fatalf("%d jobs", len(e.configure.jobs))
	}
	j := e.configure.jobs[0]
	if j.Image != "localhost/gaia/ee-default:dev@sha256:ee" || j.Playbook != ".gaia/site.yml" || j.User != "gaia" ||
		j.SSHKey != "private-key" || j.Forks != 10 || j.Timeout != time.Hour || j.Name != "gaia-"+run+"-poc-web" {
		t.Errorf("job = %+v", j)
	}
	wantHost := ansible.Host{Name: uid, Address: "10.50.0.100", Vars: map[string]any{
		"gaia_uid": uid, "gaia_hostname": "labtpoc01", "gaia_contract": "poc-web", "gaia_site": "lab", "gaia_env": "test",
		"gaia_labels": map[string]string{"app": "poc", "env": "test", "owner": "team-platform"},
		"gaia_ntp":    []string{"pool.ntp.org"}, "gaia_dns": []string{"1.1.1.1"},
	}}
	if len(j.Hosts) != 1 || !reflect.DeepEqual(j.Hosts[0], wantHost) {
		t.Errorf("hosts\n got %+v\nwant %+v", j.Hosts, wantHost)
	}
	proj := e.configure.projects[0]
	if proj["baseline/linux.yml"] != "- hosts: all" || proj["roles/gaia_linux_base/tasks/main.yml"] == "" {
		t.Errorf("project = %v", proj)
	}
	if !strings.Contains(proj[".gaia/site.yml"], `"import_playbook": "../baseline/linux.yml"`) {
		t.Errorf("site.yml = %s", proj[".gaia/site.yml"])
	}

	all, err := q.RunEvents(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	evs := ansibleEvents(all)
	if len(evs) != 2 || evs[0].HostUid.String != uid || evs[0].Stage != "configure" || !evs[0].Changed || evs[1].Event != "playbook_on_stats" || evs[1].HostUid.Valid {
		t.Errorf("events = %+v", evs)
	}
	// Output lines are events of their stage, in order with the rest.
	var stages []string
	for i, e := range all {
		if int(e.Seq) != i+1 {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		if e.Event == "log" && (len(stages) == 0 || stages[len(stages)-1] != e.Stage) {
			stages = append(stages, e.Stage)
		}
	}
	if got := strings.Join(stages, ","); got != ",ipam,provision,register,configure,record" {
		t.Errorf("log stages = %s", got)
	}
	r, _ := q.Run(ctx, run)
	if r.EeImage.String != "localhost/gaia/ee-default:dev@sha256:ee" || len(r.ConfigCommit.String) != 40 {
		t.Errorf("run = %+v", r)
	}
}

func TestFailedConfigureFailsTheRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	g := e.graph(nil)
	p, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	// The UID is made during the run; fail whichever host comes.
	e.deps.Configure = failAll{e.configure}
	run, err := Run(ctx, g, p, e.deps)
	if err == nil || !strings.Contains(err.Error(), "configure failed on labtpoc01") {
		t.Fatalf("err = %v", err)
	}
	r, _ := e.store.Q().Run(ctx, run)
	if r.Status != "failed" {
		t.Errorf("run = %+v", r)
	}
	all, _ := e.store.Q().RunEvents(ctx, run)
	if evs := ansibleEvents(all); len(evs) == 0 || !evs[0].Failed {
		t.Errorf("events = %+v", evs)
	}
}

// ansibleEvents leaves out output lines.
func ansibleEvents(all []db.RunEvent) []db.RunEvent {
	var out []db.RunEvent
	for _, e := range all {
		if e.Event != "log" {
			out = append(out, e)
		}
	}
	return out
}

// failAll makes every host of a job fail.
type failAll struct{ f *fakeConfigurer }

func (a failAll) Image(ctx context.Context, ref string) (string, error) { return a.f.Image(ctx, ref) }
func (a failAll) Run(ctx context.Context, j ansible.Job, on func(ansible.Event) error) (ansible.Result, error) {
	a.f.fail = j.Hosts[0].Name
	return a.f.Run(ctx, j, on)
}

func TestSitePlay(t *testing.T) {
	c := plan.Contract{Name: "web", Baseline: &plan.Baseline{Playbook: "baseline/linux.yml"},
		Roles: []plan.Role{{Name: "ansible-nginx", Role: "nginx", Params: map[string]any{"port": 8080}}}}
	got, err := sitePlay(c, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"import_playbook": "../baseline/linux.yml"`, `"role": "nginx"`, `"port": 8080`, `"become": true`} {
		if !strings.Contains(string(got), s) {
			t.Errorf("%s not in %s", s, got)
		}
	}
	// A no_log default masked in the plan is the role's own: not passed.
	c.Roles[0].Params["password"] = plan.Masked
	if got, err := sitePlay(c, nil, false); err != nil || strings.Contains(string(got), "password") {
		t.Errorf("masked param: %s %v", got, err)
	}
}

func TestApplyRegistersServersInMonitoring(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	hosts, _ := e.store.Q().LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid
	want := zabbix.Host{
		UID: uid, Hostname: "labtpoc01", IP: "10.50.0.100",
		Groups: []string{"Gaia", "Gaia/env/test", "Gaia/app/poc", "Gaia/site/lab", "Gaia/contract/poc-web"}, Templates: []string{"ICMP Ping"},
		Tags: []zabbix.Tag{{Tag: "gaia.contract", Value: "poc-web"}, {Tag: "app", Value: "poc"}, {Tag: "env", Value: "test"}, {Tag: "owner", Value: "team-platform"}},
	}
	if got := e.monitor.hosts[uid]; !reflect.DeepEqual(got, want) {
		t.Errorf("host\n got %+v\nwant %+v", got, want)
	}
	if e.monitor.endpoint != "http://192.168.1.25:8080/api_jsonrpc.php" || e.monitor.token != "zbx" {
		t.Errorf("monitor at %s with %s", e.monitor.endpoint, e.monitor.token)
	}
	id, _ := (&runner{d: e.deps}).externalID(ctx, uid, "zabbix", "host")
	if id != e.monitor.ids[uid] {
		t.Errorf("recorded host %q, want %q", id, e.monitor.ids[uid])
	}
	// The second apply finds the same host by its recorded ID.
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(e.monitor.ids) != 1 {
		t.Errorf("%d hosts", len(e.monitor.ids))
	}
}

func TestApplyWritesTheRunRecord(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	g := e.graph(nil)
	p, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	run, err := Run(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	q := e.store.Q()
	rec, err := q.RunRecord(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	var body runrecord.Record
	if err := json.Unmarshal([]byte(rec.Body), &body); err != nil {
		t.Fatal(err)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid
	if body.Run != run || body.RequestedBy != "ops@lab" || body.Result != "succeeded" || body.PlanHash != p.Hash ||
		body.CatalogCommit != "c0ffee" || body.Source.Commit != "c0ffee" || len(body.ConfigCommit) != 40 ||
		body.ExecutionEnvironment != "localhost/gaia/ee-default:dev@sha256:ee" || body.FinishedAt.Before(body.StartedAt) {
		t.Errorf("record = %+v", body)
	}
	if want := map[string]string{"linux": "gaia", "pve-lab": "gaia@pve!poc", "zabbix": "api token"}; !reflect.DeepEqual(body.ExecutedAs, want) {
		t.Errorf("executedAs = %v", body.ExecutedAs)
	}
	var stages []string
	for _, c := range body.Changes {
		stages = append(stages, c.Stage+":"+c.Action)
		if c.Stage != "provision" && c.UID != uid {
			t.Errorf("change without the UID: %+v", c)
		}
	}
	if got := strings.Join(stages, ","); got != "plan:create,provision:pulumi up,monitoring:create,configure:ansible" {
		t.Errorf("changes = %s", got)
	}

	if len(e.archive.objects) != 2 {
		t.Fatalf("archive: %v", e.archive.objects)
	}
	key := runrecord.Key(rec)
	if rec.Archived.String != "mem://"+key || time.Until(e.archive.until[key]) < 23*time.Hour {
		t.Errorf("archived %q until %s", rec.Archived.String, e.archive.until[key])
	}
	// The raw events go next to it, unlocked, one JSON line each.
	var logKey string
	for k := range e.archive.objects {
		if strings.HasPrefix(k, "logs/") {
			logKey = k
		}
	}
	all, _ := q.RunEvents(ctx, run)
	if !strings.HasSuffix(logKey, "/"+run+".jsonl") || !e.archive.until[logKey].IsZero() ||
		strings.Count(string(e.archive.objects[logKey]), "\n") != len(all) {
		t.Errorf("logs at %q (until %s): %d lines for %d events", logKey, e.archive.until[logKey], strings.Count(string(e.archive.objects[logKey]), "\n"), len(all))
	}

	// A failed run is recorded too, and the chain holds.
	e.deps.Provision = func(context.Context, pulumi.Backend, pulumi.Stack, io.Writer) (map[string]pulumi.Result, pulumi.Changes, error) {
		return nil, nil, fmt.Errorf("proxmox is down")
	}
	p, _ = plan.Build(ctx, g, nil, StoreState{S: e.store})
	failed, _ := Run(ctx, g, p, e.deps)
	r2, err := q.RunRecord(ctx, failed)
	if err != nil || r2.PrevHash != rec.RecordHash || !strings.Contains(r2.Body, `"result":"failed"`) || !strings.Contains(r2.Body, "proxmox is down") {
		t.Errorf("failed run record: %+v, %v", r2, err)
	}
	if n, err := runrecord.Verify(ctx, q); err != nil || n != 2 {
		t.Errorf("verify: %d, %v", n, err)
	}
}

func TestApplyDistributesAccess(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	person := func(name string, enabled bool, key string) {
		id := store.NewUserID()
		if err := q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id, Username: name, Provider: "corp", Subject: pgText("g-" + name), DirectoryName: pgText(name), Enabled: enabled}); err != nil {
			t.Fatal(err)
		}
		if err := q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: "team-platform", Source: "ad:corp"}); err != nil {
			t.Fatal(err)
		}
		if key != "" {
			if err := q.InsertLoginKey(ctx, db.InsertLoginKeyParams{Fingerprint: "fp-" + name, UserID: id, Name: "k", PublicKey: key}); err != nil {
				t.Fatal(err)
			}
		}
	}
	person("ayse", true, "ssh-ed25519 AAAA ayse")
	person("mehmet", true, "")
	person("ali", false, "ssh-ed25519 BBBB ali")
	person("Bad Name", true, "")
	g := e.graph(func(c string) string {
		return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { team: team-platform, sudo: true }\n", 1) +
			"---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-platform }\nspec: {}\n"
	})
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Contracts[0].Access; len(a) != 1 || a[0].Via != "team/team-platform" || !a[0].Sudo {
		t.Fatalf("plan access = %+v", a)
	}
	run, err := Run(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	proj := e.configure.projects[0]
	if proj[".gaia/roles/gaia_access/tasks/main.yml"] == "" {
		t.Fatal("the access role is not in the project")
	}
	site := proj[".gaia/site.yml"]
	if !strings.Contains(site, `"role": "gaia_access"`) || !strings.Contains(site, `"name": "ayse"`) || !strings.Contains(site, `"ssh-ed25519 AAAA ayse"`) ||
		!strings.Contains(site, `"name": "mehmet"`) || strings.Contains(site, "ali") || strings.Contains(site, "Bad Name") {
		t.Errorf("site.yml = %s", site)
	}
	ug, ok := e.monitor.userGroups["Gaia/team-platform"]
	if !ok || strings.Join(ug.users, ",") != "Bad Name,ayse,mehmet" || len(ug.rights) != 1 || ug.rights["Gaia/contract/poc-web"] != zabbix.PermissionReadWrite {
		t.Errorf("monitoring user groups = %+v", e.monitor.userGroups)
	}
	for hg, perm := range ug.rights {
		if perm != zabbix.PermissionReadWrite {
			t.Errorf("%s: permission %d, want read-write (sudo)", hg, perm)
		}
	}
	rec, err := q.RunRecord(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	var body runrecord.Record
	_ = json.Unmarshal([]byte(rec.Body), &body)
	if len(body.AccessSnapshot) != 1 || strings.Join(body.AccessSnapshot[0].Users, ",") != "ayse,mehmet" || !body.AccessSnapshot[0].Sudo {
		t.Errorf("access snapshot = %+v", body.AccessSnapshot)
	}
}

func TestRunAccessOnlyDistributesAccess(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	id := store.NewUserID()
	_ = q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id, Username: "ayse", Provider: "corp", Subject: pgText("g"), DirectoryName: pgText("ayse"), Enabled: true})
	_ = q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: "team-platform", Source: "ad:corp"})
	g := e.graph(func(c string) string {
		return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - { team: team-platform }\n", 1) +
			"---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-platform }\nspec: {}\n"
	})
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	applied, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	// A new member: the access run gives her access and does nothing else.
	id2 := store.NewUserID()
	_ = q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id2, Username: "zeynep", Provider: "corp", Subject: pgText("g2"), DirectoryName: pgText("zeynep"), Enabled: true})
	_ = q.AddTeam(ctx, db.AddTeamParams{UserID: id2, Team: "team-platform", Source: "ad:corp"})
	run, err := RunAccess(ctx, g, applied, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.configure.jobs) != 2 {
		t.Fatalf("%d configure jobs", len(e.configure.jobs))
	}
	site := e.configure.projects[1][".gaia/site.yml"]
	if strings.Contains(site, "import_playbook") || !strings.Contains(site, `"name": "zeynep"`) || !strings.Contains(site, `"name": "ayse"`) {
		t.Errorf("access run site.yml = %s", site)
	}
	if hosts, _ := q.LiveHosts(ctx, "poc-web"); len(hosts) != 1 {
		t.Errorf("hosts after the access run = %d", len(hosts))
	}
	rec, _ := q.RunRecord(ctx, run)
	var body runrecord.Record
	_ = json.Unmarshal([]byte(rec.Body), &body)
	if len(body.AccessSnapshot) != 1 || strings.Join(body.AccessSnapshot[0].Users, ",") != "ayse,zeynep" || body.Result != "succeeded" {
		t.Errorf("record = %+v", body)
	}
	for _, c := range body.Changes {
		if c.Stage == "provision" {
			t.Errorf("the access run provisioned: %+v", c)
		}
	}
}

const agentsAndApps = `---
apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec: { secrets: { prefix: "bao://kv/apps/team-platform/" } }
---
apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: web.tomcat }
spec: { source: { provider: config-repo, role: corp.web.tomcat } }
---
apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: agents.bigfix }
spec:
  source: { provider: config-repo, role: corp.agents.bigfix }
  params: { relay: { type: str, default: relay.lab } }
---
apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: agents.zabbix }
spec: { source: { provider: config-repo, role: corp.agents.zabbix } }
---
apiVersion: gaia/v1alpha1
kind: Agent
metadata: { name: bigfix }
spec: { os: [linux], ansibleRole: agents.bigfix }
---
apiVersion: gaia/v1alpha1
kind: Agent
metadata: { name: zabbix-agent }
spec: { os: [linux], ansibleRole: agents.zabbix, mandatory: true }
---
apiVersion: gaia/v1alpha1
kind: App
metadata: { name: portal }
spec:
  playbook: apps/portal.yml
  requires: [web.tomcat]
  params:
    version: { type: str, required: true, gaia: { format: semver } }
    dbPassword: { type: str, no_log: true, gaia: { secret: true } }
`

func TestApplyRunsAgentsAndAppsWithSecrets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.deps.Secrets.(fakeSecrets)["bao://kv/apps/team-platform/portal"] = map[string]string{"db_password": "s3cret-db"}
	g := e.graph(func(c string) string {
		return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  ansibleRoles: [web.tomcat]\n  agents: [bigfix]\n  apps:\n    - name: portal\n      params: { version: 2.4.1, dbPassword: \"bao://kv/apps/team-platform/portal#db_password\" }\n", 1) + agentsAndApps
	})
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Contracts[0]
	if len(c.Agents) != 2 || c.Agents[0].Name != "zabbix-agent" || !c.Agents[0].Mandatory || c.Agents[1].Params["relay"] != "relay.lab" ||
		len(c.Apps) != 1 || c.Apps[0].Params["dbPassword"] != "bao://kv/apps/team-platform/portal#db_password" {
		t.Fatalf("plan = %+v", c)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	j := e.configure.jobs[0]
	site := e.configure.projects[0][".gaia/site.yml"]
	last := -1
	for _, x := range []string{"baseline/linux.yml", "corp.agents.zabbix", "corp.agents.bigfix", "corp.web.tomcat", "apps/portal.yml"} {
		i := strings.Index(site, x)
		if i <= last {
			t.Errorf("%s out of order in %s", x, site)
		}
		last = i
	}
	if !strings.Contains(site, `"dbPassword": "s3cret-db"`) || !strings.Contains(site, `"version": "2.4.1"`) || !slices.Contains(j.Secrets, "s3cret-db") {
		t.Errorf("secrets: site %s, job secrets %v", site, j.Secrets)
	}
	if strings.Contains(fmt.Sprint(p), "s3cret-db") {
		t.Error("the secret is in the plan")
	}

	// Another team's secret, or a value in Git, does not plan.
	for _, v := range []string{`"bao://kv/apps/team-web/portal#x"`, `hunter2`} {
		bad := e.graphErr(func(c string) string {
			return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  ansibleRoles: [web.tomcat]\n  apps:\n    - name: portal\n      params: { version: 2.4.1, dbPassword: "+v+" }\n", 1) + agentsAndApps
		})
		if bad == nil || !strings.Contains(bad.Error(), "dbPassword") {
			t.Errorf("%s: %v", v, bad)
		}
	}
}

const rebootOp = `---
apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: reboot-poc }
spec:
  action: reboot
  selector: 'contract == "poc-web" && labels.env == "test"'
  strategy: { batchSize: "1", maxFailures: 0 }
`

func TestRunOperationInBatches(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	edit := func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 3", 1) + rebootOp }
	g := e.graph(edit)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	op, err := plan.BuildOperation(ctx, g, "reboot-poc", StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Targets) != 3 || len(op.Batches) != 3 || strings.Join(op.Steps, ",") != "maintenance,reboot,health,end maintenance" || op.Contracts[0] != "poc-web" {
		t.Fatalf("operation plan = %+v", op)
	}
	// The second server fails: the third batch does not run.
	e.configure.fail = op.Batches[1][0]
	jobs := len(e.configure.jobs)
	_, out, err := RunOperation(ctx, g, op, e.deps)
	if err == nil || out.Status != "partial" {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	var got []string
	for _, r := range out.Results {
		got = append(got, fmt.Sprintf("%d:%s", r.Batch, r.Result))
	}
	if strings.Join(got, ",") != "1:ok,2:failed,3:not run" || len(e.configure.jobs)-jobs != 2 {
		t.Errorf("results = %v, jobs %d", got, len(e.configure.jobs)-jobs)
	}
	site := e.configure.projects[len(e.configure.projects)-1][".gaia/site.yml"]
	if !strings.Contains(site, "ansible.builtin.reboot") || !strings.Contains(site, "wait_for_connection") {
		t.Errorf("operation play = %s", site)
	}
	// Plans are stable: the same servers give the same hash.
	again, _ := plan.BuildOperation(ctx, g, "reboot-poc", StoreState{S: e.store})
	if again.Hash != op.Hash {
		t.Error("the operation plan hash moved")
	}
}

func TestPatchOperationWithSnapshotAndMaintenance(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var snaps []string
	e.deps.Snapshot = func(_ context.Context, _ proxmox.Credentials, vmid int, name, _ string) error {
		snaps = append(snaps, fmt.Sprintf("%d:%s", vmid, name))
		return nil
	}
	op := `---
apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: patch-poc }
spec:
  action: patch
  selector: 'contract == "poc-web"'
  options: { scope: security, reboot: auto, snapshot: true }
`
	g := e.graph(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) + op })
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	o, err := plan.BuildOperation(ctx, g, "patch-poc", StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(o.Steps, ","); got != "maintenance,snapshot,patch,reboot (if needed),health,end maintenance" {
		t.Errorf("steps = %s", got)
	}
	_, out, err := RunOperation(ctx, g, o, e.deps)
	if err != nil || out.Status != "succeeded" || len(out.Results) != 2 {
		t.Fatalf("outcome = %+v, %v", out, err)
	}
	if len(snaps) != 2 || !strings.Contains(snaps[0], ":gaia_patch_poc_") {
		t.Errorf("snapshots = %v", snaps)
	}
	if len(e.monitor.maintenances) != 2 || !strings.HasPrefix(e.monitor.maintenances[0], "start Gaia: patch-poc, batch 1 ") || e.monitor.maintenances[1] != "end m0" {
		t.Errorf("maintenance = %v", e.monitor.maintenances)
	}
	site := e.configure.projects[len(e.configure.projects)-1][".gaia/site.yml"]
	if !strings.Contains(site, `"security": true`) || !strings.Contains(site, "needs-restarting -r") {
		t.Errorf("patch play = %s", site)
	}

	// A snapshot that fails fails its server, which then is not patched.
	e.deps.Snapshot = func(context.Context, proxmox.Credentials, int, string, string) error {
		return fmt.Errorf("storage full")
	}
	jobs := len(e.configure.jobs)
	_, out, _ = RunOperation(ctx, g, o, e.deps)
	if out.Status != "failed" || !strings.Contains(out.Results[0].Detail, "storage full") || len(e.configure.jobs) != jobs {
		t.Errorf("after a failed snapshot: %+v, %d jobs", out, len(e.configure.jobs)-jobs)
	}
}

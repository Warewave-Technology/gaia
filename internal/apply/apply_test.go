package apply

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/apply/access"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/compute"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/ipam"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/nexus"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/warpgate"
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
	if b.URL != "postgres://gaia_owner:pw@192.168.1.26:5432/pulumi?sslmode=disable" || b.Env["VAULT_TOKEN"] != "bao-token" {
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
		if st.VSphere != nil {
			res[v.UID] = pulumi.Result{UUID: "4200-" + v.Hostname, IPv4: []string{ip}}
		}
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
	disabled        map[string]bool
}

func (f *fakeMonitor) State(_ context.Context, uid, hostID string) (*zabbix.HostState, error) {
	h, ok := f.hosts[uid]
	if !ok || f.ids[uid] != hostID {
		return nil, nil
	}
	return &zabbix.HostState{HostID: hostID, Name: h.Hostname, Enabled: !f.disabled[hostID], IP: h.IP, Groups: h.Groups, Templates: h.Templates}, nil
}

func (f *fakeMonitor) GaiaHosts(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for uid, h := range f.hosts {
		out[uid] = h.Hostname
	}
	return out, nil
}

func (f *fakeMonitor) SetEnabled(_ context.Context, hostID string, enabled bool) error {
	if f.disabled == nil {
		f.disabled = map[string]bool{}
	}
	f.disabled[hostID] = !enabled
	return nil
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
	platform  map[string]func(string) string // edits of other example files
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
		ScanHostKey: func(_ context.Context, addr, alg string) (ssh.PublicKey, error) {
			if alg != ssh.KeyAlgoED25519 {
				return nil, fmt.Errorf("no %s", alg)
			}
			return testHostKey(addr), nil
		},
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
		if f := e.platform[rel]; f != nil {
			content = f(content)
		}
		files = append(files, catalog.File{Path: rel, Data: []byte(content)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Edits of files the example lacks add them.
	for rel, f := range e.platform {
		if !slices.ContainsFunc(files, func(x catalog.File) bool { return x.Path == rel }) {
			files = append(files, catalog.File{Path: rel, Data: []byte(f(""))})
		}
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

// fakeDirectory is AD-integrated DNS: zone -> name -> records.
type fakeDirectory struct {
	zones     map[string]map[string][]ad.Record
	computers map[string]*ad.Computer // DN -> account
	passwords map[string]string       // DN -> its one-time password
	cfg       ad.Config
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{zones: map[string]map[string][]ad.Record{}, computers: map[string]*ad.Computer{}, passwords: map[string]string{}}
}

func (f *fakeDirectory) SetRecord(zone, name string, rec ad.Record, owned bool) (bool, error) {
	z, ok := f.zones[zone]
	if !ok {
		return false, ad.ErrNoZone
	}
	for _, r := range z[name] {
		if r.Type == rec.Type && r.Data == rec.Data {
			return false, nil
		}
		if r.Type == rec.Type && !owned {
			return false, fmt.Errorf("%w: %s.%s has %s", ad.ErrTaken, name, zone, r.Data)
		}
	}
	z[name] = []ad.Record{rec}
	return true, nil
}

func (f *fakeDirectory) DeleteRecord(zone, name string, rec ad.Record) (bool, error) {
	for _, r := range f.zones[zone][name] {
		if r.Type == rec.Type && r.Data == rec.Data {
			delete(f.zones[zone], name)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeDirectory) ReverseZone(ip netip.Addr) (string, string, error) {
	o := ip.As4()
	z := fmt.Sprintf("%d.%d.%d.in-addr.arpa", o[2], o[1], o[0])
	if _, ok := f.zones[z]; !ok {
		return "", "", ad.ErrNoZone
	}
	return z, fmt.Sprint(o[3]), nil
}

func (f *fakeDirectory) Close() error { return nil }

func (f *fakeDirectory) Records(zone, name string) ([]ad.Record, error) {
	if _, ok := f.zones[zone]; !ok {
		return nil, ad.ErrNoZone
	}
	return f.zones[zone][name], nil
}

func (f *fakeDirectory) Domain() string { return "lab.test" }

func (f *fakeDirectory) Computer(name string) (ad.Computer, error) {
	for _, c := range f.computers {
		if strings.EqualFold(c.Name, name) {
			return *c, nil
		}
	}
	return ad.Computer{}, ad.ErrNoComputer
}

func (f *fakeDirectory) ComputerByGUID(guid string) (ad.Computer, error) {
	for _, c := range f.computers {
		if c.GUID == guid {
			return *c, nil
		}
	}
	return ad.Computer{}, ad.ErrNoComputer
}

func (f *fakeDirectory) CreateComputer(ou, name, info, password string) (ad.Computer, error) {
	dn := "CN=" + strings.ToUpper(name) + "," + ou
	if _, ok := f.computers[dn]; ok {
		return ad.Computer{}, errors.New("exists")
	}
	c := &ad.Computer{DN: dn, Name: strings.ToUpper(name), GUID: fmt.Sprintf("guid-%d", len(f.computers)+1), Info: info}
	f.computers[dn] = c
	f.passwords[dn] = password
	return *c, nil
}

func (f *fakeDirectory) ResetComputer(dn, password string) error {
	f.passwords[dn] = password
	return nil
}

func (f *fakeDirectory) SetComputerEnabled(dn string, enabled bool) error {
	if c, ok := f.computers[dn]; ok {
		c.Disabled = !enabled
	}
	return nil
}

func (f *fakeDirectory) DeleteComputer(dn string) error {
	delete(f.computers, dn)
	return nil
}

func TestApplyWritesDNS(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := newFakeDirectory()
	dir.zones = map[string]map[string][]ad.Record{"lab.test": {"taken": {{Type: ad.TypeA, Data: "10.9.9.9"}}}, "0.50.10.in-addr.arpa": {}}
	e.deps.Directory = func(_ context.Context, c ad.Config) (Directory, error) {
		dir.cfg = c
		return dir, nil
	}
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/ad/writer"] = map[string]string{"username": "gaia-writer@lab.test", "password": "w"}
	e.platform = map[string]func(string) string{
		"platform/providers/corp-ad.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: corp-ad }\nspec: { type: activedirectory, endpoint: ldaps://dc1.lab.test, credentials: bao://kv/platform/ad/reader, writeCredentials: bao://kv/platform/ad/writer }\n"
		},
		"platform/catalog/network.yaml": func(c string) string {
			return strings.Replace(c, "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1], provider: corp-ad, zone: lab.test, reverse: true, ttl: 600 }", 1)
		},
	}

	p, err := e.apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Contracts[0].Servers[0].DNS; d == nil || *d != (plan.DNS{Provider: "corp-ad", Zone: "lab.test", Reverse: true, TTL: 600}) {
		t.Errorf("plan DNS = %+v", d)
	}
	if dir.cfg.Endpoint != "ldaps://dc1.lab.test" || dir.cfg.Username != "gaia-writer@lab.test" {
		t.Errorf("directory config = %+v", dir.cfg)
	}
	a := dir.zones["lab.test"]["labtpoc01"]
	ptr := dir.zones["0.50.10.in-addr.arpa"]["100"]
	if len(a) != 1 || a[0] != (ad.Record{Type: ad.TypeA, TTL: 600, Data: "10.50.0.100"}) {
		t.Errorf("A = %+v", a)
	}
	if len(ptr) != 1 || ptr[0] != (ad.Record{Type: ad.TypePTR, TTL: 600, Data: "labtpoc01.lab.test"}) {
		t.Errorf("PTR = %+v", ptr)
	}
	hosts, _ := e.store.Q().LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid
	ids := map[string]string{}
	got, _ := e.store.Q().ExternalIDs(ctx, uid)
	for _, x := range got {
		ids[x.Provider+"/"+x.Kind] = x.ExternalID
	}
	if ids["corp-ad/dns-a"] != "labtpoc01.lab.test" || ids["corp-ad/dns-ptr"] != "100.0.50.10.in-addr.arpa" {
		t.Errorf("external IDs = %v", ids)
	}

	// Gaia's own record with another address is put right; someone else's
	// name stops the run.
	dir.zones["lab.test"]["labtpoc01"] = []ad.Record{{Type: ad.TypeA, Data: "10.50.0.250"}}
	dir.zones["lab.test"]["labtpoc02"] = []ad.Record{{Type: ad.TypeA, Data: "10.50.0.250"}}
	if _, err := e.apply(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) }); err == nil ||
		!strings.Contains(err.Error(), "labtpoc02: ad: the name has another record: labtpoc02.lab.test has 10.50.0.250") {
		t.Fatalf("taken name: %v", err)
	}
	if a := dir.zones["lab.test"]["labtpoc01"]; a[0].Data != "10.50.0.100" {
		t.Errorf("own record = %+v", a)
	}

	// Decommissioning removes both records.
	delete(dir.zones["lab.test"], "labtpoc02")
	if _, err := e.apply(func(c string) string { return c + "  lifecycle: { state: decommissioned }\n" }); err != nil {
		t.Fatal(err)
	}
	if n := len(dir.zones["lab.test"]) + len(dir.zones["0.50.10.in-addr.arpa"]); n != 1 { // "taken" stays
		t.Errorf("zones after decommission = %+v", dir.zones)
	}
}

func TestApplyJoinsTheDomain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := newFakeDirectory()
	dir.computers["CN=LABTPOC02,CN=Computers"] = &ad.Computer{DN: "CN=LABTPOC02,CN=Computers", Name: "LABTPOC02", GUID: "someone-else"}
	e.deps.Directory = func(context.Context, ad.Config) (Directory, error) { return dir, nil }
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/ad/writer"] = map[string]string{"username": "gaia-writer@lab.test", "password": "w"}
	ou := "OU=Servers,DC=lab,DC=test"
	e.platform = map[string]func(string) string{
		"platform/providers/corp-ad.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: corp-ad }\nspec: { type: activedirectory, endpoint: ldaps://dc1.lab.test, credentials: bao://kv/platform/ad/reader, writeCredentials: bao://kv/platform/ad/writer }\n"
		},
		"platform/catalog/network.yaml": func(c string) string {
			return strings.Replace(c, "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1] }\n  domain: { provider: corp-ad, ou: \""+ou+"\" }", 1)
		},
	}
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	dn := "CN=LABTPOC01," + ou
	comp := dir.computers[dn]
	hosts, _ := e.store.Q().LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid
	if comp == nil || comp.Info != "Managed by Gaia. gaia.uid="+uid+" contract=poc-web" {
		t.Fatalf("computers = %+v", dir.computers)
	}
	if id, _ := e.externalIDOf(uid, "corp-ad", "computer"); id != comp.GUID {
		t.Errorf("recorded GUID = %q", id)
	}
	otp := dir.passwords[dn]
	job := e.configure.jobs[0]
	if j := job.Hosts[0].Vars["gaia_domain_join"]; j != (join{Domain: "lab.test", Password: otp}) || otp == "" {
		t.Errorf("join vars = %+v (password %q)", j, otp)
	}
	if !slices.Contains(job.Secrets, otp) {
		t.Error("the one-time password is not masked")
	}
	site := e.configure.projects[0][sitePlaybook]
	if !strings.Contains(site, `"name": "Domain of poc-web"`) || strings.Index(site, "gaia_domain_join") > strings.Index(site, "baseline/linux.yml") {
		t.Errorf("site play:\n%s", site)
	}
	if _, ok := e.configure.projects[0][".gaia/roles/gaia_domain_join/tasks/main.yml"]; !ok {
		t.Error("the join role is not in the project")
	}

	// Not joined yet: a new password. Joined: none.
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	if again := dir.passwords[dn]; again == otp || e.configure.jobs[1].Hosts[0].Vars["gaia_domain_join"].(join).Password != again {
		t.Errorf("not joined: password %q, then %q", otp, again)
	}
	comp.Joined = true
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	if j := e.configure.jobs[2].Hosts[0].Vars["gaia_domain_join"].(join); j.Password != "" {
		t.Errorf("joined: %+v", j)
	}

	// A name whose account is someone else's stops the run.
	if _, err := e.apply(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) }); err == nil ||
		!strings.Contains(err.Error(), "labtpoc02: computer account CN=LABTPOC02,CN=Computers exists and is not the server's") {
		t.Fatalf("taken: %v", err)
	}
	delete(dir.computers, "CN=LABTPOC02,CN=Computers")

	// Decommissioning deletes the accounts.
	if _, err := e.apply(func(c string) string { return c + "  lifecycle: { state: decommissioned }\n" }); err != nil {
		t.Fatal(err)
	}
	if len(dir.computers) != 0 {
		t.Errorf("computers after decommission = %+v", dir.computers)
	}
}

func (e *env) externalIDOf(uid, provider, kind string) (string, error) {
	ids, err := e.store.Q().ExternalIDs(context.Background(), uid)
	for _, x := range ids {
		if x.Provider == provider && x.Kind == kind {
			return x.ExternalID, err
		}
	}
	return "", err
}

func TestRegistrationDrift(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := newFakeDirectory()
	dir.zones = map[string]map[string][]ad.Record{"lab.test": {}, "0.50.10.in-addr.arpa": {}}
	e.deps.Directory = func(context.Context, ad.Config) (Directory, error) { return dir, nil }
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/ad/writer"] = map[string]string{"username": "w", "password": "w"}
	e.platform = map[string]func(string) string{
		"platform/providers/corp-ad.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: corp-ad }\nspec: { type: activedirectory, endpoint: ldaps://dc1, credentials: bao://kv/platform/ad/reader, writeCredentials: bao://kv/platform/ad/writer }\n"
		},
		"platform/catalog/network.yaml": func(c string) string {
			return strings.Replace(c, "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1], provider: corp-ad, zone: lab.test, reverse: true }", 1)
		},
	}
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	g := e.graph(nil)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if fs, err := CheckRegistration(ctx, g, p, e.deps); err != nil || len(fs) != 0 {
		t.Fatalf("clean: %v %v", fs, err)
	}

	// Someone switched monitoring off and pointed the name elsewhere.
	_ = e.monitor.SetEnabled(ctx, "10000", false)
	dir.zones["lab.test"]["labtpoc01"] = []ad.Record{{Type: ad.TypeA, Data: "10.50.0.250"}}
	fs, err := CheckRegistration(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.String())
	}
	want := []string{"poc-web labtpoc01 monitoring/enabled: want enabled, got disabled", "poc-web labtpoc01 dns/a: want 10.50.0.100, got 10.50.0.250"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("findings\n got %v\nwant %v", got, want)
	}
}

func TestProvisioningAndConfigurationDrift(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	g := e.graph(nil)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	uid := mustLive(t, e)[0].Uid
	var previewed []pulumi.VM
	e.deps.PreviewDrift = func(_ context.Context, _ pulumi.Backend, st pulumi.Stack) (map[string]pulumi.Drift, error) {
		previewed = st.VMs
		return map[string]pulumi.Drift{uid: {Op: "update", Diffs: []string{"memory"}}}, nil
	}
	fs, err := CheckProvisioning(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].String() != "poc-web labtpoc01 provisioning/vm: want as applied, got update memory" {
		t.Errorf("provisioning = %v", fs)
	}
	if len(previewed) != 1 || !reflect.DeepEqual(previewed[0], e.compute.stacks[0].VMs[0]) {
		t.Errorf("previewed %+v, applied %+v", previewed, e.compute.stacks[0].VMs[0])
	}

	// The fake configurer changes one task on every host: in check mode
	// that is drift.
	jobs := len(e.configure.jobs)
	fs, err = CheckConfiguration(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.configure.jobs) != jobs+1 || !e.configure.jobs[jobs].Check {
		t.Fatalf("check job: %d jobs, %+v", len(e.configure.jobs)-jobs, e.configure.jobs[len(e.configure.jobs)-1].Check)
	}
	if len(fs) != 1 || fs[0].Area != "configuration" || !strings.HasPrefix(fs[0].Got, "1 task(s) would change: ") {
		t.Errorf("configuration = %v", fs)
	}
}

func TestDiscover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.apply(func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) }); err != nil {
		t.Fatal(err)
	}
	hosts := mustLive(t, e)
	day2 := &fakeDayTwo{vms: []compute.ObservedVM{
		{ID: "9401", Name: "labtpoc01", PowerState: "on"},
		{ID: "9300", Name: "tpl", PowerState: "off", Template: true},
		{ID: "9450", Name: "old", PowerState: "off", Tags: []string{"gaia", "gaia-uid_gone1"}},
		{ID: "120", Name: "legacy-db", PowerState: "on"},
	}}
	e.deps.Compute = func(context.Context, string, string, map[string]string) (compute.Compute, error) { return day2, nil }
	e.monitor.hosts["gone2"] = zabbix.Host{Hostname: "oldmon"}
	rep, err := Discover(ctx, e.graph(nil), e.deps)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range rep.VMs {
		got = append(got, v.Category+":"+v.Name+":"+v.UID)
	}
	want := "managed:labtpoc01:" + hosts[0].Uid + ",orphan:old:gone1,template:tpl:,unmanaged:legacy-db:"
	if strings.Join(got, ",") != want {
		t.Errorf("vms\n got %v\nwant %s", got, want)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Hostname != "labtpoc02" || rep.Missing[0].ID != "9402" {
		t.Errorf("missing = %+v", rep.Missing)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0].Name != "oldmon" || rep.Counts["monitoring orphan"] != 1 {
		t.Errorf("monitoring = %+v", rep.Hosts)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %v", rep.Errors)
	}
}

func TestAdoption(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	legacy := `apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: legacy-web
  labels: { app: legacy, env: test, owner: team-platform }
spec:
  hosts:
    - { name: legacyweb01, vm: "pve-lab:120", ip: 10.50.0.20 }
  management: { configuration: report }
  image: rocky9
  size: small
  network: lab-poc
  baseline: linux-base
`
	e.platform = map[string]func(string) string{"contracts/legacy.yaml": func(string) string { return legacy }}
	none := func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 0", 1) }
	stacks, jobs := len(e.compute.stacks), len(e.configure.jobs)
	p, err := e.apply(none)
	if err != nil {
		t.Fatal(err)
	}
	var lc plan.Contract
	for _, c := range p.Contracts {
		if c.Name == "legacy-web" {
			lc = c
		}
	}
	if len(lc.Servers) != 1 || lc.Servers[0].Action != plan.Adopt {
		t.Fatalf("plan = %+v", lc.Servers)
	}
	hosts, _ := q.LiveHosts(ctx, "legacy-web")
	if len(hosts) != 1 || hosts[0].Hostname != "legacyweb01" || hosts[0].Status != db.HostStatusActive {
		t.Fatalf("hosts = %+v", hosts)
	}
	uid := hosts[0].Uid
	ids := map[string]string{}
	for _, x := range must(q.ExternalIDs(ctx, uid)) {
		ids[x.Provider+"/"+x.Kind] = x.ExternalID
	}
	if ids["pve-lab/vm"] != "120" || ids["ipam/ip"] != "10.50.0.20" || ids["zabbix/host"] == "" {
		t.Errorf("external IDs = %v", ids)
	}
	// Nothing provisioned for it; configuration only reported.
	for _, st := range e.compute.stacks[stacks:] {
		for _, v := range st.VMs {
			t.Errorf("a VM in a stack: %+v", v)
		}
	}
	if len(e.configure.jobs) != jobs {
		t.Errorf("%d configure jobs for a report-only contract", len(e.configure.jobs)-jobs)
	}
	if h, ok := e.monitor.hosts[uid]; !ok || h.IP != "10.50.0.20" {
		t.Errorf("monitoring = %+v", e.monitor.hosts)
	}
	// Again: kept, not adopted twice.
	if p, err := e.apply(none); err != nil {
		t.Fatal(err)
	} else {
		for _, c := range p.Contracts {
			if c.Name == "legacy-web" && (len(c.Servers) != 1 || c.Servers[0].Action != plan.Keep) {
				t.Errorf("second plan = %+v", c.Servers)
			}
		}
	}
	// Decommissioned: let go, nothing deleted. Also when the contract no
	// longer lists it at all.
	e.platform["contracts/legacy.yaml"] = func(string) string { return legacy + "  lifecycle: { state: decommissioned }\n" }
	if _, err := e.apply(none); err != nil {
		t.Fatal(err)
	}
	if h, _ := q.Host(ctx, uid); h.Status != db.HostStatusUnmanaged {
		t.Errorf("host = %s", h.Status)
	}
	if len(e.monitor.deleted) != 0 {
		t.Errorf("zabbix deleted %v", e.monitor.deleted)
	}
}

// onVSphere turns the lab site into a vSphere one.
func (e *env) onVSphere() {
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/vsphere/lab"] = map[string]string{"username": "gaia@vsphere.local", "password": "vc", "insecure": "true"}
	e.platform = map[string]func(string) string{
		"platform/providers/pve-lab.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: pve-lab }\nspec: { type: vsphere, endpoint: https://vc.lab.test, credentials: bao://kv/platform/vsphere/lab }\n"
		},
		"platform/catalog/site.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Site\nmetadata: { name: lab }\nspec:\n  compute: { provider: pve-lab, datacenter: DC0, cluster: C0, folder: gaia, storage: { default: ds1 } }\n"
		},
		"platform/catalog/network.yaml": func(c string) string {
			c = strings.Replace(c, "attachment: { vnet: gaiapoc }", "attachment: { portgroup: VM Network }", 1)
			return strings.Replace(c, "dns: { servers: [1.1.1.1] }", "dns: { servers: [1.1.1.1], zone: lab.test }", 1)
		},
		"platform/catalog/image.yaml": func(c string) string {
			return strings.Replace(c, "template: { vmid: 9300 }", "template: rocky9-tpl", 1)
		},
	}
}

func TestApplyOnVSphere(t *testing.T) {
	e := setup(t)
	e.onVSphere()
	ctx := context.Background()
	q := e.store.Q()

	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	if len(hosts) != 1 || hosts[0].Status != db.HostStatusActive {
		t.Fatalf("hosts = %+v", hosts)
	}
	uid := hosts[0].Uid
	ids := map[string]string{}
	got, _ := q.ExternalIDs(ctx, uid)
	for _, x := range got {
		ids[x.Provider+"/"+x.Kind] = x.ExternalID
	}
	if ids["pve-lab/vm"] != "4200-labtpoc01" || ids["pve-lab/smbios"] != "" {
		t.Errorf("external IDs = %v", ids)
	}
	st := e.compute.stacks[0]
	if st.VSphere == nil || *st.VSphere != (pulumi.VSphereCredentials{Server: "vc.lab.test", Username: "gaia@vsphere.local", Password: "vc", Insecure: true}) {
		t.Errorf("credentials = %+v", st.VSphere)
	}
	want := pulumi.VM{
		UID: uid, Hostname: "labtpoc01", CPU: 2, MemoryMiB: 2048, DiskGiB: 20, Address: "10.50.0.100/24", Gateway: "10.50.0.1",
		DNS: []string{"1.1.1.1"}, Domain: "lab.test", User: "gaia", SSHKeys: []string{"ssh-ed25519 AAAA gaia@lab"},
		Datacenter: "DC0", Cluster: "C0", Folder: "gaia", Datastore: "ds1", Portgroup: "VM Network", TemplateName: "rocky9-tpl",
		CloudInit: true, Description: "Managed by Gaia.\ngaia.uid=" + uid + "\ncontract=poc-web\n",
	}
	if !reflect.DeepEqual(st.VMs[0], want) {
		t.Errorf("VM\n got %+v\nwant %+v", st.VMs[0], want)
	}

	// Decommissioning destroys it; there is no VM ID to wait for.
	if _, err := e.apply(func(c string) string { return c + "  lifecycle: { state: decommissioned }\n" }); err != nil {
		t.Fatal(err)
	}
	if last := e.compute.stacks[len(e.compute.stacks)-1]; last.VSphere == nil || len(last.VMs) != 0 {
		t.Errorf("last stack = %+v", last)
	}
	if hosts, _ := q.LiveHosts(ctx, "poc-web"); len(hosts) != 0 {
		t.Errorf("hosts = %+v", hosts)
	}
}

func TestDecommissionWithAGracePeriod(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	e.deps.Now = func() time.Time { return now }
	day2 := &fakeDayTwo{onDelete: func(vm string) {
		for k, id := range e.compute.vms {
			if fmt.Sprint(id) == vm {
				delete(e.compute.vms, k)
			}
		}
	}}
	e.deps.Compute = func(context.Context, string, string, map[string]string) (compute.Compute, error) { return day2, nil }
	var forgot []string
	e.deps.Forget = func(_ context.Context, _ pulumi.Backend, st pulumi.Stack, uids []string) ([]string, error) {
		forgot = append(forgot, st.Name+" "+strings.Join(uids, ","))
		return uids, nil
	}
	e.platform = map[string]func(string) string{
		"platform/policy.yaml": func(c string) string {
			return strings.Replace(c, "  environments:\n", "  decommission: { gracePeriod: 7d, finalSnapshot: { enabled: true }, ipQuarantine: 14d }\n  environments:\n", 1)
		},
	}
	gone := func(c string) string { return c + "  lifecycle: { state: decommissioned }\n" }

	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	uid := mustLive(t, e)[0].Uid
	applied := e.compute.stacks[0].VMs[0]

	// Decommissioned: stopped, not deleted.
	p, err := e.apply(gone)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Contracts[0].Decommission; d == nil || *d != (plan.Decommission{GraceSeconds: 7 * 86400, FinalSnapshot: true, IPQuarantine: 14 * 86400}) {
		t.Errorf("plan decommission = %+v", d)
	}
	h, _ := q.Host(ctx, uid)
	if h.Status != db.HostStatusStopped || !h.DeleteAfter.Time.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("host = %s until %v", h.Status, h.DeleteAfter.Time)
	}
	if got := strings.Join(day2.calls, "|"); got != "9401 snapshot gaia_final_20261102T1000|9401 power off" {
		t.Errorf("hypervisor calls = %s", got)
	}
	if !e.monitor.disabled["10000"] || len(e.monitor.deleted) != 0 {
		t.Errorf("monitoring: disabled %v, deleted %v", e.monitor.disabled, e.monitor.deleted)
	}
	stopped := applied
	stopped.Stopped = true
	if last := e.compute.stacks[len(e.compute.stacks)-1].VMs; len(last) != 1 || !reflect.DeepEqual(last[0], stopped) {
		t.Errorf("the stack while it waits:\n got %+v\nwant %+v", last, stopped)
	}

	// Still waiting: nothing happens to it.
	day2.calls = nil
	now = now.Add(24 * time.Hour)
	if _, err := e.apply(gone); err != nil {
		t.Fatal(err)
	}
	if len(day2.calls) != 0 {
		t.Errorf("calls while waiting: %v", day2.calls)
	}

	// Wanted again: started, monitored, as it was.
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := q.Host(ctx, uid); h.Status != db.HostStatusActive || h.DeleteAfter.Valid {
		t.Errorf("restored host = %s %v", h.Status, h.DeleteAfter)
	}
	if got := strings.Join(day2.calls, "|"); got != "9401 power on" || e.monitor.disabled["10000"] {
		t.Errorf("restore: calls %s, disabled %v", got, e.monitor.disabled)
	}
	if last := e.compute.stacks[len(e.compute.stacks)-1].VMs; !reflect.DeepEqual(last[0], applied) {
		t.Errorf("restored VM = %+v", last[0])
	}

	// Gone again, and the grace period ends: the decommission run deletes
	// it without touching the stack.
	day2.calls = nil
	g := e.graph(gone)
	p, err = plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	stacks := len(e.compute.stacks)
	now = now.Add(8 * 24 * time.Hour)
	appliedPlan, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if _, err := RunDecommission(ctx, g, appliedPlan, e.deps); err != nil {
		t.Fatal(err)
	}
	if len(e.compute.stacks) != stacks {
		t.Error("the decommission run ran Pulumi")
	}
	if strings.Join(forgot, ";") != "poc-web-lab "+uid {
		t.Errorf("forgot = %v", forgot)
	}
	if got := strings.Join(day2.calls, "|"); !strings.HasSuffix(got, "9401 delete VM") {
		t.Errorf("calls = %s", got)
	}
	if h, _ := q.Host(ctx, uid); h.Status != db.HostStatusDeleted {
		t.Errorf("host = %s", h.Status)
	}
	if strings.Join(e.monitor.deleted, ",") != uid {
		t.Errorf("zabbix deleted %v", e.monitor.deleted)
	}
	// The address is in quarantine.
	if a, err := ipamclient.New(e.ipam, "ipam-token").Allocate(ctx, "10.50.0.0/24", "next", "x"); err != nil || a.IP == "10.50.0.100" {
		t.Errorf("next allocation = %+v %v", a, err)
	}
}

func TestRetainLetsServersGo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	var forgot []string
	e.deps.Forget = func(_ context.Context, _ pulumi.Backend, st pulumi.Stack, uids []string) ([]string, error) {
		forgot = append(forgot, st.Name+" "+strings.Join(uids, ","))
		return uids, nil
	}
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	uid := mustLive(t, e)[0].Uid
	retain := func(c string) string { return c + "  lifecycle: { state: decommissioned, deletionPolicy: retain }\n" }
	p, err := e.apply(retain)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Contracts[0].Retain {
		t.Error("the plan does not retain")
	}
	if strings.Join(forgot, ";") != "poc-web-lab "+uid {
		t.Errorf("forgot = %v", forgot)
	}
	if h, _ := q.Host(ctx, uid); h.Status != db.HostStatusUnmanaged {
		t.Errorf("host = %s", h.Status)
	}
	if len(e.monitor.deleted) != 0 {
		t.Errorf("zabbix deleted %v", e.monitor.deleted)
	}
	if a, _ := ipamclient.New(e.ipam, "ipam-token").Allocations(ctx, uid); len(a) != 1 {
		t.Errorf("allocations = %+v", a)
	}
	if holder, err := q.NameHolder(ctx, "labtpoc01"); err != nil || holder != uid {
		t.Errorf("name held by %q %v", holder, err)
	}
	// Gone from Gaia: the next plan has nothing to do.
	if p, err := e.apply(retain); err != nil || len(p.Contracts[0].Servers) != 0 {
		t.Errorf("next plan = %+v %v", p.Contracts[0].Servers, err)
	}
}

func mustLive(t *testing.T, e *env) []db.Host {
	t.Helper()
	hosts, err := e.store.Q().LiveHosts(context.Background(), "poc-web")
	if err != nil || len(hosts) == 0 {
		t.Fatalf("live hosts = %+v %v", hosts, err)
	}
	return hosts
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
	// The new server's host key is recorded and checked from then on.
	hk, _ := q.HostKeys(ctx, uid)
	wantKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(testHostKey("10.50.0.100"))))
	if len(hk) != 1 || hk[0].PublicKey != wantKey || j.KnownHosts != "10.50.0.100 "+wantKey+"\n" {
		t.Errorf("host keys %+v, known hosts %q", hk, j.KnownHosts)
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
	got, err := sitePlay(c, nil, false, false)
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
	if got, err := sitePlay(c, nil, false, false); err != nil || strings.Contains(string(got), "password") {
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
	if e.monitor.endpoint != "http://192.168.1.26:8080/api_jsonrpc.php" || e.monitor.token != "zbx" {
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

// fakeBastion is Warpgate: targets by ID, roles with their users.
type fakeBastion struct {
	targets     map[string]warpgate.Target
	targetRoles map[string][]string
	roles       map[string]string // name -> ID
	roleUsers   map[string][]string
	users       []warpgate.User
	hostKeys    map[string][]ssh.PublicKey
}

func newFakeBastion() *fakeBastion {
	return &fakeBastion{targets: map[string]warpgate.Target{}, targetRoles: map[string][]string{}, roles: map[string]string{},
		roleUsers: map[string][]string{}, hostKeys: map[string][]ssh.PublicKey{},
		users: []warpgate.User{{ID: "w-ayse", Username: "ayse"}, {ID: "w-admin", Username: "admin"}}}
}

func (f *fakeBastion) EnsureTarget(_ context.Context, t warpgate.Target, id, mark string) (warpgate.Result, error) {
	if _, ok := f.targets[id]; !ok {
		id = ""
		for k, x := range f.targets {
			if x.Name == t.Name {
				if !strings.Contains(x.Description, mark) {
					return warpgate.Result{}, warpgate.ErrTaken
				}
				id = k
			}
		}
	}
	if id == "" {
		id = fmt.Sprintf("t%d", len(f.targets)+1)
		t.ID = id
		f.targets[id] = t
		return warpgate.Result{ID: id, Action: "create"}, nil
	}
	t.ID = id
	if f.targets[id] == t {
		return warpgate.Result{ID: id, Action: "same"}, nil
	}
	f.targets[id] = t
	return warpgate.Result{ID: id, Action: "update"}, nil
}

func (f *fakeBastion) Target(_ context.Context, id string) (warpgate.Target, error) {
	t, ok := f.targets[id]
	if !ok {
		return warpgate.Target{}, warpgate.ErrNotFound
	}
	return t, nil
}

func (f *fakeBastion) TargetRoles(_ context.Context, id string) ([]string, error) {
	var out []string
	for _, rid := range f.targetRoles[id] {
		for name, x := range f.roles {
			if x == rid {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

func (f *fakeBastion) DeleteTarget(_ context.Context, id string) (bool, error) {
	_, ok := f.targets[id]
	delete(f.targets, id)
	return ok, nil
}

func (f *fakeBastion) EnsureRole(_ context.Context, name string) (string, error) {
	if _, ok := f.roles[name]; !ok {
		f.roles[name] = "r-" + name
	}
	return f.roles[name], nil
}

func (f *fakeBastion) SetTargetRoles(_ context.Context, target string, roles []string) ([]string, error) {
	f.targetRoles[target] = roles
	return nil, nil
}

func (f *fakeBastion) Users(context.Context) ([]warpgate.User, error) { return f.users, nil }

func (f *fakeBastion) SetRoleUsers(_ context.Context, role string, users []warpgate.User) ([]string, []string, error) {
	var names []string
	for _, u := range users {
		names = append(names, u.Username)
	}
	sort.Strings(names)
	f.roleUsers[role] = names
	return names, nil, nil
}

func (f *fakeBastion) OwnKeys(context.Context) ([]string, error) {
	return []string{"ssh-ed25519 WGKEY warpgate"}, nil
}

func (f *fakeBastion) SetHostKeys(_ context.Context, host string, _ int, keys []ssh.PublicKey) (bool, error) {
	f.hostKeys[host] = keys
	return true, nil
}

func TestApplyThroughTheBastion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	for _, name := range []string{"ayse", "mehmet"} {
		id := store.NewUserID()
		if err := q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id, Username: name, Provider: "corp", Subject: pgText("g-" + name), DirectoryName: pgText(name), Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: "team-platform", Source: "ad:corp"}); err != nil {
			t.Fatal(err)
		}
	}
	wg := newFakeBastion()
	e.deps.Bastion = func(endpoint, token string, insecure bool, _ []byte) (Bastion, error) {
		if endpoint != "https://wg.lab.test:8888" || token != "wgt" || !insecure {
			return nil, fmt.Errorf("bastion %s %s %v", endpoint, token, insecure)
		}
		return wg, nil
	}
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/warpgate"] = map[string]string{"token": "wgt", "insecure": "true"}
	e.platform = map[string]func(string) string{
		"platform/providers/wg.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: wg }\nspec: { type: warpgate, endpoint: \"https://wg.lab.test:8888\", credentials: bao://kv/platform/warpgate }\n"
		},
		"platform/platform.yaml": func(c string) string {
			return strings.Replace(c, "\nspec:\n", "\nspec:\n  bastion: { provider: wg }\n", 1)
		},
	}
	withAccess := func(sudo bool) func(string) string {
		return func(c string) string {
			return strings.Replace(c, "  baseline: linux-base\n", fmt.Sprintf("  baseline: linux-base\n  access:\n    - { team: team-platform, sudo: %v }\n", sudo), 1) +
				"---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-platform }\nspec: {}\n"
		}
	}
	p, err := e.apply(withAccess(true))
	if err != nil {
		t.Fatal(err)
	}
	if b := p.Contracts[0].Bastion; b == nil || *b != (plan.Bastion{Provider: "wg", Kind: "ssh", Sudo: true}) {
		t.Fatalf("plan bastion = %+v", b)
	}
	hosts, _ := q.LiveHosts(ctx, "poc-web")
	uid := hosts[0].Uid
	main, _ := e.externalIDOf(uid, "wg", "target")
	sudo, _ := e.externalIDOf(uid, "wg", "target-sudo")
	desc := "Managed by Gaia. gaia.uid=" + uid + " contract=poc-web"
	if got := wg.targets[main]; got != (warpgate.Target{ID: main, Name: "labtpoc01", Description: desc, Kind: "ssh", Host: "10.50.0.100", Port: 22, Username: "gaia-wg"}) {
		t.Errorf("target = %+v", got)
	}
	if got := wg.targets[sudo]; got.Name != "labtpoc01-sudo" || got.Username != "gaia-wg-sudo" {
		t.Errorf("sudo target = %+v", got)
	}
	if r := wg.targetRoles[main]; len(r) != 1 || r[0] != "r-gaia/poc-web" || wg.targetRoles[sudo][0] != "r-gaia/poc-web/sudo" {
		t.Errorf("target roles = %v", wg.targetRoles)
	}
	if u := wg.roleUsers["r-gaia/poc-web"]; strings.Join(u, ",") != "ayse" || strings.Join(wg.roleUsers["r-gaia/poc-web/sudo"], ",") != "ayse" {
		t.Errorf("role users = %v (mehmet has no Warpgate user)", wg.roleUsers)
	}
	if k := wg.hostKeys["10.50.0.100"]; len(k) != 1 || k[0].Type() != ssh.KeyAlgoED25519 {
		t.Errorf("host keys = %v", k)
	}
	site := e.configure.projects[0][".gaia/site.yml"]
	if !strings.Contains(site, `"name": "gaia-wg"`) || !strings.Contains(site, `"name": "gaia-wg-sudo"`) || !strings.Contains(site, "ssh-ed25519 WGKEY warpgate") {
		t.Errorf("site.yml lacks the bastion accounts:\n%s", site)
	}

	// Without sudo the sudo target goes; without access every target.
	// What the change flow records after a run: whether the access was
	// there decides whether it is taken back.
	applied := func(p *plan.Plan, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(p)
		if err := q.SetApplied(ctx, db.SetAppliedParams{Contract: "poc-web", AppliedPlanHash: pgText(p.Hash), AppliedPlan: data}); err != nil {
			t.Fatal(err)
		}
	}
	applied(p, nil)
	applied(e.apply(withAccess(false)))
	if _, ok := wg.targets[sudo]; ok || len(wg.targets) != 1 {
		t.Errorf("targets without sudo = %+v", wg.targets)
	}
	if _, err := e.apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(wg.targets) != 0 {
		t.Errorf("targets without access = %+v", wg.targets)
	}
	if id, _ := e.externalIDOf(uid, "wg", "target"); id != "" {
		t.Errorf("target ID still recorded: %s", id)
	}

	// Decommissioning deletes the target and the host keys.
	if _, err := e.apply(withAccess(false)); err != nil || len(wg.targets) != 1 {
		t.Fatalf("again: %v %+v", err, wg.targets)
	}
	if _, err := e.apply(func(c string) string { return withAccess(false)(c + "  lifecycle: { state: decommissioned }\n") }); err != nil {
		t.Fatal(err)
	}
	if len(wg.targets) != 0 || len(wg.hostKeys["10.50.0.100"]) != 0 {
		t.Errorf("after decommission: %+v %v", wg.targets, wg.hostKeys)
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

func TestAppArtifactIsChecked(t *testing.T) {
	e := setup(t)
	var asked []string
	e.deps.Artifact = func(_ context.Context, endpoint, user, pw, repo, path string) (nexus.Asset, error) {
		asked = append(asked, endpoint+" "+user+":"+pw+" "+repo+"/"+path)
		if path != "portal/portal-2.4.1.tar.gz" {
			return nexus.Asset{}, fmt.Errorf("%w: %s in %s", nexus.ErrNoArtifact, path, repo)
		}
		return nexus.Asset{URL: endpoint + "/repository/" + repo + "/" + path, SHA256: "d730d8f9"}, nil
	}
	e.deps.Secrets.(fakeSecrets)["bao://kv/platform/nexus"] = map[string]string{"username": "gaia", "password": "pw"}
	e.deps.Secrets.(fakeSecrets)["bao://kv/apps/team-platform/portal"] = map[string]string{"db_password": "x"}
	e.platform = map[string]func(string) string{
		"platform/providers/nexus.yaml": func(string) string {
			return "apiVersion: gaia/v1alpha1\nkind: Provider\nmetadata: { name: nexus }\nspec: { type: nexus, endpoint: \"https://nexus.lab.test\", credentials: bao://kv/platform/nexus }\n"
		},
	}
	withApp := func(version string) func(string) string {
		return func(c string) string {
			apps := strings.Replace(agentsAndApps, "  playbook: apps/portal.yml\n", "  playbook: apps/portal.yml\n  artifact: { provider: nexus, repository: releases, path: \"portal/portal-${version}.tar.gz\" }\n", 1)
			return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  ansibleRoles: [web.tomcat]\n  apps:\n    - name: portal\n      params: { version: "+version+", dbPassword: \"bao://kv/apps/team-platform/portal#db_password\" }\n", 1) + apps
		}
	}
	p, err := e.apply(withApp("2.4.1"))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Contracts[0].Apps[0].Source; s == nil || *s != (plan.ArtifactSource{Provider: "nexus", Repository: "releases", Path: "portal/portal-2.4.1.tar.gz"}) {
		t.Errorf("source = %+v", s)
	}
	if len(asked) != 1 || asked[0] != "https://nexus.lab.test gaia:pw releases/portal/portal-2.4.1.tar.gz" {
		t.Errorf("asked = %v", asked)
	}
	site := e.configure.projects[0][".gaia/site.yml"]
	if !strings.Contains(site, `"artifact": "https://nexus.lab.test/repository/releases/portal/portal-2.4.1.tar.gz"`) || !strings.Contains(site, `"sha256": "d730d8f9"`) {
		t.Errorf("site.yml = %s", site)
	}

	// A version that is not in Nexus touches no server.
	jobs := len(e.configure.jobs)
	if _, err := e.apply(withApp("2.5.0")); err == nil || !strings.Contains(err.Error(), "app portal: nexus: no such artifact: portal/portal-2.5.0.tar.gz in releases") {
		t.Fatalf("missing artifact: %v", err)
	}
	if len(e.configure.jobs) != jobs {
		t.Error("a server was configured without its artifact")
	}
}

func TestApplyRunsAgentsAndAppsWithSecrets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.deps.Secrets.(fakeSecrets)["bao://kv/apps/team-platform/portal"] = map[string]string{"db_password": "s3cret-db"}
	g := e.graph(func(c string) string {
		return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  ansibleRoles: [web.tomcat]\n  agents: [bigfix]\n  apps:\n    - name: portal\n      params: { version: 2.4.1, dbPassword: \"bao://kv/apps/team-platform/portal#db_password\" }\n  access:\n    - { team: team-platform }\n", 1) + agentsAndApps
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

	// An access run deploys no agents or apps.
	applied, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunAccess(ctx, g, applied, e.deps); err != nil {
		t.Fatal(err)
	}
	if site := e.configure.projects[1][".gaia/site.yml"]; strings.Contains(site, "apps/portal.yml") || strings.Contains(site, "corp.agents") || !strings.Contains(site, "gaia_access") {
		t.Errorf("access run site.yml = %s", site)
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

const replaceOp = `---
apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: replace-poc02 }
spec:
  action: replace
  selector: 'contract == "poc-web"'
  hosts: [labtpoc02]
`

func TestReplaceOperation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	day2 := &fakeDayTwo{}
	e.deps.Compute = func(context.Context, string, string, map[string]string) (compute.Compute, error) { return day2, nil }
	var forgot []string
	e.deps.Forget = func(_ context.Context, _ pulumi.Backend, st pulumi.Stack, uids []string) ([]string, error) {
		forgot = append(forgot, st.Name+" "+strings.Join(uids, ","))
		for _, uid := range uids {
			delete(e.compute.vms, st.Name+"/"+uid)
		}
		return uids, nil
	}
	day2.onDelete = func(string) {}
	edit := func(c string) string { return strings.Replace(c, "replicas: 1", "replicas: 2", 1) + replaceOp }
	g := e.graph(edit)
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	op, err := plan.BuildOperation(ctx, g, "replace-poc02", StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	// Not applied yet as far as the change flow knows: refused.
	if _, out, _ := RunOperation(ctx, g, op, e.deps); out.Results[0].Result != "failed" || !strings.Contains(out.Results[0].Detail, "has not been applied") {
		t.Fatalf("before it is applied: %+v", out)
	}
	applied, _ := plan.Build(ctx, g, nil, StoreState{S: e.store})
	data, _ := json.Marshal(applied)
	if err := q.SetApplied(ctx, db.SetAppliedParams{Contract: "poc-web", AppliedPlanHash: pgText(applied.Hash), AppliedPlan: data}); err != nil {
		t.Fatal(err)
	}
	hosts := mustLive(t, e)
	keep, old := hosts[0], hosts[1]
	oldVMID := ""
	for _, x := range must(q.ExternalIDs(ctx, old.Uid)) {
		if x.Kind == "vm" {
			oldVMID = x.ExternalID
		}
	}
	jobs := len(e.configure.jobs)

	_, out, err := RunOperation(ctx, g, op, e.deps)
	if err != nil || out.Status != "succeeded" {
		t.Fatalf("outcome = %+v %v", out, err)
	}
	hosts = mustLive(t, e)
	if len(hosts) != 2 || hosts[0].Uid != keep.Uid || hosts[1].Hostname != "labtpoc02" || hosts[1].Uid == old.Uid || hosts[1].Seq != 2 {
		t.Fatalf("hosts after = %+v", hosts)
	}
	fresh := hosts[1].Uid
	if h, _ := q.Host(ctx, old.Uid); h.Status != db.HostStatusDeleted {
		t.Errorf("old host = %s", h.Status)
	}
	ips := map[string]string{}
	for _, uid := range []string{old.Uid, fresh} {
		for _, x := range must(q.ExternalIDs(ctx, uid)) {
			if x.Kind == "ip" {
				ips[uid] = x.ExternalID
			}
		}
	}
	if ips[fresh] != "10.50.0.101" || ips[old.Uid] != "" {
		t.Errorf("addresses = %v", ips)
	}
	if a, _ := ipamclient.New(e.ipam, "ipam-token").Allocations(ctx, fresh); len(a) != 1 || a[0].IP != "10.50.0.101" {
		t.Errorf("IPAM = %+v", a)
	}
	if strings.Join(forgot, ";") != "poc-web-lab "+old.Uid || strings.Join(day2.calls, "|") != oldVMID+" delete VM" {
		t.Errorf("forgot %v, calls %v", forgot, day2.calls)
	}
	if strings.Join(e.monitor.deleted, ",") != old.Uid {
		t.Errorf("zabbix deleted %v", e.monitor.deleted)
	}
	// Only the new server was configured.
	if n := len(e.configure.jobs) - jobs; n != 1 {
		t.Fatalf("%d configure jobs", n)
	}
	if h := e.configure.jobs[len(e.configure.jobs)-1].Hosts; len(h) != 1 || h[0].Name != fresh {
		t.Errorf("configured %+v", h)
	}
	if last := e.compute.stacks[len(e.compute.stacks)-1].VMs; len(last) != 2 || last[1].UID != fresh || last[1].Address != "10.50.0.101/24" {
		t.Errorf("stack = %+v", last)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestPatchOperationWithSnapshotAndMaintenance(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fc := &fakeDayTwo{}
	e.deps.Compute = func(context.Context, string, string, map[string]string) (compute.Compute, error) { return fc, nil }
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
	if len(fc.calls) != 2 || !strings.Contains(fc.calls[0], " snapshot gaia_patch_poc_") {
		t.Errorf("snapshots = %v", fc.calls)
	}
	if len(e.monitor.maintenances) != 2 || !strings.HasPrefix(e.monitor.maintenances[0], "start Gaia: patch-poc, batch 1 ") || e.monitor.maintenances[1] != "end m0" {
		t.Errorf("maintenance = %v", e.monitor.maintenances)
	}
	site := e.configure.projects[len(e.configure.projects)-1][".gaia/site.yml"]
	if !strings.Contains(site, `"security": true`) || !strings.Contains(site, "needs-restarting -r") {
		t.Errorf("patch play = %s", site)
	}

	// A snapshot that fails fails its server, which then is not patched.
	fc.err = fmt.Errorf("storage full")
	jobs := len(e.configure.jobs)
	_, out, _ = RunOperation(ctx, g, o, e.deps)
	if out.Status != "failed" || !strings.Contains(out.Results[0].Detail, "storage full") || len(e.configure.jobs) != jobs {
		t.Errorf("after a failed snapshot: %+v, %d jobs", out, len(e.configure.jobs)-jobs)
	}
}

// testHostKey is a server's host key in tests, derived from its address.
func testHostKey(addr string) ssh.PublicKey {
	seed := sha256.Sum256([]byte(addr))
	k, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed[:]).Public())
	return k
}

// fakeDayTwo records day-2 calls.
type fakeDayTwo struct {
	calls    []string
	err      error
	onDelete func(vm string)
	vms      []compute.ObservedVM
}

func (f *fakeDayTwo) Delete(_ context.Context, vm string) error {
	f.calls = append(f.calls, vm+" delete VM")
	if f.onDelete != nil {
		f.onDelete(vm)
	}
	return f.err
}

func (f *fakeDayTwo) Power(_ context.Context, vm, action string) error {
	f.calls = append(f.calls, vm+" power "+action)
	return f.err
}

func (f *fakeDayTwo) Snapshot(_ context.Context, vm, name, _ string) error {
	f.calls = append(f.calls, vm+" snapshot "+name)
	return f.err
}

func (f *fakeDayTwo) Revert(_ context.Context, vm, name string) error {
	f.calls = append(f.calls, vm+" revert "+name)
	return f.err
}

func (f *fakeDayTwo) DeleteSnapshot(_ context.Context, vm, name string) error {
	f.calls = append(f.calls, vm+" delete "+name)
	return f.err
}

func (f *fakeDayTwo) Discover(context.Context) ([]compute.ObservedVM, error) { return f.vms, f.err }

func TestPowerAndRevertOperations(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fc := &fakeDayTwo{}
	e.deps.Compute = func(_ context.Context, kind, _ string, sec map[string]string) (compute.Compute, error) {
		if kind != "proxmox" || sec["token_id"] == "" {
			return nil, fmt.Errorf("compute %s %v", kind, sec)
		}
		return fc, nil
	}
	ops := `---
apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: off }
spec: { action: power, selector: 'true', options: { state: "off" } }
---
apiVersion: gaia/v1alpha1
kind: Operation
metadata: { name: back }
spec: { action: revert, selector: 'true', options: { snapshot: before } }
`
	g := e.graph(func(c string) string { return c + ops })
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, g, p, e.deps); err != nil {
		t.Fatal(err)
	}
	jobs := len(e.configure.jobs)
	off, _ := plan.BuildOperation(ctx, g, "off", StoreState{S: e.store})
	if _, out, err := RunOperation(ctx, g, off, e.deps); err != nil || out.Status != "succeeded" || len(e.configure.jobs) != jobs {
		t.Fatalf("power off: %+v %v (%d jobs)", out, err, len(e.configure.jobs)-jobs)
	}
	back, _ := plan.BuildOperation(ctx, g, "back", StoreState{S: e.store})
	if _, out, err := RunOperation(ctx, g, back, e.deps); err != nil || out.Status != "succeeded" || len(e.configure.jobs) != jobs+1 {
		t.Fatalf("revert: %+v %v", out, err)
	}
	if len(fc.calls) != 2 || !strings.HasSuffix(fc.calls[0], " power off") || !strings.HasSuffix(fc.calls[1], " revert before") {
		t.Errorf("calls = %v", fc.calls)
	}
	if !strings.Contains(e.configure.projects[len(e.configure.projects)-1][".gaia/site.yml"], "wait_for_connection") {
		t.Error("no health check after the revert")
	}
}

func TestPatchPlayNeverPassesANullExclude(t *testing.T) {
	// The dnf module refuses exclude: null (found in the lab).
	for _, opts := range []map[string]any{nil, {"exclude": []any{"kernel*"}}} {
		data, err := json.Marshal(patchPlay(opts))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"exclude":null`) {
			t.Errorf("options %v: %s", opts, data)
		}
	}
}

// Sudo for a few commands goes to the access role as a list, gives
// read-only monitoring, and the run record names the commands.
func TestApplyLimitedSudo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := e.store.Q()
	for _, name := range []string{"yigit", "mehmet"} {
		id := store.NewUserID()
		if err := q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{ID: id, Username: name, Provider: "corp", Subject: pgText("g-" + name), DirectoryName: pgText(name), Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: "team-web-ops", Source: "ad:corp"}); err != nil {
			t.Fatal(err)
		}
	}
	g := e.graph(func(c string) string {
		return strings.Replace(c, "  baseline: linux-base\n", "  baseline: linux-base\n  access:\n    - team: team-web-ops\n      sudo: [/usr/bin/systemctl restart nginx, /usr/bin/systemctl restart podman]\n", 1) +
			"---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-web-ops }\nspec: {}\n---\napiVersion: gaia/v1alpha1\nkind: Team\nmetadata: { name: team-platform }\nspec: {}\n"
	})
	p, err := plan.Build(ctx, g, nil, StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Run(ctx, g, p, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	site := e.configure.projects[0][".gaia/site.yml"]
	var doc []struct {
		Roles []struct {
			Role string
			Vars map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(site), &doc); err != nil {
		t.Fatalf("site.yml: %v\n%s", err, site)
	}
	var users []access.User
	for _, play := range doc {
		for _, r := range play.Roles {
			if r.Role == access.Role {
				_ = json.Unmarshal(r.Vars["gaia_access_users"], &users)
			}
		}
	}
	want := "/usr/bin/systemctl restart nginx|/usr/bin/systemctl restart podman"
	if len(users) != 2 || users[0].Sudo || strings.Join(users[0].Commands, "|") != want || strings.Join(users[1].Commands, "|") != want {
		t.Errorf("access users = %+v", users)
	}
	ug := e.monitor.userGroups["Gaia/team-web-ops"]
	if ug.rights["Gaia/contract/poc-web"] != zabbix.PermissionRead {
		t.Errorf("monitoring rights = %+v, want read-only", ug.rights)
	}
	rec, err := q.RunRecord(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	var body runrecord.Record
	_ = json.Unmarshal([]byte(rec.Body), &body)
	if s := body.AccessSnapshot; len(s) != 1 || s[0].Sudo || strings.Join(s[0].SudoCommands, "|") != want {
		t.Errorf("access snapshot = %+v", s)
	}
}

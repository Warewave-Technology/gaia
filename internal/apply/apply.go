package apply

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/compute"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/plan"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Secrets reads bao:// references.
type Secrets interface {
	Read(ctx context.Context, ref string) (map[string]string, error)
	Addr() string
	Token() string
}

// Deps are what a run talks to. The CLI fills them with the real systems;
// tests replace provisioning and the Proxmox API.
type Deps struct {
	Store     *store.Store
	Secrets   Secrets
	Provision func(ctx context.Context, b pulumi.Backend, st pulumi.Stack, w io.Writer) (map[string]pulumi.Result, pulumi.Changes, error)
	UsedVMIDs func(ctx context.Context, c proxmox.Credentials) (map[int]bool, error)
	// ScanHostKey reads the SSH host key a server offers (ScanHostKey).
	ScanHostKey func(ctx context.Context, addr, algorithm string) (ssh.PublicKey, error)
	// Compute is a hypervisor's day-2 API (power, snapshots), for
	// Operations: kind is the provider's type, secret its credentials.
	Compute   func(ctx context.Context, kind, endpoint string, secret map[string]string) (compute.Compute, error)
	Configure Configurer
	Monitor   func(endpoint, token string) Monitor
	// Archive opens the run record archive (Platform.runArchive).
	Archive func(endpoint, bucket, accessKey, secretKey string) (runrecord.Archive, error)
	// RunID is a queued run to take; empty starts a new run.
	RunID string
	// Config is the config repository pinned for the run; without it the
	// config provider's ref is read at the start of the run.
	Config *ConfigRepo
	// Who asked for the run and where its contracts came from, for the
	// run record.
	RequestedBy string
	// ApprovedBy are who approved the change the run applies.
	ApprovedBy []string
	Source     runrecord.Source
	Out        io.Writer
}

// User is the account cloud-init creates on every server for Gaia.
const User = "gaia"

// CheckApproval refuses a plan that changes servers in an environment
// that needs approval, unless the run applies an approved change: approval
// is given in Gaia's change flow (design 7.6), never by running gaia
// apply.
func CheckApproval(p *plan.Plan, approvedBy []string) error {
	if len(approvedBy) > 0 {
		return nil
	}
	for _, c := range p.Contracts {
		if c.Approval != "required" {
			continue
		}
		for _, s := range c.Servers {
			if s.Action != plan.Keep {
				return fmt.Errorf("%s: the plan changes servers and needs approval; approve its change in gaia server", c.Name)
			}
		}
	}
	return nil
}

// Run applies a plan built from g. A plan that needs approval runs only
// with the approvals of its change (CheckApproval).
func Run(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) (string, error) {
	if err := CheckApproval(p, d.ApprovedBy); err != nil {
		return "", err
	}
	return run(ctx, g, p, d, func(ctx context.Context, r *runner) error { return r.all(ctx, p) })
}

// RunAccess distributes the access of a plan already applied again
// (design 14.5): the people of its teams changed, or their keys. Nothing
// is provisioned; only the access play runs on the contract's live
// servers. p must be the plan last applied, never one waiting for
// approval.
func RunAccess(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps) (string, error) {
	return run(ctx, g, p, d, func(ctx context.Context, r *runner) error { return r.accessOnly(ctx, p) })
}

func run(ctx context.Context, g *catalog.Graph, p *plan.Plan, d Deps, do func(context.Context, *runner) error) (string, error) {
	q := d.Store.Q()
	runID := d.RunID
	if runID == "" {
		runID = store.NewRunID()
		if err := q.StartRun(ctx, db.StartRunParams{ID: runID, Contracts: p.ContractNames(), Commit: p.Commit, PlanHash: p.Hash, RequestedBy: pgText(d.RequestedBy)}); err != nil {
			return "", err
		}
	} else {
		n, err := q.TakeRun(ctx, runID)
		if err != nil {
			return runID, err
		}
		if n != 1 {
			return runID, fmt.Errorf("run %s is not queued", runID)
		}
	}
	r := &runner{g: g, d: d, run: runID, used: map[string]map[int]bool{}, monitors: map[string]Monitor{},
		executedAs: map[string]string{"linux": User}, started: time.Now().UTC(), events: newEvents(d.Store, runID)}
	err := do(ctx, r)
	status, msg := "succeeded", ""
	switch {
	case err != nil && ctx.Err() != nil:
		status, msg = "cancelled", err.Error()
	case err != nil:
		status, msg = "failed", err.Error()
	}
	// The run's outcome is recorded even when its context ended.
	ctx = context.WithoutCancel(ctx)
	// The run is marked finished last: whoever sees it finished finds its
	// record and every event (the event stream ends on it).
	if rerr := r.stage(ctx, "", "record", func() error { return r.writeRecord(ctx, p, status, msg) }); rerr != nil {
		err = errors.Join(err, rerr)
	}
	if eerr := r.events.close(); eerr != nil {
		err = errors.Join(err, fmt.Errorf("run events: %w", eerr))
	}
	if lerr := r.archiveLogs(ctx); lerr != nil {
		err = errors.Join(err, fmt.Errorf("run logs: %w (the events are in the database)", lerr))
	}
	if ferr := q.FinishRun(ctx, db.FinishRunParams{ID: runID, Status: status, Error: pgText(msg)}); ferr != nil {
		err = errors.Join(err, ferr)
	}
	return runID, err
}

type runner struct {
	g        *catalog.Graph
	d        Deps
	run      string
	used     map[string]map[int]bool // VM IDs in use, per compute provider
	cfg      *config                 // set when a contract has a baseline or roles
	monitors map[string]Monitor      // per monitoring provider
	events   *events
	// For the run record.
	started    time.Time
	changes    []runrecord.Change
	executedAs map[string]string // provider -> identity
	access     []runrecord.AccessGrant
}

func (r *runner) change(c runrecord.Change) { r.changes = append(r.changes, c) }

func (r *runner) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(r.out(), format, a...)
}

// out is where a run's output goes: its events, and Deps.Out when set.
func (r *runner) out() io.Writer {
	if r.d.Out == nil {
		return r.events
	}
	return io.MultiWriter(r.d.Out, r.events)
}

func (r *runner) all(ctx context.Context, p *plan.Plan) error {
	backend, err := r.backend(ctx)
	if err != nil {
		return err
	}
	sshKey, privateKey, err := r.sshKeys(ctx)
	if err != nil {
		return err
	}
	for _, c := range p.Contracts {
		if running(c) > 0 && r.configures(ctx, c) {
			if r.cfg, err = r.configSetup(ctx, privateKey); err != nil {
				return err
			}
			break
		}
	}
	for _, c := range p.Contracts {
		if len(c.Servers) == 0 {
			continue
		}
		err := r.d.Store.WithContractLock(ctx, c.Name, func(ctx context.Context) error {
			return r.contract(ctx, c, backend, sshKey)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

// accessOnly runs the access play of each contract on its live servers.
func (r *runner) accessOnly(ctx context.Context, p *plan.Plan) error {
	live := 0
	for _, c := range p.Contracts {
		live += running(c)
	}
	if live == 0 {
		r.printf("no servers; nothing to distribute\n")
		return nil
	}
	_, privateKey, err := r.sshKeys(ctx)
	if err != nil {
		return err
	}
	if r.cfg, err = r.configSetup(ctx, privateKey); err != nil {
		return err
	}
	for _, c := range p.Contracts {
		contract, ok := r.g.Catalog.Get(catalog.KindServer, c.Name)
		if !ok {
			return fmt.Errorf("%s: not in the catalog", c.Name)
		}
		var targets []target
		for _, s := range c.Servers {
			if s.Action == plan.Delete || s.UID == "" {
				continue
			}
			addr, err := r.address(ctx, s)
			if err != nil {
				return fmt.Errorf("%s: %w", s.Hostname, err)
			}
			targets = append(targets, target{s: s, address: strings.SplitN(addr, "/", 2)[0]})
		}
		only := c
		only.Baseline, only.Roles = nil, nil
		err := r.d.Store.WithContractLock(ctx, c.Name, func(ctx context.Context) error {
			return r.stage(ctx, c.Name, "access", func() error {
				if err := r.configure(ctx, only, contract, targets); err != nil {
					return err
				}
				return r.monitorAccess(ctx, c)
			})
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

// backend is where Pulumi state lives (Platform.engines.provisioning.state)
// and how its secrets are encrypted (Platform.secrets).
func (r *runner) backend(ctx context.Context) (pulumi.Backend, error) {
	ps := r.g.Platform().Spec.(*catalog.PlatformSpec)
	st := r.provider(ps.Engines.Provisioning.State)
	creds, err := r.d.Secrets.Read(ctx, st.Credentials)
	if err != nil {
		return pulumi.Backend{}, err
	}
	b := pulumi.Backend{
		SecretsProvider: "hashivault://pulumi",
		Settings:        StackSettings{S: r.d.Store},
		Env: map[string]string{
			"VAULT_ADDR":               r.d.Secrets.Addr(),
			"VAULT_TOKEN":              r.d.Secrets.Token(),
			"PULUMI_SKIP_UPDATE_CHECK": "true",
		},
	}
	u, err := url.Parse(st.Endpoint)
	if err != nil || u.Host == "" {
		return pulumi.Backend{}, fmt.Errorf("state endpoint %q: want a URL", st.Endpoint)
	}
	switch st.Type {
	case "postgres":
		// postgres://host:port/database?sslmode=...; the role comes from
		// the credentials.
		u.User = url.UserPassword(creds["username"], creds["password"])
		b.URL = u.String()
	case "s3":
		// gocloud's s3blob (AWS SDK v2): the endpoint's scheme decides
		// between http and https. SeaweedFS lists an emptied directory as
		// an object, which breaks Pulumi's lock check; use a store that
		// does not (design 13).
		q := url.Values{}
		q.Set("endpoint", st.Endpoint)
		q.Set("region", "us-east-1")
		q.Set("use_path_style", "true")
		b.URL = "s3://" + st.Bucket + "?" + q.Encode()
		b.Env["AWS_ACCESS_KEY_ID"] = creds["access_key"]
		b.Env["AWS_SECRET_ACCESS_KEY"] = creds["secret_key"]
		b.Env["AWS_REGION"] = "us-east-1"
	default:
		return pulumi.Backend{}, fmt.Errorf("state provider type %s", st.Type)
	}
	return b, nil
}

// sshKeys returns Gaia's key pair: cloud-init gets the public key, Ansible
// the private one.
func (r *runner) sshKeys(ctx context.Context) (public, private string, err error) {
	ref := r.g.Platform().Spec.(*catalog.PlatformSpec).Engines.Configuration.SSHKey
	if ref == "" {
		return "", "", errors.New("no engines.configuration.sshKey in the Platform: servers would be unreachable")
	}
	f, err := r.d.Secrets.Read(ctx, ref)
	if err != nil {
		return "", "", err
	}
	if f["public"] == "" || f["private"] == "" {
		return "", "", fmt.Errorf("%s: want private and public", ref)
	}
	return strings.TrimSpace(f["public"]), f["private"], nil
}

func (r *runner) provider(name string) *catalog.ProviderSpec {
	o, _ := r.g.Catalog.Get(catalog.KindProvider, name)
	return o.Spec.(*catalog.ProviderSpec)
}

// work is what one contract's run does, gathered while preparing it.
type work struct {
	bySite    map[string][]pulumi.VM
	siteCreds map[string]proxmox.Credentials // a site has one compute provider
	creds     map[string]proxmox.Credentials
	siteVS    map[string]*pulumi.VSphereCredentials // vSphere sites
	vsCreds   map[string]*pulumi.VSphereCredentials
	targets   []target
	gone      []doomed
}

// contract runs one contract's stages in the order of design 7.2:
// addresses and IDs, deregistering what goes, provisioning, releasing what
// went, registering, configuring.
func (r *runner) contract(ctx context.Context, c plan.Contract, backend pulumi.Backend, sshKey string) error {
	contract, _ := r.g.Catalog.Get(catalog.KindServer, c.Name)
	w := &work{bySite: map[string][]pulumi.VM{}, siteCreds: map[string]proxmox.Credentials{}, creds: map[string]proxmox.Credentials{},
		siteVS: map[string]*pulumi.VSphereCredentials{}, vsCreds: map[string]*pulumi.VSphereCredentials{}}
	if err := r.stage(ctx, c.Name, "ipam", func() error { return r.prepare(ctx, c, contract, w, sshKey) }); err != nil {
		return err
	}
	if len(w.gone) > 0 {
		// Monitoring goes first, so that a VM going away raises no alerts.
		if err := r.stage(ctx, c.Name, "deregister", func() error { return r.unregister(ctx, c, w.gone) }); err != nil {
			return err
		}
	}
	if err := r.stage(ctx, c.Name, "provision", func() error { return r.provision(ctx, c, backend, w) }); err != nil {
		return err
	}
	if len(w.gone) > 0 {
		if err := r.stage(ctx, c.Name, "release", func() error { return r.release(ctx, c, w.gone, w.creds) }); err != nil {
			return err
		}
	}
	if c.Monitoring != nil && len(w.targets) > 0 {
		if err := r.stage(ctx, c.Name, "register", func() error { return r.register(ctx, c, contract, w.targets) }); err != nil {
			return err
		}
	}
	if r.configures(ctx, c) && len(w.targets) > 0 {
		if err := r.stage(ctx, c.Name, "configure", func() error { return r.configure(ctx, c, contract, w.targets) }); err != nil {
			return err
		}
	}
	if c.Monitoring != nil && r.managesAccess(ctx, c) {
		return r.stage(ctx, c.Name, "access", func() error { return r.monitorAccess(ctx, c) })
	}
	return nil
}

// prepare records new hosts and gives every server its address, VM ID
// and SMBIOS UUID; servers that go are marked decommissioning.
func (r *runner) prepare(ctx context.Context, c plan.Contract, contract *catalog.Object, w *work, sshKey string) error {
	q := r.d.Store.Q()
	for _, s := range c.Servers {
		r.change(runrecord.Change{Contract: c.Name, UID: s.UID, Hostname: s.Hostname, Stage: "plan", Action: s.Action})
		if s.Action == plan.Delete {
			d, err := r.doom(ctx, s)
			if err != nil {
				return err
			}
			w.gone = append(w.gone, d)
			if d.compute != "" {
				// The stack runs even when no server of the site is left:
				// that is how its VMs are destroyed.
				if _, ok := w.bySite[s.Site]; !ok {
					w.bySite[s.Site] = nil
				}
				if err := r.siteHypervisor(ctx, w, s.Site, d.compute); err != nil {
					return err
				}
			}
			continue
		}
		if s.Action == plan.Create {
			s.UID = store.NewUID()
			if err := r.d.Store.CreateHost(ctx, store.NewHost{
				UID: s.UID, Contract: c.Name, Hostname: s.Hostname, Site: s.Site, Network: s.Network, Seq: s.Seq, Run: r.run,
			}); err != nil {
				return fmt.Errorf("%s: %w (plan again)", s.Hostname, err)
			}
			r.printf("  %s: uid %s\n", s.Hostname, s.UID)
			r.changes[len(r.changes)-1].UID = s.UID
		}

		if err := r.siteHypervisor(ctx, w, s.Site, s.Compute.Provider); err != nil {
			return err
		}
		addr, err := r.address(ctx, s)
		if err != nil {
			return err
		}
		if w.siteVS[s.Site] != nil {
			// vSphere names its VMs by UUID, recorded once the VM exists.
			w.bySite[s.Site] = append(w.bySite[s.Site], r.vsphereVM(c, s, addr, sshKey))
			w.targets = append(w.targets, target{s: s, address: strings.SplitN(addr, "/", 2)[0]})
			continue
		}
		cr := w.siteCreds[s.Site]
		vmid, err := r.vmid(ctx, s, cr)
		if err != nil {
			return err
		}
		smbios := SMBIOSUUID(s.UID)
		if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: s.UID, Provider: s.Compute.Provider, Kind: "smbios", ExternalID: smbios}); err != nil {
			return err
		}
		w.bySite[s.Site] = append(w.bySite[s.Site], r.vm(c, contract, s, addr, vmid, smbios, sshKey))
		w.targets = append(w.targets, target{s: s, address: strings.SplitN(addr, "/", 2)[0]})
	}
	return nil
}

// siteProvider is a site's compute provider.
func (r *runner) siteProvider(site string) string {
	o, _ := r.g.Catalog.Get(catalog.KindSite, site)
	return o.Spec.(*catalog.SiteSpec).Compute.Provider
}

// siteHypervisor records the credentials of a site's compute provider for
// its stack: Proxmox or vSphere.
func (r *runner) siteHypervisor(ctx context.Context, w *work, site, provider string) error {
	if r.provider(provider).Type == "vsphere" {
		vc, err := cached(w.vsCreds, provider, func() (*pulumi.VSphereCredentials, error) { return r.vsphereCreds(ctx, provider) })
		if err != nil {
			return err
		}
		w.siteVS[site] = vc
		return nil
	}
	cr, err := cached(w.creds, provider, func() (proxmox.Credentials, error) { return r.proxmoxCreds(ctx, provider) })
	if err != nil {
		return err
	}
	w.siteCreds[site] = cr
	return nil
}

// vsphereCreds reads a vsphere provider's credentials: username, password
// and insecure (a self-signed certificate).
func (r *runner) vsphereCreds(ctx context.Context, provider string) (*pulumi.VSphereCredentials, error) {
	p := r.provider(provider)
	f, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return nil, err
	}
	if f["username"] == "" || f["password"] == "" {
		return nil, fmt.Errorf("%s: username and password are required", p.Credentials)
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("%s: endpoint %q: want https://vcenter", provider, p.Endpoint)
	}
	insecure, _ := strconv.ParseBool(f["insecure"])
	r.executedAs[provider] = f["username"]
	return &pulumi.VSphereCredentials{Server: u.Hostname(), Username: f["username"], Password: f["password"], Insecure: insecure}, nil
}

// vsphereVM is a server as the vSphere program takes it.
func (r *runner) vsphereVM(c plan.Contract, s plan.Server, addr, sshKey string) pulumi.VM {
	site, _ := r.g.Catalog.Get(catalog.KindSite, s.Site)
	sc := site.Spec.(*catalog.SiteSpec).Compute
	net, _ := r.g.Catalog.Get(catalog.KindNetwork, s.Network)
	ns := net.Spec.(*catalog.NetworkSpec)
	img, _ := r.g.Catalog.Get(catalog.KindImage, c.Image)
	vm := pulumi.VM{
		UID: s.UID, Hostname: s.Hostname, CPU: c.Size.CPU, MemoryMiB: c.Size.MemoryGiB * 1024, DiskGiB: c.Size.DiskGiB,
		Address: addr, Gateway: s.IPAM.Gateway, Datacenter: sc.Datacenter, Cluster: sc.Cluster, Folder: sc.Folder,
		Portgroup: ns.Attachment.Portgroup, User: User, SSHKeys: []string{sshKey},
		CloudInit:   s.Compute.Customization == "cloud-init",
		Description: fmt.Sprintf("Managed by Gaia.\ngaia.uid=%s\ncontract=%s\n", s.UID, c.Name),
	}
	if sc.Storage != nil {
		vm.Datastore = sc.Storage.Default
	}
	for _, t := range img.Spec.(*catalog.ImageSpec).Templates {
		if t.Provider == s.Compute.Provider {
			vm.TemplateName = t.Template.Name
		}
	}
	for _, d := range c.Disks {
		vm.ExtraDisks = append(vm.ExtraDisks, d.SizeGiB)
	}
	if ns.DNS != nil {
		vm.DNS, vm.Domain = ns.DNS.Servers, ns.DNS.Zone
	}
	return vm
}

// provision runs one Pulumi stack per site and checks every VM: its ID
// and the address its guest agent reports.
func (r *runner) provision(ctx context.Context, c plan.Contract, backend pulumi.Backend, w *work) error {
	q := r.d.Store.Q()
	sites := make([]string, 0, len(w.bySite))
	for site := range w.bySite {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	for _, site := range sites {
		vms := w.bySite[site]
		stack := pulumi.Stack{Name: c.Name + "-" + site, Proxmox: w.siteCreds[site], VSphere: w.siteVS[site], VMs: vms}
		r.printf("  pulumi up %s (%d VM)\n", stack.Name, len(vms))
		got, changes, err := r.d.Provision(ctx, backend, stack, r.out())
		if err != nil {
			return err
		}
		r.printf("  %s: %s\n", stack.Name, changes)
		r.change(runrecord.Change{Contract: c.Name, Stage: "provision", Action: "pulumi up", Detail: stack.Name + ": " + changes.String()})
		for _, v := range vms {
			res := got[v.UID]
			if stack.VSphere != nil {
				if res.UUID == "" {
					return fmt.Errorf("%s: vSphere reports no UUID", v.Hostname)
				}
				if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: v.UID, Provider: r.siteProvider(site), Kind: "vm", ExternalID: res.UUID}); err != nil {
					return err
				}
			} else if res.VMID != v.VMID {
				return fmt.Errorf("%s: Proxmox reports VM ID %d, Gaia recorded %d", v.Hostname, res.VMID, v.VMID)
			}
			// A VM is active only when it answers with its address: Pulumi
			// can finish while cloud-init never configured the network.
			want := strings.SplitN(v.Address, "/", 2)[0]
			if !slices.Contains(res.IPv4, want) {
				return fmt.Errorf("%s: the guest agent reports %v, not %s; the VM is not configured", v.Hostname, res.IPv4, want)
			}
			if err := q.SetHostStatus(ctx, db.SetHostStatusParams{Uid: v.UID, Status: db.HostStatusActive}); err != nil {
				return err
			}
		}
	}
	return nil
}

// stage runs fn as one stage of the contract and records how it ended.
func (r *runner) stage(ctx context.Context, contract, name string, fn func() error) error {
	q := r.d.Store.Q()
	if err := q.StartStage(ctx, db.StartStageParams{RunID: r.run, Contract: contract, Stage: name}); err != nil {
		return err
	}
	r.events.setStage(name)
	err := fn()
	status, detail := "succeeded", ""
	switch {
	case err != nil && ctx.Err() != nil:
		status, detail = "cancelled", err.Error()
	case err != nil:
		status, detail = "failed", err.Error()
	}
	if ferr := q.FinishStage(context.WithoutCancel(ctx), db.FinishStageParams{RunID: r.run, Contract: contract, Stage: name, Status: status, Detail: pgText(detail)}); ferr != nil {
		err = errors.Join(err, ferr)
	}
	return err
}

// running counts the servers of c that stay.
func running(c plan.Contract) int {
	n := 0
	for _, s := range c.Servers {
		if s.Action != plan.Delete {
			n++
		}
	}
	return n
}

// cached returns m[k], filling it with load the first time.
func cached[V any](m map[string]V, k string, load func() (V, error)) (V, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	v, err := load()
	if err == nil {
		m[k] = v
	}
	return v, err
}

func (r *runner) proxmoxCreds(ctx context.Context, provider string) (proxmox.Credentials, error) {
	p := r.provider(provider)
	f, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return proxmox.Credentials{}, err
	}
	insecure, _ := strconv.ParseBool(f["insecure"])
	c := proxmox.Credentials{Endpoint: p.Endpoint, TokenID: f["token_id"], TokenSecret: f["token_secret"], Insecure: insecure}
	if c.TokenID == "" || c.TokenSecret == "" {
		return c, fmt.Errorf("%s: token_id and token_secret are required", p.Credentials)
	}
	r.executedAs[provider] = c.TokenID
	return c, nil
}

// address returns the server's address from IPAM in CIDR form; IPAM gives
// the same address for the same owner, so this is safe to repeat.
func (r *runner) address(ctx context.Context, s plan.Server) (string, error) {
	p := r.provider(s.IPAM.Provider)
	f, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return "", err
	}
	a, err := ipamclient.New(p.Endpoint, f["token"]).Allocate(ctx, s.IPAM.Pool, s.UID, s.Hostname)
	if err != nil {
		return "", fmt.Errorf("%s: %w", s.Hostname, err)
	}
	if err := r.d.Store.Q().SetExternalID(ctx, db.SetExternalIDParams{Uid: s.UID, Provider: s.IPAM.Provider, Kind: "ip", ExternalID: a.IP}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%d", a.IP, a.Prefix), nil
}

// vmid returns the server's VM ID: the one recorded, or the lowest free in
// the site's range, recorded before the VM exists so that a repeated run
// creates the same VM.
func (r *runner) vmid(ctx context.Context, s plan.Server, c proxmox.Credentials) (int, error) {
	q := r.d.Store.Q()
	ids, err := q.ExternalIDs(ctx, s.UID)
	if err != nil {
		return 0, err
	}
	for _, e := range ids {
		if e.Provider == s.Compute.Provider && e.Kind == "vm" {
			return strconv.Atoi(e.ExternalID)
		}
	}
	site, _ := r.g.Catalog.Get(catalog.KindSite, s.Site)
	rng := site.Spec.(*catalog.SiteSpec).Compute.VMIDs
	used, ok := r.used[s.Compute.Provider]
	if !ok {
		if used, err = r.d.UsedVMIDs(ctx, c); err != nil {
			return 0, err
		}
		r.used[s.Compute.Provider] = used
	}
	taken := map[int]bool{}
	held, err := q.ExternalIDsOfKind(ctx, db.ExternalIDsOfKindParams{Provider: s.Compute.Provider, Kind: "vm"})
	if err != nil {
		return 0, err
	}
	for _, h := range held {
		if n, err := strconv.Atoi(h); err == nil {
			taken[n] = true
		}
	}
	id, err := proxmox.FreeVMID(used, taken, rng.Start, rng.End)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", s.Hostname, err)
	}
	used[id] = true
	if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: s.UID, Provider: s.Compute.Provider, Kind: "vm", ExternalID: strconv.Itoa(id)}); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *runner) vm(c plan.Contract, contract *catalog.Object, s plan.Server, addr string, vmid int, smbios, sshKey string) pulumi.VM {
	site, _ := r.g.Catalog.Get(catalog.KindSite, s.Site)
	sc := site.Spec.(*catalog.SiteSpec).Compute
	net, _ := r.g.Catalog.Get(catalog.KindNetwork, s.Network)
	ns := net.Spec.(*catalog.NetworkSpec)
	img, _ := r.g.Catalog.Get(catalog.KindImage, c.Image)
	tpl := 0
	for _, t := range img.Spec.(*catalog.ImageSpec).Templates {
		if t.Provider == s.Compute.Provider {
			tpl = t.Template.VMID
		}
	}
	vm := pulumi.VM{
		UID: s.UID, Hostname: s.Hostname, VMID: vmid, Node: sc.Nodes[0], Pool: sc.Pool,
		Template: tpl, CPU: c.Size.CPU, MemoryMiB: c.Size.MemoryGiB * 1024, DiskGiB: c.Size.DiskGiB,
		Address: addr, Gateway: s.IPAM.Gateway, User: User, SSHKeys: []string{sshKey},
		Tags:        Tags(s.UID, c.Name, contract.Metadata.Labels),
		Description: fmt.Sprintf("Managed by Gaia.\ngaia.uid=%s\ncontract=%s\n", s.UID, c.Name),
		SMBIOSUUID:  smbios,
	}
	if sc.Storage != nil {
		vm.Storage = sc.Storage.Default
	}
	for _, d := range c.Disks {
		vm.ExtraDisks = append(vm.ExtraDisks, d.SizeGiB)
	}
	switch a := ns.Attachment; {
	case a.VNet != "":
		vm.Bridge = a.VNet
	default:
		vm.Bridge, vm.VLAN = a.Bridge, a.VLAN
	}
	if ns.DNS != nil {
		vm.DNS = ns.DNS.Servers
	}
	return vm
}

var tagChar = regexp.MustCompile(`[^a-z0-9_+.-]+`)

// Tags are the Proxmox tags of a server: "gaia", its UID and the contract's
// labels as key_value (Proxmox tags are plain strings, design 4.5).
func Tags(uid, contract string, labels map[string]string) []string {
	clean := func(s string) string { return tagChar.ReplaceAllString(strings.ToLower(s), "-") }
	out := []string{"gaia", "gaia-uid_" + clean(uid), "contract_" + clean(contract)}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, clean(k)+"_"+clean(labels[k]))
	}
	return out
}

// SMBIOSUUID derives the VM's SMBIOS UUID from its UID, so that Gaia can
// recognize the VM by it (design 9.1); version 8 (custom), RFC 4122 variant.
func SMBIOSUUID(uid string) string {
	sum := sha256.Sum256([]byte("gaia/" + uid))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

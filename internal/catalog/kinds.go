package catalog

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Fields tagged gaia:"required" must be present in the document. Checks
// that need more than presence live in each spec's validate method.

// PlatformSpec is the single Platform object: engine settings.
type PlatformSpec struct {
	Secrets    string      `yaml:"secrets" gaia:"required"`
	Engines    Engines     `yaml:"engines" gaia:"required"`
	Monitoring *Monitoring `yaml:"monitoring"`
	// Bastion is the Warpgate the people of a contract's access reach its
	// servers through (design 14.4).
	Bastion    *Bastion `yaml:"bastion"`
	RunArchive string   `yaml:"runArchive"`
	// RunLogs is an s3 provider for each run's raw output, kept without
	// a lock (design 10.1: logs are not evidence).
	RunLogs string `yaml:"runLogs"`
	// Contracts are the repositories with Server and Operation objects
	// beside the platform repository itself (design 2, 7.6).
	Contracts []ContractRepo `yaml:"contracts"`
	// Notifications is how Gaia reaches people who subscribe themselves
	// (design 7.6): email is an smtp provider.
	Notifications *PlatformNotifications `yaml:"notifications"`
}

// Bastion names a warpgate provider.
type Bastion struct {
	Provider string `yaml:"provider" gaia:"required"`
}

// PlatformNotifications names the channels of personal subscriptions.
type PlatformNotifications struct {
	Email string `yaml:"email"`
}

// ContractRepo is a git provider and the directories of it to read.
type ContractRepo struct {
	Provider string   `yaml:"provider" gaia:"required"`
	Paths    []string `yaml:"paths"`
}

// Monitoring is where servers are registered. Host groups are CEL
// expressions with the naming variables (an empty result adds no group);
// templates are listed per image OS and per role monitoring name.
type Monitoring struct {
	Provider   string              `yaml:"provider" gaia:"required"`
	HostGroups []string            `yaml:"hostGroups"`
	Templates  map[string][]string `yaml:"templates"`
}

type Engines struct {
	Provisioning  ProvisioningEngine  `yaml:"provisioning" gaia:"required"`
	Configuration ConfigurationEngine `yaml:"configuration" gaia:"required"`
}

type ProvisioningEngine struct {
	Tool  string `yaml:"tool" gaia:"required"`
	State string `yaml:"state" gaia:"required"`
}

type ConfigurationEngine struct {
	Tool                 string       `yaml:"tool" gaia:"required"`
	Executor             string       `yaml:"executor" gaia:"required"`
	ExecutionEnvironment string       `yaml:"executionEnvironment" gaia:"required"`
	Source               string       `yaml:"source" gaia:"required"`
	Limits               EngineLimits `yaml:"limits"`
	// SSHKey is Gaia's key pair (private, public) for reaching servers:
	// cloud-init gets the public key, Ansible the private one.
	SSHKey string `yaml:"sshKey"`
}

type EngineLimits struct {
	ConcurrentRuns int    `yaml:"concurrentRuns"`
	Forks          int    `yaml:"forks"`
	Timeout        string `yaml:"timeout"`
}

func (s *PlatformSpec) validate() []string {
	var out []string
	if t := s.Engines.Provisioning.Tool; t != "pulumi" {
		out = append(out, fmt.Sprintf("engines.provisioning.tool: %q is not supported (pulumi)", t))
	}
	c := s.Engines.Configuration
	if c.Tool != "ansible" {
		out = append(out, fmt.Sprintf("engines.configuration.tool: %q is not supported (ansible)", c.Tool))
	}
	if c.Executor != "ansible-runner" {
		out = append(out, fmt.Sprintf("engines.configuration.executor: %q is not supported (ansible-runner)", c.Executor))
	}
	if c.SSHKey != "" && !strings.HasPrefix(c.SSHKey, "bao://") {
		out = append(out, "engines.configuration.sshKey: must be a bao:// reference")
	}
	if c.Limits.Timeout != "" {
		if _, err := time.ParseDuration(c.Limits.Timeout); err != nil {
			out = append(out, fmt.Sprintf("engines.configuration.limits.timeout: %v", err))
		}
	}
	return out
}

// ProviderSpec describes how to reach one system. Which fields apply
// depends on the type.
type ProviderSpec struct {
	Type        string        `yaml:"type" gaia:"required"`
	Endpoint    string        `yaml:"endpoint"`
	Credentials string        `yaml:"credentials"`
	Auth        *ProviderAuth `yaml:"auth"`
	Bucket      string        `yaml:"bucket"` // s3
	// Retention is how long run records are locked in an s3 archive
	// (COMPLIANCE): 30d, 10y or a Go duration.
	Retention string `yaml:"retention"`
	URL       string `yaml:"url"`  // git
	Path      string `yaml:"path"` // git
	Ref       string `yaml:"ref"`  // git
	From      string `yaml:"from"` // smtp: the sender address
	// BaseDN is where an activedirectory provider searches; empty is the
	// directory's default naming context.
	BaseDN string `yaml:"baseDN"`
	// WriteCredentials is an activedirectory account that may write DNS
	// records and computer accounts, and nothing else (design 14.6).
	WriteCredentials string `yaml:"writeCredentials"`
}

type ProviderAuth struct {
	Method string `yaml:"method" gaia:"required"`
	Mount  string `yaml:"mount"`
}

// ProviderTypes are the provider types Gaia knows.
var ProviderTypes = []string{"activedirectory", "git", "ipam", "msteams", "nexus", "openbao", "postgres", "proxmox", "s3", "slack", "smtp", "vsphere", "warpgate", "webhook", "zabbix"}

// NotificationTypes are the provider types that carry notifications.
var NotificationTypes = []string{"msteams", "slack", "smtp", "webhook"}

func (s *ProviderSpec) validate() []string {
	var out []string
	if !contains(ProviderTypes, s.Type) {
		return []string{fmt.Sprintf("type: %q is unknown (one of %s)", s.Type, strings.Join(ProviderTypes, ", "))}
	}
	switch s.Type {
	case "git":
		if s.URL == "" {
			out = append(out, "url: required for type git")
		}
	case "slack", "msteams":
		// The incoming webhook's address is the secret: credentials.url.
		if s.Credentials == "" {
			out = append(out, "credentials: required for type "+s.Type+" (a bao:// secret with the incoming webhook's url)")
		}
		if s.Endpoint != "" {
			out = append(out, "endpoint: not used for type "+s.Type+"; the webhook url is a secret (credentials)")
		}
	case "webhook":
		if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			out = append(out, fmt.Sprintf("endpoint: %q: want https://host/path", s.Endpoint))
		}
	case "activedirectory":
		if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "ldaps" && u.Scheme != "ldap") || u.Host == "" {
			out = append(out, fmt.Sprintf("endpoint: %q: want ldaps://host or ldap://host (StartTLS)", s.Endpoint))
		}
		if s.Credentials == "" {
			out = append(out, "credentials: required for type activedirectory (username, password; ca and server_name optional)")
		}
	case "nexus":
		if u, err := url.Parse(s.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			out = append(out, fmt.Sprintf("endpoint: %q: want https://nexus", s.Endpoint))
		}
	case "warpgate":
		if u, err := url.Parse(s.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			out = append(out, fmt.Sprintf("endpoint: %q: want https://host:port", s.Endpoint))
		}
		if s.Credentials == "" {
			out = append(out, "credentials: required for type warpgate (token; insecure or ca optional)")
		}
	case "smtp":
		if _, _, err := net.SplitHostPort(s.Endpoint); err != nil {
			out = append(out, fmt.Sprintf("endpoint: %q: want host:port", s.Endpoint))
		}
		if _, err := mail.ParseAddress(s.From); err != nil {
			out = append(out, fmt.Sprintf("from: %q: want an email address", s.From))
		}
	default:
		if s.Endpoint == "" {
			out = append(out, "endpoint: required for type "+s.Type)
		}
	}
	if s.Type == "s3" && s.Bucket == "" {
		out = append(out, "bucket: required for type s3")
	}
	if s.Retention != "" {
		if s.Type != "s3" {
			out = append(out, "retention: only for type s3")
		} else if _, err := ParseRetention(s.Retention); err != nil {
			out = append(out, "retention: "+err.Error())
		}
	}
	if s.Credentials != "" && !strings.HasPrefix(s.Credentials, "bao://") {
		out = append(out, "credentials: must be a bao:// reference")
	}
	return out
}

// maxRetention is S3 Object Lock's limit (design 10.1).
const maxRetention = 100 * 365 * 24 * time.Hour

// ParseRetention reads a retention: days (30d), years (10y, 365 days
// each) or a Go duration; at least one hour and at most 100 years.
func ParseRetention(s string) (time.Duration, error) {
	var d time.Duration
	switch {
	case s == "":
		return 0, errors.New("empty")
	case strings.HasSuffix(s, "d") || strings.HasSuffix(s, "y"):
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("%q: want a positive number of days (30d) or years (10y)", s)
		}
		d = time.Duration(n) * 24 * time.Hour
		if strings.HasSuffix(s, "y") {
			d *= 365
		}
	default:
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("%q: want 30d, 10y or a duration", s)
		}
	}
	if d < time.Hour || d > maxRetention {
		return 0, fmt.Errorf("%q: between 1h and 100y", s)
	}
	return d, nil
}

// PolicySpec holds naming and environment rules.
type PolicySpec struct {
	Naming         string                       `yaml:"naming"`
	RequiredLabels []string                     `yaml:"requiredLabels"`
	Environments   map[string]EnvironmentPolicy `yaml:"environments"`
	// GaiaRoles gives Gaia's roles to teams (design 11.4): operator and
	// platform-admin; everyone who signs in is a viewer.
	GaiaRoles map[string][]string `yaml:"gaiaRoles"`
	// Operations says who approves Operations (design 6.4).
	Operations *OperationPolicy `yaml:"operations"`
	// Decommission is how servers go (design 9.8); environments may
	// override it.
	Decommission *DecommissionPolicy `yaml:"decommission"`
	// Drift is how often each area is checked and how it is put right
	// (design 7.5).
	Drift *DriftPolicy `yaml:"drift"`
}

// DriftAreas are the areas drift is checked in.
var DriftAreas = []string{"registration", "provisioning", "configuration"}

// DriftPolicy: per area how often (every, a Go duration; configuration
// also a window, "02:00-05:00", and how many servers at once) and how
// drift is put right: auto (a run without approval) or approval (a change
// the owner approves), for every environment or per environment with a
// default.
type DriftPolicy struct {
	Registration  *DriftArea             `yaml:"registration"`
	Provisioning  *DriftArea             `yaml:"provisioning"`
	Configuration *DriftArea             `yaml:"configuration"`
	Remediation   map[string]Remediation `yaml:"remediation"`
}

// DriftArea is when an area is checked.
type DriftArea struct {
	Every       string `yaml:"every"`
	Window      string `yaml:"window"`
	Concurrency int    `yaml:"concurrency"`
}

// Remediation is auto or approval, for all environments (a string) or
// per environment with a default (a map).
type Remediation map[string]string

// UnmarshalYAML takes a string or a map.
func (r *Remediation) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*r = Remediation{"default": n.Value}
		return nil
	}
	m := map[string]string{}
	if err := n.Decode(&m); err != nil {
		return err
	}
	*r = m
	return nil
}

// DriftDefaults are design 7.5's: registration every 15 minutes, put
// right at once everywhere; provisioning every hour and configuration
// once a day, both approved.
var DriftDefaults = map[string]struct {
	Every       time.Duration
	Remediation string
}{
	"registration":  {15 * time.Minute, "auto"},
	"provisioning":  {time.Hour, "approval"},
	"configuration": {24 * time.Hour, "approval"},
}

// DriftEvery is how often area is checked.
func (s *PolicySpec) DriftEvery(area string) time.Duration {
	if a := s.driftArea(area); a != nil && a.Every != "" {
		if d, err := time.ParseDuration(a.Every); err == nil && d > 0 {
			return d
		}
	}
	return DriftDefaults[area].Every
}

// DriftWindow is when an area may be checked ("" any time):
// configuration logs in to every server, so at night (02:00-05:00, the
// server's time) unless the Policy says otherwise.
func (s *PolicySpec) DriftWindow(area string) string {
	if a := s.driftArea(area); a != nil && a.Window != "" {
		return a.Window
	}
	if area == "configuration" {
		return "02:00-05:00"
	}
	return ""
}

func (s *PolicySpec) driftArea(area string) *DriftArea {
	if s == nil || s.Drift == nil {
		return nil
	}
	switch area {
	case "registration":
		return s.Drift.Registration
	case "provisioning":
		return s.Drift.Provisioning
	case "configuration":
		return s.Drift.Configuration
	}
	return nil
}

// DriftRemediation is how drift in area is put right in env.
func (s *PolicySpec) DriftRemediation(area, env string) string {
	if s != nil && s.Drift != nil {
		if r := s.Drift.Remediation[area]; r != nil {
			if v, ok := r[env]; ok {
				return v
			}
			if v, ok := r["default"]; ok {
				return v
			}
		}
	}
	return DriftDefaults[area].Remediation
}

func (p *DriftPolicy) validate() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, a := range []struct {
		name string
		v    *DriftArea
	}{{"registration", p.Registration}, {"provisioning", p.Provisioning}, {"configuration", p.Configuration}} {
		if a.v == nil {
			continue
		}
		if a.v.Every != "" {
			if d, err := time.ParseDuration(a.v.Every); err != nil || d < time.Minute {
				out = append(out, fmt.Sprintf("drift.%s.every: %q: a duration of a minute or more", a.name, a.v.Every))
			}
		}
		if a.v.Window != "" {
			if _, _, err := ParseDailyWindow(a.v.Window); err != nil {
				out = append(out, fmt.Sprintf("drift.%s.window: %v", a.name, err))
			}
		}
	}
	for _, area := range sortedKeys(p.Remediation) {
		if !contains(DriftAreas, area) {
			out = append(out, fmt.Sprintf("drift.remediation.%s: not an area (%s)", area, strings.Join(DriftAreas, ", ")))
		}
		for env, v := range p.Remediation[area] {
			if v != "auto" && v != "approval" {
				out = append(out, fmt.Sprintf("drift.remediation.%s.%s: %q (auto or approval)", area, env, v))
			}
		}
	}
	return out
}

// ParseDailyWindow reads "HH:MM-HH:MM" as minutes after midnight; the end
// may be past midnight (22:00-02:00).
func ParseDailyWindow(s string) (start, end int, err error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q: want HH:MM-HH:MM", s)
	}
	parse := func(v string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("%q: want HH:MM-HH:MM", s)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	if start, err = parse(a); err != nil {
		return 0, 0, err
	}
	if end, err = parse(b); err != nil {
		return 0, 0, err
	}
	if start == end {
		return 0, 0, fmt.Errorf("%q: an empty window", s)
	}
	return start, end, nil
}

// InDailyWindow reports whether t (in its own zone) is in the window.
func InDailyWindow(window string, t time.Time) bool {
	start, end, err := ParseDailyWindow(window)
	if err != nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// DecommissionPolicy: how long a stopped server waits before it is
// deleted, whether a last snapshot is taken first, and how long its
// address stays in quarantine. Durations are days (7d) or Go durations;
// no grace period deletes at once.
type DecommissionPolicy struct {
	GracePeriod   string         `yaml:"gracePeriod"`
	FinalSnapshot *FinalSnapshot `yaml:"finalSnapshot"`
	IPQuarantine  string         `yaml:"ipQuarantine"`
}

// FinalSnapshot is the snapshot taken before a server is stopped.
type FinalSnapshot struct {
	Enabled bool `yaml:"enabled"`
}

// Decommission is a decommission policy with durations read.
type Decommission struct {
	Grace         time.Duration
	FinalSnapshot bool
	IPQuarantine  time.Duration
}

// ParsePeriod reads 7d or a Go duration; empty and "0" are zero.
func ParsePeriod(s string) (time.Duration, error) {
	switch {
	case s == "" || s == "0":
		return 0, nil
	case strings.HasSuffix(s, "d"):
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q: want days (7d) or a duration (12h)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q: want days (7d) or a duration (12h)", s)
	}
	return d, nil
}

// DecommissionIn is the decommission policy of an environment: its own
// fields over the Policy's.
func (s *PolicySpec) DecommissionIn(env string) Decommission {
	var d Decommission
	apply := func(p *DecommissionPolicy) {
		if p == nil {
			return
		}
		if p.GracePeriod != "" {
			d.Grace, _ = ParsePeriod(p.GracePeriod)
		}
		if p.FinalSnapshot != nil {
			d.FinalSnapshot = p.FinalSnapshot.Enabled
		}
		if p.IPQuarantine != "" {
			d.IPQuarantine, _ = ParsePeriod(p.IPQuarantine)
		}
	}
	apply(s.Decommission)
	apply(s.Environments[env].Decommission)
	return d
}

func (p *DecommissionPolicy) validate(at string) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, f := range []struct{ name, v string }{{"gracePeriod", p.GracePeriod}, {"ipQuarantine", p.IPQuarantine}} {
		if _, err := ParsePeriod(f.v); err != nil {
			out = append(out, fmt.Sprintf("%s.%s: %v", at, f.name, err))
		}
	}
	return out
}

// OperationPolicy: approvals per environment ("default" for the rest)
// and per action ("default" for the rest), as roles: owner or
// platform-admin. An Operation touching several owners' contracts needs
// multiOwner (platform-admin) instead of each owner. requireWindow lists
// the actions an environment runs only in a window.
type OperationPolicy struct {
	Approvals     map[string]map[string][]string `yaml:"approvals"`
	MultiOwner    string                         `yaml:"multiOwner"`
	RequireWindow map[string][]string            `yaml:"requireWindow"`
}

// Approvers returns the roles that approve action in env.
func (o *OperationPolicy) Approvers(env, action string) []string {
	if o == nil {
		return []string{"owner"}
	}
	byAction, ok := o.Approvals[env]
	if !ok {
		byAction = o.Approvals["default"]
	}
	if r, ok := byAction[action]; ok {
		return r
	}
	if r, ok := byAction["default"]; ok {
		return r
	}
	return []string{"owner"}
}

// GaiaRoles beyond viewer, which everyone has.
var GaiaRoles = []string{"operator", "platform-admin"}

type EnvironmentPolicy struct {
	Approval         string              `yaml:"approval"`
	DeleteProtection bool                `yaml:"deleteProtection"`
	Decommission     *DecommissionPolicy `yaml:"decommission"`
}

func (s *PolicySpec) validate() []string {
	var out []string
	for _, role := range sortedKeys(s.GaiaRoles) {
		if !contains(GaiaRoles, role) {
			out = append(out, fmt.Sprintf("gaiaRoles.%s: unknown role (%s)", role, strings.Join(GaiaRoles, ", ")))
		}
	}
	for _, name := range sortedKeys(s.Environments) {
		if a := s.Environments[name].Approval; a != "" && a != "required" && a != "none" {
			out = append(out, fmt.Sprintf("environments.%s.approval: %q (required or none)", name, a))
		}
		out = append(out, s.Environments[name].Decommission.validate("environments."+name+".decommission")...)
	}
	out = append(out, s.Decommission.validate("decommission")...)
	out = append(out, s.Drift.validate()...)
	if o := s.Operations; o != nil {
		for _, env := range sortedKeys(o.Approvals) {
			for _, action := range sortedKeys(o.Approvals[env]) {
				for _, role := range o.Approvals[env][action] {
					if role != "owner" && role != "platform-admin" {
						out = append(out, fmt.Sprintf("operations.approvals.%s.%s: %q (owner or platform-admin)", env, action, role))
					}
				}
			}
		}
		if m := o.MultiOwner; m != "" && m != "platform-admin" {
			out = append(out, fmt.Sprintf("operations.multiOwner: %q (platform-admin)", m))
		}
	}
	return out
}

// SiteSpec is a location: where VMs are placed and site-wide services.
type SiteSpec struct {
	Compute     Compute  `yaml:"compute" gaia:"required"`
	NTP         []string `yaml:"ntp"`
	ZabbixProxy string   `yaml:"zabbixProxy"`
}

// Compute is the placement on the site's hypervisor. vSphere uses
// datacenter, cluster and folder; Proxmox uses cluster, nodes, pool and
// storage. Which fields are valid is checked against the provider's type
// once references are resolved.
type Compute struct {
	Provider   string   `yaml:"provider" gaia:"required"`
	Datacenter string   `yaml:"datacenter"`
	Cluster    string   `yaml:"cluster"`
	Folder     string   `yaml:"folder"`
	Nodes      []string `yaml:"nodes"`
	Pool       string   `yaml:"pool"`
	Storage    *Storage `yaml:"storage"`
	// VMIDs is the Proxmox VM ID range Gaia allocates from.
	VMIDs *IDRange `yaml:"vmids"`
}

type IDRange struct {
	Start int `yaml:"start" gaia:"required"`
	End   int `yaml:"end" gaia:"required"`
}

func (s *SiteSpec) validate() []string {
	if r := s.Compute.VMIDs; r != nil && (r.Start < 100 || r.End < r.Start) {
		return []string{"compute.vmids: start >= 100 and end >= start"}
	}
	return nil
}

type Storage struct {
	Default   string `yaml:"default" gaia:"required"`
	Snapshots bool   `yaml:"snapshots"`
}

// NetworkSpec is a network on one site.
type NetworkSpec struct {
	Site                string      `yaml:"site" gaia:"required"`
	Attachment          Attachment  `yaml:"attachment" gaia:"required"`
	IPAM                NetworkIPAM `yaml:"ipam" gaia:"required"`
	Gateway             string      `yaml:"gateway,omitempty"`
	DNS                 *DNS        `yaml:"dns,omitempty"`
	Domain              *Domain     `yaml:"domain,omitempty"`
	AllowedEnvironments []string    `yaml:"allowedEnvironments,omitempty"`
	Description         string      `yaml:"description,omitempty"`
}

// Attachment is how a VM joins the network: a vSphere portgroup, a
// Proxmox bridge (with an optional VLAN tag) or a Proxmox SDN VNet.
type Attachment struct {
	Portgroup string `yaml:"portgroup,omitempty"`
	Bridge    string `yaml:"bridge,omitempty"`
	VLAN      int    `yaml:"vlan,omitempty"`
	VNet      string `yaml:"vnet,omitempty"`
}

type NetworkIPAM struct {
	Provider string `yaml:"provider" gaia:"required"`
	Pool     string `yaml:"pool" gaia:"required"`
}

// DNS is a network's name service. With a provider, Gaia writes each
// server's A record in zone and, with reverse, its PTR record.
type DNS struct {
	Provider string   `yaml:"provider,omitempty"`
	Zone     string   `yaml:"zone,omitempty"`
	Reverse  bool     `yaml:"reverse,omitempty"`
	TTL      int      `yaml:"ttl,omitempty"` // seconds; 3600 when not set
	Servers  []string `yaml:"servers,omitempty"`
}

// Domain puts a network's servers in Active Directory: Gaia creates each
// one's computer account in OU, and the server joins with it.
type Domain struct {
	Provider string `yaml:"provider" gaia:"required"`
	OU       string `yaml:"ou" gaia:"required"`
}

func (s *NetworkSpec) validate() []string {
	a := s.Attachment
	set := 0
	for _, v := range []string{a.Portgroup, a.Bridge, a.VNet} {
		if v != "" {
			set++
		}
	}
	var out []string
	if set != 1 {
		out = append(out, "attachment: exactly one of portgroup, bridge or vnet")
	}
	if a.VLAN != 0 && a.Bridge == "" {
		out = append(out, "attachment.vlan: only with bridge")
	}
	if d := s.DNS; d != nil {
		if d.Provider != "" && d.Zone == "" {
			out = append(out, "dns.zone: required with dns.provider")
		}
		if d.Provider == "" && d.Reverse {
			out = append(out, "dns.reverse: only with dns.provider")
		}
		if d.TTL < 0 {
			out = append(out, "dns.ttl: seconds, not negative")
		}
	}
	if d := s.Domain; d != nil && !strings.HasPrefix(strings.ToUpper(d.OU), "OU=") && !strings.HasPrefix(strings.ToUpper(d.OU), "CN=") {
		out = append(out, fmt.Sprintf("domain.ou: %q: want a DN (OU=Servers,DC=corp,DC=local)", d.OU))
	}
	return out
}

// ImageSpec is an OS image with one template per hypervisor.
type ImageSpec struct {
	OS         string          `yaml:"os" gaia:"required"`
	Connection string          `yaml:"connection" gaia:"required"`
	Templates  []ImageTemplate `yaml:"templates" gaia:"required"`
}

type ImageTemplate struct {
	Provider      string      `yaml:"provider" gaia:"required"`
	Template      TemplateRef `yaml:"template" gaia:"required"`
	Customization string      `yaml:"customization"`
}

// TemplateRef names a template: a plain string (vSphere template name) or
// a mapping with name or vmid (Proxmox).
type TemplateRef struct {
	Name string `yaml:"name"`
	VMID int    `yaml:"vmid"`
}

func (t *TemplateRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&t.Name)
	}
	type plain TemplateRef
	var p plain
	if err := decodeStrict(n, &p); err != nil {
		return err
	}
	*t = TemplateRef(p)
	return nil
}

func (s *ImageSpec) validate() []string {
	var out []string
	if s.OS != "linux" && s.OS != "windows" {
		out = append(out, fmt.Sprintf("os: %q (linux or windows)", s.OS))
	}
	if s.Connection != "ssh" && s.Connection != "winrm" {
		out = append(out, fmt.Sprintf("connection: %q (ssh or winrm)", s.Connection))
	}
	if len(s.Templates) == 0 {
		out = append(out, "templates: at least one")
	}
	for i, t := range s.Templates {
		if t.Template.Name == "" && t.Template.VMID == 0 {
			out = append(out, fmt.Sprintf("templates[%d].template: name or vmid", i))
		}
	}
	return out
}

// SizeSpec is a CPU, memory and disk profile.
type SizeSpec struct {
	CPU       int `yaml:"cpu" gaia:"required"`
	MemoryGiB int `yaml:"memoryGiB" gaia:"required"`
	DiskGiB   int `yaml:"diskGiB" gaia:"required"`
}

func (s *SizeSpec) validate() []string {
	if s.CPU < 1 || s.MemoryGiB < 1 || s.DiskGiB < 1 {
		return []string{"cpu, memoryGiB and diskGiB must be at least 1"}
	}
	return nil
}

// BaselineSpec is a hardening playbook.
type BaselineSpec struct {
	OS       string `yaml:"os" gaia:"required"`
	Playbook string `yaml:"playbook" gaia:"required"`
}

// AnsibleRoleSpec is an Ansible role in the catalog. In the PoC it is
// written by hand; later AnsibleRoleSource generates it.
type AnsibleRoleSpec struct {
	Source              RoleSource       `yaml:"source" gaia:"required"`
	OS                  []string         `yaml:"os,omitempty"`
	Monitoring          []string         `yaml:"monitoring,omitempty"`
	Conflicts           []string         `yaml:"conflicts,omitempty"`
	AllowedEnvironments []string         `yaml:"allowedEnvironments,omitempty"`
	CheckMode           string           `yaml:"checkMode,omitempty"`
	Runbook             bool             `yaml:"runbook,omitempty"`
	Params              map[string]Param `yaml:"params,omitempty"`
	Rules               []Rule           `yaml:"rules,omitempty"`
}

type RoleSource struct {
	Provider string `yaml:"provider" gaia:"required"`
	Role     string `yaml:"role" gaia:"required"`
	Revision string `yaml:"revision,omitempty"`
}

// Param follows Ansible's argument_specs, with Gaia's extensions under
// gaia (design 6.2).
type Param struct {
	Type        string           `yaml:"type,omitempty"`
	Required    bool             `yaml:"required,omitempty"`
	Default     any              `yaml:"default,omitempty"` // only a nil default is left out, not false or 0
	Choices     []any            `yaml:"choices,omitempty"`
	Elements    string           `yaml:"elements,omitempty"`
	Options     map[string]Param `yaml:"options,omitempty"`
	NoLog       bool             `yaml:"no_log,omitempty"`
	Description any              `yaml:"description,omitempty"`
	Gaia        *ParamExt        `yaml:"gaia,omitempty"`
}

type ParamExt struct {
	Min        *float64 `yaml:"min,omitempty"`
	Max        *float64 `yaml:"max,omitempty"`
	Pattern    string   `yaml:"pattern,omitempty"`
	Format     string   `yaml:"format,omitempty"`
	Secret     bool     `yaml:"secret,omitempty"`
	Ref        string   `yaml:"ref,omitempty"`
	Deprecated string   `yaml:"deprecated,omitempty"`
	Freeform   bool     `yaml:"freeform,omitempty"`
}

// Rule is a CEL expression over params and the resolved contract.
type Rule struct {
	Rule    string `yaml:"rule" gaia:"required"`
	Message string `yaml:"message" gaia:"required"`
}

var paramTypes = []string{"bool", "dict", "float", "int", "list", "path", "str"}

func (s *AnsibleRoleSpec) validate() []string {
	var out []string
	if c := s.CheckMode; c != "" && c != "supported" && c != "unsupported" {
		out = append(out, fmt.Sprintf("checkMode: %q (supported or unsupported)", c))
	}
	for _, name := range sortedKeys(s.Params) {
		out = append(out, validateParam("params."+name, s.Params[name])...)
	}
	return out
}

// validateParam keeps the escape hatch closed: no raw, json or free-form
// structures unless gaia.freeform is set (design 6.2, rule 1).
func validateParam(path string, p Param) []string {
	freeform := p.Gaia != nil && p.Gaia.Freeform
	t := p.Type
	if t == "" {
		t = "str"
	}
	var out []string
	switch {
	case !contains(paramTypes, t):
		out = append(out, fmt.Sprintf("%s.type: %q is not allowed (one of %s)", path, p.Type, strings.Join(paramTypes, ", ")))
	case t == "dict" && len(p.Options) == 0 && !freeform:
		out = append(out, path+": dict needs options (or gaia.freeform)")
	case t == "list" && p.Elements == "" && !freeform:
		out = append(out, path+": list needs elements (or gaia.freeform)")
	}
	if p.Gaia != nil && p.Gaia.Secret && t != "str" {
		out = append(out, path+": a secret param must be str")
	}
	if len(out) == 0 {
		out = append(out, checkParamSchema(path, p)...)
	}
	for _, name := range sortedKeys(p.Options) {
		out = append(out, validateParam(path+".options."+name, p.Options[name])...)
	}
	return out
}

// ServerSpec is a contract for one group of servers.
type ServerSpec struct {
	Replicas     *int          `yaml:"replicas"`
	Image        string        `yaml:"image" gaia:"required"`
	Size         string        `yaml:"size" gaia:"required"`
	Network      string        `yaml:"network"`
	Topology     []Placement   `yaml:"topology"`
	Disks        []Disk        `yaml:"disks"`
	Baseline     string        `yaml:"baseline"`
	AnsibleRoles []RoleRef     `yaml:"ansibleRoles"`
	Patch        *PatchSetting `yaml:"patch"`
	Lifecycle    *Lifecycle    `yaml:"lifecycle"`
	Retire       []string      `yaml:"retire"`
	// Access is who may log in to the servers (design 14.3): a Group or a
	// Team, with sudo or not.
	Access []Access `yaml:"access"`
	// Apps are the applications deployed on the servers, with their
	// params; Agents the agents installed besides the mandatory ones.
	Apps   []AppRef `yaml:"apps"`
	Agents []string `yaml:"agents"`
	// Hosts are servers Gaia did not create and adopts (design 8.2): named
	// one by one, instead of replicas.
	Hosts []AdoptedHost `yaml:"hosts"`
	// Management is how far Gaia goes per area with adopted servers.
	Management *Management `yaml:"management"`
}

// AdoptedHost is an existing server: its name, its VM (provider:id) and
// its address.
type AdoptedHost struct {
	Name string `yaml:"name" gaia:"required"`
	VM   string `yaml:"vm" gaia:"required"`
	IP   string `yaml:"ip" gaia:"required"`
}

// VMRef splits vm into its provider and the VM's ID there.
func (h AdoptedHost) VMRef() (provider, id string) {
	provider, id, _ = strings.Cut(h.VM, ":")
	return provider, id
}

// Management levels (design 8.1): observe records drift, report also
// tells the owners, enforce puts it right.
type Management struct {
	Provisioning  string `yaml:"provisioning"`
	Configuration string `yaml:"configuration"`
	Registration  string `yaml:"registration"`
}

// ManagementLevels are what a management area may be.
var ManagementLevels = []string{"observe", "report", "enforce"}

// Adopted reports whether the contract's servers are adopted ones.
func (s *ServerSpec) Adopted() bool { return len(s.Hosts) > 0 }

// Level is how far Gaia goes in an area: enforce for the servers it
// creates; for adopted ones what the contract says, else observe for
// provisioning, report for configuration and enforce for registration
// (design 8.2).
func (s *ServerSpec) Level(area string) string {
	if !s.Adopted() {
		return "enforce"
	}
	m := Management{}
	if s.Management != nil {
		m = *s.Management
	}
	v, def := m.Registration, "enforce"
	switch area {
	case "provisioning":
		v, def = m.Provisioning, "observe"
	case "configuration":
		v, def = m.Configuration, "report"
	}
	if v == "" {
		return def
	}
	return v
}

// AppRef is an App on a contract's servers and the params it gets.
type AppRef struct {
	Name   string         `yaml:"name" gaia:"required"`
	Params map[string]any `yaml:"params"`
}

// Access grants a Group or a Team login to a contract's servers.
type Access struct {
	Group string `yaml:"group"`
	Team  string `yaml:"team"`
	Sudo  Sudo   `yaml:"sudo"`
}

// Sudo is what an access entry may run as root: everything (sudo: true)
// or only the listed commands (sudo: [/usr/bin/systemctl restart nginx]).
type Sudo struct {
	All      bool
	Commands []string
}

// Any reports whether it gives any sudo.
func (s Sudo) Any() bool { return s.All || len(s.Commands) > 0 }

func (s *Sudo) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*s = Sudo{}
			return nil
		}
		var b bool
		if err := n.Decode(&b); err != nil {
			return fmt.Errorf("line %d: sudo: true, false or a list of commands", n.Line)
		}
		*s = Sudo{All: b}
		return nil
	case yaml.SequenceNode:
		var cmds []string
		if err := n.Decode(&cmds); err != nil {
			return fmt.Errorf("line %d: sudo: a list of commands", n.Line)
		}
		*s = Sudo{Commands: cmds}
		return nil
	}
	return fmt.Errorf("line %d: sudo: true, false or a list of commands", n.Line)
}

func (s Sudo) MarshalYAML() (any, error) {
	if len(s.Commands) > 0 {
		return s.Commands, nil
	}
	return s.All, nil
}

// sudoCommand is a command line sudoers takes as written: an absolute
// path and plain arguments, none of sudoers' separators, wildcards or
// negation, and not ALL.
var sudoCommand = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+( [A-Za-z0-9._/@+%-]+)*$`)

func validSudoCommand(c string) bool {
	if !sudoCommand.MatchString(c) {
		return false
	}
	for _, w := range strings.Fields(c) {
		if w == "ALL" || w == "sudoedit" || strings.HasSuffix(w, "/sudoedit") {
			return false
		}
	}
	return true
}

type Placement struct {
	Network  string `yaml:"network" gaia:"required"`
	Replicas int    `yaml:"replicas" gaia:"required"`
}

type Disk struct {
	SizeGiB int    `yaml:"sizeGiB" gaia:"required"`
	Mount   string `yaml:"mount" gaia:"required"`
}

type PatchSetting struct {
	Wave     int  `yaml:"wave"`
	Snapshot bool `yaml:"snapshot"`
}

type Lifecycle struct {
	State          string `yaml:"state"`
	DeletionPolicy string `yaml:"deletionPolicy"`
}

// RoleRef is an AnsibleRole reference: a plain name or {name, params}.
type RoleRef struct {
	Name   string         `yaml:"name" gaia:"required"`
	Params map[string]any `yaml:"params"`
}

func (r *RoleRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&r.Name)
	}
	type plain RoleRef
	var p plain
	if err := decodeStrict(n, &p); err != nil {
		return err
	}
	*r = RoleRef(p)
	return nil
}

func (s *ServerSpec) validateAccess() []string {
	var out []string
	seen := map[string]bool{}
	for i, a := range s.Access {
		at := fmt.Sprintf("access[%d]", i)
		if (a.Group == "") == (a.Team == "") {
			out = append(out, at+": name a group or a team")
			continue
		}
		k := "group/" + a.Group + "team/" + a.Team
		if seen[k] {
			out = append(out, at+": listed twice")
		}
		seen[k] = true
		cmds := map[string]bool{}
		for j, c := range a.Sudo.Commands {
			switch {
			case !validSudoCommand(c):
				out = append(out, fmt.Sprintf("%s.sudo[%d]: %q is not a plain command: an absolute path and arguments, without , : = \\ * ? ! or ALL", at, j, c))
			case cmds[c]:
				out = append(out, fmt.Sprintf("%s.sudo[%d]: %q listed twice", at, j, c))
			}
			cmds[c] = true
		}
	}
	return out
}

func (s *ServerSpec) validate() []string {
	if out := s.validateAccess(); len(out) > 0 {
		return append(out, s.validateRest()...)
	}
	return s.validateRest()
}

func (s *ServerSpec) validateRest() []string {
	var out []string
	switch {
	case s.Network != "" && len(s.Topology) > 0:
		out = append(out, "network and topology: use one of them")
	case s.Network == "" && len(s.Topology) == 0:
		out = append(out, "network or topology: one is required")
	case len(s.Topology) > 0 && s.Replicas != nil:
		out = append(out, "replicas: set per topology entry when topology is used")
	}
	if s.Replicas != nil && *s.Replicas < 0 {
		out = append(out, "replicas: must not be negative")
	}
	for i, p := range s.Topology {
		if p.Replicas < 0 {
			out = append(out, fmt.Sprintf("topology[%d].replicas: must not be negative", i))
		}
	}
	if l := s.Lifecycle; l != nil {
		if l.State != "" && l.State != "active" && l.State != "decommissioned" {
			out = append(out, fmt.Sprintf("lifecycle.state: %q (active or decommissioned)", l.State))
		}
		if l.DeletionPolicy != "" && l.DeletionPolicy != "delete" && l.DeletionPolicy != "retain" {
			out = append(out, fmt.Sprintf("lifecycle.deletionPolicy: %q (delete or retain)", l.DeletionPolicy))
		}
	}
	return append(out, s.validateAdoption()...)
}

func (s *ServerSpec) validateAdoption() []string {
	var out []string
	if !s.Adopted() {
		if s.Management != nil {
			out = append(out, "management: only for adopted servers (hosts)")
		}
		return out
	}
	if s.Replicas != nil || len(s.Topology) > 0 || len(s.Retire) > 0 {
		out = append(out, "hosts: adopted servers are listed; replicas, topology and retire do not go with hosts")
	}
	seen := map[string]bool{}
	for i, h := range s.Hosts {
		at := fmt.Sprintf("hosts[%d]", i)
		if seen[h.Name] {
			out = append(out, at+".name: "+h.Name+" is listed twice")
		}
		seen[h.Name] = true
		if p, id := h.VMRef(); p == "" || id == "" {
			out = append(out, fmt.Sprintf("%s.vm: %q: want provider:id (pve-lab:120)", at, h.VM))
		}
		if _, err := netip.ParseAddr(h.IP); err != nil {
			out = append(out, fmt.Sprintf("%s.ip: %q is not an address", at, h.IP))
		}
	}
	if m := s.Management; m != nil {
		for _, a := range []struct{ name, v string }{{"provisioning", m.Provisioning}, {"configuration", m.Configuration}, {"registration", m.Registration}} {
			if a.v != "" && !contains(ManagementLevels, a.v) {
				out = append(out, fmt.Sprintf("management.%s: %q (%s)", a.name, a.v, strings.Join(ManagementLevels, ", ")))
			}
		}
	}
	if s.Level("provisioning") == "enforce" {
		out = append(out, "management.provisioning: enforce needs the VMs imported into Pulumi, which this version does not do yet")
	}
	if l := s.Lifecycle; l != nil && l.State == "decommissioned" && l.DeletionPolicy == "delete" {
		// design 9.7: a VM Gaia did not make is not Gaia's to delete
		out = append(out, "lifecycle.deletionPolicy: adopted servers are retained, not deleted, unless provisioning is enforced")
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AuthConfigSpec is how people sign in to Gaia (design 11.2).
type AuthConfigSpec struct {
	// URL is where people open Gaia; passkeys are bound to its host.
	URL       string         `yaml:"url" gaia:"required"`
	Require   string         `yaml:"require" gaia:"required"` // mfa | passkey
	Providers []AuthProvider `yaml:"providers" gaia:"required"`
	Sessions  AuthSessions   `yaml:"sessions"`
}

// AuthProvider is one way to sign in: an OIDC identity provider or Gaia's
// local accounts.
type AuthProvider struct {
	Name    string `yaml:"name" gaia:"required"`
	Type    string `yaml:"type" gaia:"required"` // oidc | local
	Purpose string `yaml:"purpose"`              // local: break-glass
	// OIDC
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"clientId"`
	ClientSecret string   `yaml:"clientSecret"` // bao://
	Scopes       []string `yaml:"scopes"`
	GroupsClaim  string   `yaml:"groupsClaim"` // teams from the token, when there is no directory
	MFA          *OIDCMFA `yaml:"mfa"`
	// Directory ties sign-ins to Active Directory accounts (design 14):
	// teams and roles then come from AD, not from the token.
	Directory *AuthDirectory `yaml:"directory"`
}

// AuthDirectory says which AD account a sign-in is: the account whose
// sAMAccountName is the claim's value (what precedes an @ is used).
type AuthDirectory struct {
	Provider string `yaml:"provider" gaia:"required"`
	Claim    string `yaml:"claim"` // preferred_username by default; upn, email...
}

// OIDCMFA is when an OIDC sign-in counts as MFA: the ID token's amr or acr
// must show it; otherwise Gaia asks for its own passkey.
type OIDCMFA struct {
	TrustIdp  bool     `yaml:"trustIdp"`
	AcceptAmr []string `yaml:"acceptAmr"`
	AcceptAcr []string `yaml:"acceptAcr"`
	Fallback  string   `yaml:"fallback"` // local-passkey
}

// AuthSessions are session lifetimes.
type AuthSessions struct {
	UI  UISessions  `yaml:"ui"`
	CLI CLISessions `yaml:"cli"`
}

type UISessions struct {
	TTL  string `yaml:"ttl"`
	Idle string `yaml:"idle"`
}

type CLISessions struct {
	AccessTTL  string `yaml:"accessTtl"`
	RefreshTTL string `yaml:"refreshTtl"`
}

// Durations of the sessions with the design's defaults filled in.
func (s AuthSessions) Durations() (uiTTL, uiIdle, cliAccess, cliRefresh time.Duration) {
	d := func(v string, def time.Duration) time.Duration {
		if x, err := time.ParseDuration(v); err == nil && x > 0 {
			return x
		}
		return def
	}
	return d(s.UI.TTL, 8*time.Hour), d(s.UI.Idle, 30*time.Minute), d(s.CLI.AccessTTL, time.Hour), d(s.CLI.RefreshTTL, 12*time.Hour)
}

func (s *AuthConfigSpec) validate() []string {
	var out []string
	if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		out = append(out, fmt.Sprintf("url: %q: want https://host", s.URL))
	}
	if s.Require != "mfa" && s.Require != "passkey" {
		out = append(out, fmt.Sprintf("require: %q (mfa or passkey)", s.Require))
	}
	seen := map[string]bool{}
	locals := 0
	for i, p := range s.Providers {
		at := fmt.Sprintf("providers[%d]", i)
		if seen[p.Name] {
			out = append(out, fmt.Sprintf("%s.name: %q twice", at, p.Name))
		}
		seen[p.Name] = true
		switch p.Type {
		case "local":
			locals++
		case "oidc":
			u, err := url.Parse(p.Issuer)
			localHTTP := err == nil && u.Scheme == "http" && isLoopbackHost(u.Hostname())
			if err != nil || u.Scheme != "https" && !localHTTP {
				out = append(out, fmt.Sprintf("%s.issuer: %q: want an https URL", at, p.Issuer))
			}
			if p.ClientID == "" {
				out = append(out, at+".clientId: required for oidc")
			}
			if p.ClientSecret != "" && !strings.HasPrefix(p.ClientSecret, "bao://") {
				out = append(out, at+".clientSecret: must be a bao:// reference")
			}
			if p.MFA != nil && p.MFA.Fallback != "" && p.MFA.Fallback != "local-passkey" {
				out = append(out, fmt.Sprintf("%s.mfa.fallback: %q (local-passkey)", at, p.MFA.Fallback))
			}
		default:
			out = append(out, fmt.Sprintf("%s.type: %q (oidc or local)", at, p.Type))
		}
	}
	if locals > 1 {
		out = append(out, "providers: one local provider at most")
	}
	for _, v := range []struct{ name, val string }{
		{"sessions.ui.ttl", s.Sessions.UI.TTL}, {"sessions.ui.idle", s.Sessions.UI.Idle},
		{"sessions.cli.accessTtl", s.Sessions.CLI.AccessTTL}, {"sessions.cli.refreshTtl", s.Sessions.CLI.RefreshTTL},
	} {
		if v.val != "" {
			if d, err := time.ParseDuration(v.val); err != nil || d <= 0 {
				out = append(out, fmt.Sprintf("%s: %q is not a duration", v.name, v.val))
			}
		}
	}
	return out
}

// isLoopbackHost allows a plain-http issuer for a local test IdP.
func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// TeamSpec is a team: the unit that owns contracts (design 14.1). Teams
// will be generated from AD by a TeamSource; until then they are written
// by hand. Members come from sign-in, not from Git.
type TeamSpec struct {
	DisplayName    string         `yaml:"displayName,omitempty"`
	ADGroup        *ADGroup       `yaml:"adGroup,omitempty"`
	ApproversGroup *ADGroup       `yaml:"approversGroup,omitempty"`
	Secrets        *TeamSecrets   `yaml:"secrets,omitempty"`
	Notifications  []Subscription `yaml:"notifications,omitempty"`
}

// ADGroup is a group in Active Directory.
type ADGroup struct {
	Provider string `yaml:"provider,omitempty"`
	DN       string `yaml:"dn" gaia:"required"`
}

// TeamSecrets bounds the secrets a team's contracts may refer to.
type TeamSecrets struct {
	Prefix string `yaml:"prefix" gaia:"required"`
}

// Subscription sends some events of a team's contracts to a channel.
type Subscription struct {
	Provider string   `yaml:"provider" gaia:"required"`
	Triggers []string `yaml:"triggers" gaia:"required"`
	// To are the addresses of an smtp channel.
	To []string `yaml:"to,omitempty"`
}

// Triggers are the events notifications are sent for (design 7.6).
var Triggers = []string{
	"change.pending",    // a change waits for approval
	"change.approved",   // its approvals are complete
	"change.rejected",   //
	"change.applied",    //
	"change.failed",     // its run failed or was cancelled
	"drift.found",       // the real state differs from the applied one
	"approval.expiring", // an approved change may not run in time
	"access.privileged", // AD added someone to a team in a privileged group
}

func (s *TeamSpec) validate() []string {
	var out []string
	if s.Secrets != nil && !strings.HasPrefix(s.Secrets.Prefix, "bao://") {
		out = append(out, "secrets.prefix: must be a bao:// prefix")
	}
	for i, n := range s.Notifications {
		at := fmt.Sprintf("notifications[%d]", i)
		if len(n.Triggers) == 0 {
			out = append(out, at+".triggers: at least one")
		}
		for _, t := range n.Triggers {
			if !contains(Triggers, t) {
				out = append(out, fmt.Sprintf("%s.triggers: %q is unknown (one of %s)", at, t, strings.Join(Triggers, ", ")))
			}
		}
		for _, to := range n.To {
			if _, err := mail.ParseAddress(to); err != nil {
				out = append(out, fmt.Sprintf("%s.to: %q is not an email address", at, to))
			}
		}
	}
	return out
}

// TeamSourceSpec says which AD groups are teams and how they map
// (design 14.1). Expressions are CEL over group (dn, cn, description,
// guid); map expressions also see name, the team's name.
type TeamSourceSpec struct {
	Provider   string         `yaml:"provider" gaia:"required"`
	Select     string         `yaml:"select" gaia:"required"`
	Naming     string         `yaml:"naming" gaia:"required"`
	Map        TeamSourceMap  `yaml:"map"`
	Membership TeamMembership `yaml:"membership"`
	Output     string         `yaml:"output" gaia:"required"`
}

// TeamSourceMap fills a generated Team: approvers is the common name of
// the team's approvers group.
type TeamSourceMap struct {
	DisplayName   string `yaml:"displayName"`
	Approvers     string `yaml:"approvers"`
	SecretsPrefix string `yaml:"secretsPrefix"`
}

// TeamMembership is how members are read: nested groups opened, and how
// often.
type TeamMembership struct {
	Recursive bool           `yaml:"recursive"`
	Sync      MembershipSync `yaml:"sync"`
}

// MembershipSync is the membership refresh interval (5m by default).
type MembershipSync struct {
	Every string `yaml:"every"`
}

// Every is the membership refresh interval.
func (s *TeamSourceSpec) Every() time.Duration {
	d, err := time.ParseDuration(s.Membership.Sync.Every)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

func (s *TeamSourceSpec) validate() []string {
	var out []string
	if e := s.Membership.Sync.Every; e != "" {
		if d, err := time.ParseDuration(e); err != nil || d < time.Minute {
			out = append(out, fmt.Sprintf("membership.sync.every: %q: a duration of at least 1m", e))
		}
	}
	if s.Output != "" && (strings.HasPrefix(s.Output, "/") || strings.Contains(s.Output, "..")) {
		out = append(out, fmt.Sprintf("output: %q: a directory inside the platform repository", s.Output))
	}
	return out
}

// GroupSpec is an access group (design 14.3): teams and other groups,
// never people. Its owner approves the changes it makes to contracts;
// a privileged group's changes also need a platform admin.
type GroupSpec struct {
	Description string   `yaml:"description"`
	Teams       []string `yaml:"teams"`
	Groups      []string `yaml:"groups"`
	Owner       string   `yaml:"owner" gaia:"required"`
	Privileged  bool     `yaml:"privileged"`
}

func (s *GroupSpec) validate() []string {
	if len(s.Teams) == 0 && len(s.Groups) == 0 {
		return []string{"teams: a group needs teams or groups"}
	}
	return nil
}

// NetworkSourceSpec makes Network objects from IPAM pools (design 5.2).
// Expressions are CEL over subnet: cidr, gateway, name, tags, labels and
// each label at the top (subnet.site); map expressions also see name,
// the network's, and may return maps and lists. Without a mapping, ipam
// is the source's provider and the pool's cidr, gateway the pool's.
type NetworkSourceSpec struct {
	Provider string     `yaml:"provider" gaia:"required"`
	Select   string     `yaml:"select" gaia:"required"`
	Naming   string     `yaml:"naming" gaia:"required"`
	Map      NetworkMap `yaml:"map"`
	Output   string     `yaml:"output" gaia:"required"`
}

// NetworkMap fills a generated Network.
type NetworkMap struct {
	Site                string `yaml:"site"`
	Attachment          string `yaml:"attachment"`
	Gateway             string `yaml:"gateway"`
	IPAM                string `yaml:"ipam"`
	DNS                 string `yaml:"dns"`
	AllowedEnvironments string `yaml:"allowedEnvironments"`
	Labels              string `yaml:"labels"`
	Description         string `yaml:"description"`
}

// Fields are the map's expressions by the Network field they fill.
func (m NetworkMap) Fields() map[string]string {
	return map[string]string{"site": m.Site, "attachment": m.Attachment, "gateway": m.Gateway, "ipam": m.IPAM,
		"dns": m.DNS, "allowedEnvironments": m.AllowedEnvironments, "labels": m.Labels, "description": m.Description}
}

func (s *NetworkSourceSpec) validate() []string {
	var out []string
	if s.Map.Site == "" {
		out = append(out, "map.site: required (which Site a network is in)")
	}
	if s.Map.Attachment == "" {
		out = append(out, "map.attachment: required (how a VM joins the network)")
	}
	if s.Output != "" && (strings.HasPrefix(s.Output, "/") || strings.Contains(s.Output, "..")) {
		out = append(out, fmt.Sprintf("output: %q: a directory inside the platform repository", s.Output))
	}
	return out
}

// AnsibleRoleSourceSpec makes AnsibleRole objects from the roles of a git
// repository (design 5.3), usually the config repository. paths and
// exclude are globs of role directories, inside the provider's path.
// Expressions are CEL over role (name, collection, namespace, fqcn, path)
// and meta (meta/main.yml); map expressions also see name, the object's.
// Params come from meta/argument_specs.yml (the main entry point), and
// Gaia's own fields from meta/gaia.yml.
type AnsibleRoleSourceSpec struct {
	Provider string   `yaml:"provider" gaia:"required"`
	Paths    []string `yaml:"paths" gaia:"required"`
	Exclude  []string `yaml:"exclude"`
	Naming   string   `yaml:"naming" gaia:"required"`
	Map      RoleMap  `yaml:"map"`
	Output   string   `yaml:"output" gaia:"required"`
}

// RoleMap fills a generated AnsibleRole.
type RoleMap struct {
	OS     string `yaml:"os"`
	Labels string `yaml:"labels"`
}

func (s *AnsibleRoleSourceSpec) validate() []string {
	var out []string
	for i, p := range append(append([]string{}, s.Paths...), s.Exclude...) {
		if _, err := path.Match(p, ""); err != nil || strings.HasPrefix(p, "/") {
			out = append(out, fmt.Sprintf("paths/exclude[%d]: %q is not a glob of directories", i, p))
		}
	}
	if s.Output != "" && (strings.HasPrefix(s.Output, "/") || strings.Contains(s.Output, "..")) {
		out = append(out, fmt.Sprintf("output: %q: a directory inside the platform repository", s.Output))
	}
	return out
}

// AppSpec is an application (design 4.3): a playbook in the config
// repository, the roles it needs on the server, its params and rules,
// and where its artifact comes from (${param} in path is the param's
// value).
type AppSpec struct {
	Playbook string           `yaml:"playbook" gaia:"required"`
	Requires []string         `yaml:"requires"`
	Artifact *Artifact        `yaml:"artifact"`
	Params   map[string]Param `yaml:"params"`
	Rules    []Rule           `yaml:"rules"`
}

// Artifact is a file in an artifact repository (a nexus provider).
type Artifact struct {
	Provider   string `yaml:"provider" gaia:"required"`
	Repository string `yaml:"repository" gaia:"required"`
	Path       string `yaml:"path" gaia:"required"`
}

func (s *AppSpec) validate() []string {
	var out []string
	if s.Playbook != "" && (strings.HasPrefix(s.Playbook, "/") || strings.Contains(s.Playbook, "..")) {
		out = append(out, fmt.Sprintf("playbook: %q: a path inside the config repository", s.Playbook))
	}
	for _, name := range sortedKeys(s.Params) {
		out = append(out, validateParam("params."+name, s.Params[name])...)
	}
	return out
}

// AgentSpec is an agent installed on servers by an AnsibleRole (design
// 4.3). A mandatory agent goes on every server of its operating systems
// without a contract naming it.
type AgentSpec struct {
	OS          []string       `yaml:"os"`
	AnsibleRole string         `yaml:"ansibleRole" gaia:"required"`
	Params      map[string]any `yaml:"params"`
	Mandatory   bool           `yaml:"mandatory"`
}

// OperationSpec is a one-off job on servers (design 6.4): approved like
// a change, run once, in its window, in batches.
type OperationSpec struct {
	Action   string         `yaml:"action" gaia:"required"`
	Selector string         `yaml:"selector" gaia:"required"`
	Window   *Window        `yaml:"window"`
	NotAfter string         `yaml:"notAfter"`
	Strategy Strategy       `yaml:"strategy"`
	Options  map[string]any `yaml:"options"`
	Runbook  string         `yaml:"runbook"`
	Params   map[string]any `yaml:"params"`
	// Hosts narrows the selected contracts' servers to these names; a
	// replace names its servers (design 9.5).
	Hosts []string `yaml:"hosts"`
}

// Window is when an Operation may run.
type Window struct {
	Start string `yaml:"start" gaia:"required"`
	End   string `yaml:"end" gaia:"required"`
}

// Strategy says how an Operation goes through its servers.
type Strategy struct {
	BatchSize           string `yaml:"batchSize"` // a number or a percentage
	MaxFailures         int    `yaml:"maxFailures"`
	PauseBetweenBatches string `yaml:"pauseBetweenBatches"`
}

// OperationActions are what an Operation may do (in this version).
var OperationActions = []string{"patch", "power", "reboot", "replace", "revert", "runbook", "snapshot"}

// Times returns the window and notAfter, parsed (zero when not set).
func (s *OperationSpec) Times() (start, end, notAfter time.Time, err error) {
	if s.Window != nil {
		if start, err = time.Parse(time.RFC3339, s.Window.Start); err != nil {
			return
		}
		if end, err = time.Parse(time.RFC3339, s.Window.End); err != nil {
			return
		}
	}
	if s.NotAfter != "" {
		notAfter, err = time.Parse(time.RFC3339, s.NotAfter)
	}
	return
}

// Batches splits n servers by the strategy's batch size: a number, or a
// percentage of n (at least one); all at once when it is empty.
func (s *OperationSpec) Batches(n int) []int {
	if n == 0 {
		return nil
	}
	size := n
	b := strings.TrimSpace(s.Strategy.BatchSize)
	if pct, ok := strings.CutSuffix(b, "%"); ok {
		if p, err := strconv.Atoi(pct); err == nil && p > 0 {
			size = max(1, n*p/100)
		}
	} else if v, err := strconv.Atoi(b); err == nil && v > 0 {
		size = v
	}
	var out []int
	for left := n; left > 0; left -= size {
		out = append(out, min(size, left))
	}
	return out
}

func (s *OperationSpec) validate() []string {
	var out []string
	if !contains(OperationActions, s.Action) {
		out = append(out, fmt.Sprintf("action: %q (one of %s)", s.Action, strings.Join(OperationActions, ", ")))
	}
	if (s.Action == "runbook") != (s.Runbook != "") {
		out = append(out, "runbook: names the role of a runbook action, and only then")
	}
	if len(s.Params) > 0 && s.Action != "runbook" {
		out = append(out, "params: only for a runbook")
	}
	if st, _ := s.Options["state"].(string); s.Action == "power" && st != "on" && st != "off" && st != "restart" {
		out = append(out, "options.state: on, off or restart, for a power action")
	}
	if sn, _ := s.Options["snapshot"].(string); s.Action == "revert" && sn == "" {
		out = append(out, "options.snapshot: the snapshot to go back to, for a revert")
	}
	if s.Action == "replace" && len(s.Hosts) == 0 {
		out = append(out, "hosts: a replace names the servers it replaces")
	}
	start, end, _, err := s.Times()
	switch {
	case err != nil:
		out = append(out, "window/notAfter: want RFC 3339 times ("+err.Error()+")")
	case s.Window != nil && !end.After(start):
		out = append(out, "window: end must be after start")
	}
	if b := strings.TrimSpace(s.Strategy.BatchSize); b != "" {
		v := strings.TrimSuffix(b, "%")
		if n, err := strconv.Atoi(v); err != nil || n <= 0 || strings.HasSuffix(b, "%") && n > 100 {
			out = append(out, fmt.Sprintf("strategy.batchSize: %q (a number or a percentage)", b))
		}
	}
	if p := s.Strategy.PauseBetweenBatches; p != "" {
		if _, err := time.ParseDuration(p); err != nil {
			out = append(out, "strategy.pauseBetweenBatches: "+err.Error())
		}
	}
	if s.Strategy.MaxFailures < 0 {
		out = append(out, "strategy.maxFailures: 0 or more")
	}
	return out
}

// OperationScheduleSpec writes an Operation for every window of a
// recurrence (design 6.4): createAhead before the window, into output,
// from template. approval: perInstance (each Operation is approved) or
// once (approving the schedule approves its Operations).
type OperationScheduleSpec struct {
	Recurrence  string            `yaml:"recurrence" gaia:"required"` // iCalendar RRULE
	Start       string            `yaml:"start" gaia:"required"`      // HH:MM
	Timezone    string            `yaml:"timezone"`                   // IANA; UTC by default
	Duration    string            `yaml:"duration" gaia:"required"`
	CreateAhead string            `yaml:"createAhead"`
	Approval    string            `yaml:"approval"`
	Output      string            `yaml:"output" gaia:"required"`
	Template    OperationTemplate `yaml:"template" gaia:"required"`
}

// OperationTemplate is an Operation without its window.
type OperationTemplate struct {
	Action   string         `yaml:"action" gaia:"required"`
	Selector string         `yaml:"selector" gaia:"required"`
	Strategy Strategy       `yaml:"strategy"`
	Options  map[string]any `yaml:"options"`
	Runbook  string         `yaml:"runbook"`
	Params   map[string]any `yaml:"params"`
}

// Location is the schedule's time zone.
func (s *OperationScheduleSpec) Location() (*time.Location, error) {
	if s.Timezone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(s.Timezone)
}

// Ahead is how long before a window its Operation is written (7 days by
// default; "7d" or a duration).
func (s *OperationScheduleSpec) Ahead() time.Duration {
	if d, err := ParseDays(s.CreateAhead); err == nil && d > 0 {
		return d
	}
	return 7 * 24 * time.Hour
}

// ParseDays reads "7d" or a Go duration.
func ParseDays(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}

func (s *OperationScheduleSpec) validate() []string {
	var out []string
	if _, err := time.Parse("15:04", s.Start); err != nil {
		out = append(out, fmt.Sprintf("start: %q (HH:MM)", s.Start))
	}
	if _, err := s.Location(); err != nil {
		out = append(out, fmt.Sprintf("timezone: %v", err))
	}
	if d, err := time.ParseDuration(s.Duration); err != nil || d <= 0 {
		out = append(out, fmt.Sprintf("duration: %q (such as 4h)", s.Duration))
	}
	if s.CreateAhead != "" {
		if d, err := ParseDays(s.CreateAhead); err != nil || d <= 0 {
			out = append(out, fmt.Sprintf("createAhead: %q (such as 7d)", s.CreateAhead))
		}
	}
	if a := s.Approval; a != "" && a != "perInstance" && a != "once" {
		out = append(out, fmt.Sprintf("approval: %q (perInstance or once)", a))
	}
	if s.Output != "" && (strings.HasPrefix(s.Output, "/") || strings.Contains(s.Output, "..")) {
		out = append(out, fmt.Sprintf("output: %q: a directory inside the platform repository", s.Output))
	}
	op := OperationSpec{Action: s.Template.Action, Selector: s.Template.Selector, Strategy: s.Template.Strategy,
		Options: s.Template.Options, Runbook: s.Template.Runbook, Params: s.Template.Params}
	for _, m := range op.validate() {
		out = append(out, "template."+m)
	}
	return out
}

package catalog

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
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
	RunArchive string      `yaml:"runArchive"`
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
}

// GaiaRoles beyond viewer, which everyone has.
var GaiaRoles = []string{"operator", "platform-admin"}

type EnvironmentPolicy struct {
	Approval         string `yaml:"approval"`
	DeleteProtection bool   `yaml:"deleteProtection"`
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
	Gateway             string      `yaml:"gateway"`
	DNS                 *DNS        `yaml:"dns"`
	AllowedEnvironments []string    `yaml:"allowedEnvironments"`
}

// Attachment is how a VM joins the network: a vSphere portgroup, a
// Proxmox bridge (with an optional VLAN tag) or a Proxmox SDN VNet.
type Attachment struct {
	Portgroup string `yaml:"portgroup"`
	Bridge    string `yaml:"bridge"`
	VLAN      int    `yaml:"vlan"`
	VNet      string `yaml:"vnet"`
}

type NetworkIPAM struct {
	Provider string `yaml:"provider" gaia:"required"`
	Pool     string `yaml:"pool" gaia:"required"`
}

type DNS struct {
	Provider string   `yaml:"provider"`
	Zone     string   `yaml:"zone"`
	Reverse  bool     `yaml:"reverse"`
	Servers  []string `yaml:"servers"`
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
	OS                  []string         `yaml:"os"`
	Monitoring          []string         `yaml:"monitoring"`
	Conflicts           []string         `yaml:"conflicts"`
	AllowedEnvironments []string         `yaml:"allowedEnvironments"`
	CheckMode           string           `yaml:"checkMode"`
	Runbook             bool             `yaml:"runbook"`
	Params              map[string]Param `yaml:"params"`
	Rules               []Rule           `yaml:"rules"`
}

type RoleSource struct {
	Provider string `yaml:"provider" gaia:"required"`
	Role     string `yaml:"role" gaia:"required"`
	Revision string `yaml:"revision"`
}

// Param follows Ansible's argument_specs, with Gaia's extensions under
// gaia (design 6.2).
type Param struct {
	Type        string           `yaml:"type"`
	Required    bool             `yaml:"required"`
	Default     any              `yaml:"default"`
	Choices     []any            `yaml:"choices"`
	Elements    string           `yaml:"elements"`
	Options     map[string]Param `yaml:"options"`
	NoLog       bool             `yaml:"no_log"`
	Description any              `yaml:"description"`
	Gaia        *ParamExt        `yaml:"gaia"`
}

type ParamExt struct {
	Min        *float64 `yaml:"min"`
	Max        *float64 `yaml:"max"`
	Pattern    string   `yaml:"pattern"`
	Format     string   `yaml:"format"`
	Secret     bool     `yaml:"secret"`
	Ref        string   `yaml:"ref"`
	Deprecated string   `yaml:"deprecated"`
	Freeform   bool     `yaml:"freeform"`
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

func (s *ServerSpec) validate() []string {
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
	GroupsClaim  string   `yaml:"groupsClaim"` // until teams come from AD (roadmap phase 6)
	MFA          *OIDCMFA `yaml:"mfa"`
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

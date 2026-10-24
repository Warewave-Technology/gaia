package catalog

import (
	"fmt"
	"sort"
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
	URL         string        `yaml:"url"`    // git
	Path        string        `yaml:"path"`   // git
	Ref         string        `yaml:"ref"`    // git
}

type ProviderAuth struct {
	Method string `yaml:"method" gaia:"required"`
	Mount  string `yaml:"mount"`
}

// ProviderTypes are the provider types Gaia knows.
var ProviderTypes = []string{"activedirectory", "git", "ipam", "nexus", "openbao", "postgres", "proxmox", "s3", "vsphere", "warpgate", "zabbix"}

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
	default:
		if s.Endpoint == "" {
			out = append(out, "endpoint: required for type "+s.Type)
		}
	}
	if s.Type == "s3" && s.Bucket == "" {
		out = append(out, "bucket: required for type s3")
	}
	if s.Credentials != "" && !strings.HasPrefix(s.Credentials, "bao://") {
		out = append(out, "credentials: must be a bao:// reference")
	}
	return out
}

// PolicySpec holds naming and environment rules.
type PolicySpec struct {
	Naming         string                       `yaml:"naming"`
	RequiredLabels []string                     `yaml:"requiredLabels"`
	Environments   map[string]EnvironmentPolicy `yaml:"environments"`
}

type EnvironmentPolicy struct {
	Approval         string `yaml:"approval"`
	DeleteProtection bool   `yaml:"deleteProtection"`
}

func (s *PolicySpec) validate() []string {
	var out []string
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

package catalog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// APIVersion is the only API version this Gaia understands.
const APIVersion = "gaia/v1alpha1"

// Kinds of the PoC. Every object is identified by its kind and name.
const (
	KindPlatform    = "Platform"
	KindProvider    = "Provider"
	KindPolicy      = "Policy"
	KindSite        = "Site"
	KindNetwork     = "Network"
	KindImage       = "Image"
	KindSize        = "Size"
	KindBaseline    = "Baseline"
	KindAnsibleRole = "AnsibleRole"
	KindServer      = "Server"
	KindAuthConfig  = "AuthConfig"
	KindTeam        = "Team"
	KindTeamSource  = "TeamSource"
	KindGroup       = "Group"
	KindNetworkSrc  = "NetworkSource"
	KindRoleSource  = "AnsibleRoleSource"
)

// newSpec returns an empty spec for each known kind.
var newSpec = map[string]func() any{
	KindPlatform:    func() any { return &PlatformSpec{} },
	KindProvider:    func() any { return &ProviderSpec{} },
	KindPolicy:      func() any { return &PolicySpec{} },
	KindSite:        func() any { return &SiteSpec{} },
	KindNetwork:     func() any { return &NetworkSpec{} },
	KindImage:       func() any { return &ImageSpec{} },
	KindSize:        func() any { return &SizeSpec{} },
	KindBaseline:    func() any { return &BaselineSpec{} },
	KindAnsibleRole: func() any { return &AnsibleRoleSpec{} },
	KindServer:      func() any { return &ServerSpec{} },
	KindAuthConfig:  func() any { return &AuthConfigSpec{} },
	KindTeam:        func() any { return &TeamSpec{} },
	KindTeamSource:  func() any { return &TeamSourceSpec{} },
	KindGroup:       func() any { return &GroupSpec{} },
	KindNetworkSrc:  func() any { return &NetworkSourceSpec{} },
	KindRoleSource:  func() any { return &AnsibleRoleSourceSpec{} },
}

// Kinds lists the known kinds in a stable order.
func Kinds() []string {
	out := make([]string, 0, len(newSpec))
	for k := range newSpec {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Key identifies an object.
type Key struct {
	Kind string
	Name string
}

func (k Key) String() string { return k.Kind + "/" + k.Name }

// Metadata is common to every object.
type Metadata struct {
	Name        string            `yaml:"name" gaia:"required"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// Source is where an object was read from.
type Source struct {
	File string
	Line int
}

func (s Source) String() string { return fmt.Sprintf("%s:%d", s.File, s.Line) }

// Object is one loaded document. Spec holds the kind's spec type, for
// example *NetworkSpec for a Network.
type Object struct {
	APIVersion string
	Kind       string
	Metadata   Metadata
	Spec       any
	Source     Source
	// Patches are the patch objects merged into this generated object.
	Patches []Source

	node *yaml.Node // the document, for positions of fields
}

// Line returns the line of a field given as a path such as
// "spec.templates[0].provider", or of the nearest parent that exists.
func (o *Object) Line(field string) int {
	n := o.node
	if n == nil {
		return o.Source.Line
	}
	line := n.Line
	for _, part := range strings.Split(field, ".") {
		name, idx := part, -1
		if i := strings.IndexByte(part, '['); i >= 0 && strings.HasSuffix(part, "]") {
			name = part[:i]
			idx, _ = strconv.Atoi(part[i+1 : len(part)-1])
		}
		key, val := mappingEntry(n, name)
		if val == nil {
			return line
		}
		n, line = val, key.Line
		if idx >= 0 {
			if n.Kind != yaml.SequenceNode || idx >= len(n.Content) {
				return line
			}
			n = n.Content[idx]
			line = n.Line
		}
	}
	return line
}

func mappingEntry(n *yaml.Node, key string) (k, v *yaml.Node) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

// Key returns the object's identity.
func (o *Object) Key() Key { return Key{Kind: o.Kind, Name: o.Metadata.Name} }

// Catalog is the set of objects read from one commit.
type Catalog struct {
	Commit  string
	objects map[Key]*Object
}

func newCatalog(commit string) *Catalog {
	return &Catalog{Commit: commit, objects: map[Key]*Object{}}
}

// Get returns the object with the given kind and name.
func (c *Catalog) Get(kind, name string) (*Object, bool) {
	o, ok := c.objects[Key{Kind: kind, Name: name}]
	return o, ok
}

// List returns the objects of one kind, sorted by name.
func (c *Catalog) List(kind string) []*Object {
	var out []*Object
	for k, o := range c.objects {
		if k.Kind == kind {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out
}

// Len is the number of objects.
func (c *Catalog) Len() int { return len(c.objects) }

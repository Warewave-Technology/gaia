package catalog

import (
	"fmt"
	"sort"
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

package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// contractKinds are the contract repository's kinds: the catalog API
// leaves them out, the contracts API shows them with their visibility.
var contractKinds = map[string]bool{catalog.KindServer: true, catalog.KindOperation: true, catalog.KindSchedule: true}

// CatalogObject is a catalog object in the list (design 11.5).
type CatalogObject struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	// Source is where it is written; GeneratedBy the Source that wrote it.
	Source      string `json:"source"`
	GeneratedBy string `json:"generatedBy,omitempty"`
	Patches     int    `json:"patches,omitempty"`
	UsedBy      int    `json:"usedBy"`
}

// CatalogRef is one reference of a field to another object.
type CatalogRef struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Field string `json:"field"`
}

// CatalogDetail is one catalog object in full.
type CatalogDetail struct {
	CatalogObject
	Annotations map[string]string `json:"annotations,omitempty"`
	PatchFiles  []string          `json:"patchFiles,omitempty"`
	YAML        string            `json:"yaml"`
	Refs        []CatalogRef      `json:"refs"`
	UsedByRefs  []CatalogRef      `json:"usedByRefs"`
}

func (s *Server) graph(w http.ResponseWriter) *catalog.Graph {
	if s.cfg.Catalog != nil {
		if g := s.cfg.Catalog.Graph(); g != nil {
			return g
		}
	}
	writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
	return nil
}

func catalogObject(g *catalog.Graph, o *catalog.Object) CatalogObject {
	return CatalogObject{
		Kind: o.Kind, Name: o.Metadata.Name, Labels: o.Metadata.Labels, Source: o.Source.String(),
		GeneratedBy: o.Metadata.Annotations[catalog.AnnotationGeneratedBy], Patches: len(o.Patches), UsedBy: len(g.UsedBy(o.Key())),
	}
}

// listCatalog is the desired catalog's objects, by kind and name;
// ?kind= keeps one kind.
func (s *Server) listCatalog(w http.ResponseWriter, r *http.Request) {
	g := s.graph(w)
	if g == nil {
		return
	}
	want := r.URL.Query().Get("kind")
	out := struct {
		Commit  string          `json:"commit"`
		Objects []CatalogObject `json:"objects"`
	}{Commit: g.Catalog.Commit, Objects: []CatalogObject{}}
	for _, k := range catalog.Kinds() {
		if contractKinds[k] || (want != "" && k != want) {
			continue
		}
		for _, o := range g.Catalog.List(k) {
			out.Objects = append(out.Objects, catalogObject(g, o))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func refs(rs []catalog.Ref, to bool) []CatalogRef {
	out := []CatalogRef{}
	for _, r := range rs {
		k := r.From
		if to {
			k = r.To
		}
		out = append(out, CatalogRef{Kind: k.Kind, Name: k.Name, Field: r.Field})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// getCatalogObject is one object: its YAML, what it refers to and what
// refers to it. Contracts that refer to it are named, as everywhere.
func (s *Server) getCatalogObject(w http.ResponseWriter, r *http.Request) {
	g := s.graph(w)
	if g == nil {
		return
	}
	kind, name := r.PathValue("kind"), r.PathValue("name")
	o, ok := g.Catalog.Get(kind, name)
	if !ok || contractKinds[kind] {
		writeJSON(w, http.StatusNotFound, Problem{Error: "no such object: " + kind + "/" + name})
		return
	}
	y, err := o.YAML()
	if err != nil {
		s.fail(w, err)
		return
	}
	d := CatalogDetail{
		CatalogObject: catalogObject(g, o), Annotations: o.Metadata.Annotations, YAML: string(y),
		Refs: refs(g.Refs(o.Key()), true), UsedByRefs: refs(g.UsedBy(o.Key()), false),
	}
	for _, p := range o.Patches {
		d.PatchFiles = append(d.PatchFiles, p.String())
	}
	writeJSON(w, http.StatusOK, d)
}

// ProviderInfo is a provider and how it is used (design 11.5).
type ProviderInfo struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"`
	Endpoint    string       `json:"endpoint,omitempty"`
	Credentials string       `json:"credentials,omitempty"`
	UsedBy      []CatalogRef `json:"usedBy"`
	// Error is what the last discovery run got from it, if it failed.
	Error string `json:"error,omitempty"`
}

// listProviders is the catalog's providers with where they are used and
// the last discovery's errors (platform-admin).
func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	g := s.graph(w)
	if g == nil {
		return
	}
	errs := map[string]string{}
	if rep, err := s.store.Q().LatestDiscovery(r.Context()); err == nil {
		var body struct {
			Errors map[string]string `json:"errors"`
		}
		if json.Unmarshal(rep.Report, &body) == nil {
			errs = body.Errors
		}
	}
	out := []ProviderInfo{}
	for _, o := range g.Catalog.List(catalog.KindProvider) {
		sp := o.Spec.(*catalog.ProviderSpec)
		endpoint := sp.Endpoint
		if endpoint == "" {
			endpoint = sp.URL
		}
		out = append(out, ProviderInfo{
			Name: o.Metadata.Name, Type: sp.Type, Endpoint: endpoint, Credentials: sp.Credentials,
			UsedBy: refs(g.UsedBy(o.Key()), false), Error: errs[o.Metadata.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

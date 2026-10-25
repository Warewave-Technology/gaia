package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/queue"
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
	// Params is what an AnsibleRole or an App takes in a contract; Role
	// is an AnsibleRole's source and where it runs.
	Params []ParamInfo `json:"params,omitempty"`
	Rules  []RuleInfo  `json:"rules,omitempty"`
	Role   *RoleInfo   `json:"role,omitempty"`
}

// ParamInfo is one parameter, nested options flattened with dots
// (tls.cert).
type ParamInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Elements    string   `json:"elements,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []any    `json:"choices,omitempty"`
	Description string   `json:"description,omitempty"`
	Secret      bool     `json:"secret,omitempty"` // no_log, or gaia.secret
	Pattern     string   `json:"pattern,omitempty"`
	Format      string   `json:"format,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Deprecated  string   `json:"deprecated,omitempty"`
}

// RuleInfo is a CEL rule over the params and the message it gives.
type RuleInfo struct {
	Expression string `json:"expression"`
	Message    string `json:"message,omitempty"`
}

// RoleInfo is an AnsibleRole's source and constraints.
type RoleInfo struct {
	Provider            string   `json:"provider"`
	Role                string   `json:"role"`
	OS                  []string `json:"os,omitempty"`
	Monitoring          []string `json:"monitoring,omitempty"`
	Conflicts           []string `json:"conflicts,omitempty"`
	AllowedEnvironments []string `json:"allowedEnvironments,omitempty"`
	CheckMode           string   `json:"checkMode,omitempty"`
	Runbook             bool     `json:"runbook,omitempty"`
}

// paramInfos flattens a parameter schema, sorted by name.
func paramInfos(params map[string]catalog.Param, prefix string) []ParamInfo {
	var out []ParamInfo
	for name, p := range params {
		pi := ParamInfo{Name: prefix + name, Type: p.Type, Elements: p.Elements, Required: p.Required, Default: p.Default, Choices: p.Choices, Secret: p.NoLog}
		switch d := p.Description.(type) {
		case string:
			pi.Description = d
		case []any:
			var lines []string
			for _, l := range d {
				lines = append(lines, fmt.Sprint(l))
			}
			pi.Description = strings.Join(lines, " ")
		}
		if x := p.Gaia; x != nil {
			pi.Secret = pi.Secret || x.Secret
			pi.Pattern, pi.Format, pi.Min, pi.Max, pi.Deprecated = x.Pattern, x.Format, x.Min, x.Max, x.Deprecated
		}
		if pi.Secret {
			pi.Default = nil
		}
		out = append(out, pi)
		out = append(out, paramInfos(p.Options, pi.Name+".")...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ruleInfos(rules []catalog.Rule) []RuleInfo {
	var out []RuleInfo
	for _, r := range rules {
		out = append(out, RuleInfo{Expression: r.Rule, Message: r.Message})
	}
	return out
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
	switch sp := o.Spec.(type) {
	case *catalog.AnsibleRoleSpec:
		d.Params, d.Rules = paramInfos(sp.Params, ""), ruleInfos(sp.Rules)
		d.Role = &RoleInfo{Provider: sp.Source.Provider, Role: sp.Source.Role, OS: sp.OS, Monitoring: sp.Monitoring, Conflicts: sp.Conflicts,
			AllowedEnvironments: sp.AllowedEnvironments, CheckMode: sp.CheckMode, Runbook: sp.Runbook}
	case *catalog.AppSpec:
		d.Params, d.Rules = paramInfos(sp.Params, ""), ruleInfos(sp.Rules)
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
	// Check is Gaia's last check of the provider; none before the first.
	Check *ProviderCheck `json:"check,omitempty"`
}

// ProviderCheck is whether Gaia reached the provider with its
// credentials, and when it last did.
type ProviderCheck struct {
	Status    string     `json:"status"` // ok | failed | skipped
	Detail    string     `json:"detail,omitempty"`
	Error     string     `json:"error,omitempty"`
	LatencyMs int32      `json:"latencyMs"`
	CheckedAt time.Time  `json:"checkedAt"`
	LastOKAt  *time.Time `json:"lastOkAt,omitempty"`
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
	checks := map[string]*ProviderCheck{}
	rows, err := s.store.Q().ProviderChecks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, c := range rows {
		checks[c.Provider] = &ProviderCheck{Status: c.Status, Detail: c.Detail.String, Error: c.Error.String, LatencyMs: c.LatencyMs, CheckedAt: c.CheckedAt.Time, LastOKAt: timePtr(c.LastOkAt)}
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
			UsedBy: refs(g.UsedBy(o.Key()), false), Error: errs[o.Metadata.Name], Check: checks[o.Metadata.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// checkProviders asks for a provider check now (platform-admin).
func (s *Server) checkProviders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Catalog == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
		return
	}
	if _, err := s.queue.Insert(r.Context(), queue.CheckProvidersArgs{}, nil); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
}

package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// OperationState is an Operation's state and how it went per server,
// with what the contract repository says it does while it is there.
type OperationState struct {
	Name       string          `json:"name"`
	Status     string          `json:"status,omitempty"` // none before Gaia first takes it up
	Change     string          `json:"change,omitempty"`
	Run        string          `json:"run,omitempty"`
	Results    json.RawMessage `json:"results,omitempty"`
	UpdatedAt  *time.Time      `json:"updatedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Action     string          `json:"action,omitempty"`
	Selector   string          `json:"selector,omitempty"`
	Window     *OperationSpan  `json:"window,omitempty"`
	NotAfter   string          `json:"notAfter,omitempty"`
	Hosts      []string        `json:"hosts,omitempty"`
	// Schedule is the OperationSchedule that wrote it, if one did.
	Schedule string `json:"schedule,omitempty"`
}

// OperationSpan is when an Operation may run.
type OperationSpan struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// operationObjects is the Operations in the contract repository, by
// name; none when the server follows no repository.
func (s *Server) operationObjects() map[string]*catalog.Object {
	out := map[string]*catalog.Object{}
	if s.cfg.Catalog == nil {
		return out
	}
	g := s.cfg.Catalog.Graph()
	if g == nil {
		return out
	}
	for _, o := range g.Catalog.List(catalog.KindOperation) {
		out[o.Metadata.Name] = o
	}
	return out
}

func describeOperation(st *OperationState, o *catalog.Object) {
	sp := o.Spec.(*catalog.OperationSpec)
	st.Action, st.Selector, st.NotAfter, st.Hosts = sp.Action, sp.Selector, sp.NotAfter, sp.Hosts
	if sp.Window != nil {
		st.Window = &OperationSpan{Start: sp.Window.Start, End: sp.Window.End}
	}
	if by := o.Metadata.Annotations[catalog.AnnotationGeneratedBy]; by != "" {
		st.Schedule = by
	}
}

// listOperations is the Operations Gaia took up, newest first, then the
// ones in the repository it has not taken up yet.
func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Q().Operations(r.Context(), 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	defined := s.operationObjects()
	out := make([]OperationState, 0, len(rows)+len(defined))
	for _, o := range rows {
		st := OperationState{Name: o.Name, Status: o.Status, Change: o.ChangeID.String, Run: o.RunID.String,
			Results: json.RawMessage(o.Results), UpdatedAt: timePtr(o.UpdatedAt), FinishedAt: timePtr(o.FinishedAt)}
		if len(o.Results) == 0 {
			st.Results = nil
		}
		if d, ok := defined[o.Name]; ok {
			describeOperation(&st, d)
			delete(defined, o.Name)
		}
		out = append(out, st)
	}
	names := make([]string, 0, len(defined))
	for n := range defined {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := OperationState{Name: n}
		describeOperation(&st, defined[n])
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, out)
}

// ScheduleInfo is an OperationSchedule and the Operations it wrote.
type ScheduleInfo struct {
	Name        string   `json:"name"`
	Recurrence  string   `json:"recurrence"`
	Start       string   `json:"start"`
	Timezone    string   `json:"timezone,omitempty"`
	Duration    string   `json:"duration"`
	CreateAhead string   `json:"createAhead,omitempty"`
	Approval    string   `json:"approval,omitempty"`
	Action      string   `json:"action"`
	Selector    string   `json:"selector"`
	Operations  []string `json:"operations"`
}

// listSchedules is the contract repository's OperationSchedules.
func (s *Server) listSchedules(w http.ResponseWriter, _ *http.Request) {
	g := s.graph(w)
	if g == nil {
		return
	}
	made := map[string][]string{}
	for _, o := range g.Catalog.List(catalog.KindOperation) {
		if by := o.Metadata.Annotations[catalog.AnnotationGeneratedBy]; by != "" {
			made[by] = append(made[by], o.Metadata.Name)
		}
	}
	out := []ScheduleInfo{}
	for _, o := range g.Catalog.List(catalog.KindSchedule) {
		sp := o.Spec.(*catalog.OperationScheduleSpec)
		ops := made[catalog.KindSchedule+"/"+o.Metadata.Name]
		if ops == nil {
			ops = []string{}
		}
		sort.Strings(ops)
		out = append(out, ScheduleInfo{
			Name: o.Metadata.Name, Recurrence: sp.Recurrence, Start: sp.Start, Timezone: sp.Timezone, Duration: sp.Duration,
			CreateAhead: sp.CreateAhead, Approval: sp.Approval, Action: sp.Template.Action, Selector: sp.Template.Selector, Operations: ops,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// AccessEntry is one access entry of a contract (design 14.3): who may
// log in to its servers and what they may run as root.
type AccessEntry struct {
	Contract string   `json:"contract"`
	Group    string   `json:"group,omitempty"`
	Team     string   `json:"team,omitempty"`
	Sudo     bool     `json:"sudo,omitempty"` // everything
	Commands []string `json:"commands,omitempty"`
}

// listAccess is the access entries of the contracts the caller may see
// in full, by contract.
func (s *Server) listAccess(w http.ResponseWriter, r *http.Request) {
	g := s.graph(w)
	if g == nil {
		return
	}
	out := []AccessEntry{}
	for _, o := range g.Catalog.List(catalog.KindServer) {
		if !s.full(r, []string{o.Metadata.Name}) {
			continue
		}
		for _, a := range o.Spec.(*catalog.ServerSpec).Access {
			out = append(out, AccessEntry{Contract: o.Metadata.Name, Group: a.Group, Team: a.Team, Sudo: a.Sudo.All, Commands: a.Sudo.Commands})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// SettingsVersion is one version of Gaia's settings (design 17.2).
type SettingsVersion struct {
	Version     int64     `json:"version"`
	Author      string    `json:"author"`
	Reason      string    `json:"reason"`
	Proposal    string    `json:"proposal,omitempty"`
	ContentHash string    `json:"contentHash"`
	CreatedAt   time.Time `json:"createdAt"`
	Objects     int       `json:"objects,omitempty"`
}

// SettingsState is the settings the desired state is loaded with, and
// what Git still has of the settings kinds (left out).
type SettingsState struct {
	// Store is false when this Gaia keeps its settings in Git.
	Store   bool             `json:"store"`
	Current *SettingsVersion `json:"current,omitempty"`
	Ignored []IgnoredObject  `json:"ignored"`
}

// IgnoredObject is an object of a settings kind still in Git.
type IgnoredObject struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// SettingsFile is one object of a version, as it is written.
type SettingsFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	YAML string `json:"yaml"`
}

func settingsVersion(v db.SettingsVersion) SettingsVersion {
	return SettingsVersion{Version: v.Version, Author: v.Author, Reason: v.Reason, Proposal: v.Proposal.String, ContentHash: v.ContentHash, CreatedAt: v.CreatedAt.Time}
}

// getSettings is the settings version the desired state is loaded with.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	out := SettingsState{Store: s.cfg.Settings != nil, Ignored: []IgnoredObject{}}
	if s.cfg.Catalog != nil {
		snap, _ := s.cfg.Catalog.Snapshot()
		if snap.Settings > 0 {
			v, err := s.store.Q().SettingsVersion(r.Context(), snap.Settings)
			if err != nil {
				s.fail(w, err)
				return
			}
			sv := settingsVersion(v)
			out.Current = &sv
		}
		if g := s.cfg.Catalog.Graph(); g != nil {
			for _, ig := range g.Catalog.Ignored {
				out.Ignored = append(out.Ignored, IgnoredObject{Kind: ig.Key.Kind, Name: ig.Key.Name, Source: ig.Source.String()})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listSettingsVersions is the settings versions, newest first.
func (s *Server) listSettingsVersions(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "limit", 100, 1, 1000)
	if err != nil {
		badRequest(w, err)
		return
	}
	rows, err := s.store.Q().SettingsVersions(r.Context(), int32(n)) //nolint:gosec // bounded above
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]SettingsVersion, 0, len(rows))
	for _, v := range rows {
		sv := SettingsVersion{Version: v.Version, Author: v.Author, Reason: v.Reason, Proposal: v.Proposal.String, ContentHash: v.ContentHash, CreatedAt: v.CreatedAt.Time, Objects: int(v.Objects)}
		out = append(out, sv)
	}
	writeJSON(w, http.StatusOK, out)
}

// getSettingsVersion is one version with its objects as written.
func (s *Server) getSettingsVersion(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, Problem{Error: "version: a number"})
		return
	}
	v, err := s.store.Q().SettingsVersion(r.Context(), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.store.Q().SettingsObjects(r.Context(), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := struct {
		SettingsVersion
		Files []SettingsFile `json:"files"`
	}{SettingsVersion: settingsVersion(v), Files: make([]SettingsFile, 0, len(rows))}
	out.Objects = len(rows)
	for _, f := range rows {
		out.Files = append(out.Files, SettingsFile{Path: f.Path, Kind: catalog.SettingsFileKind(f.Path), YAML: f.Data})
	}
	writeJSON(w, http.StatusOK, out)
}

// AuditEntry is one entry of the audit log (design 17.4).
type AuditEntry struct {
	ID        int64           `json:"id"`
	At        time.Time       `json:"at"`
	Actor     string          `json:"actor"`
	ActorID   string          `json:"actorId,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Address   string          `json:"address,omitempty"`
	Action    string          `json:"action"`
	Object    string          `json:"object,omitempty"`
	Result    string          `json:"result"`
	Detail    json.RawMessage `json:"detail,omitempty"`
}

// listAudit is the audit log, newest first (platform-admin): by actor,
// action (a prefix: settings.), object, since a time and before an
// entry. ?format=jsonl gives one JSON object per line, for a SIEM.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, err := intParam(r, "limit", 200, 1, 10000)
	if err != nil {
		badRequest(w, err)
		return
	}
	p := db.AuditLogParams{MaxRows: int32(n)} //nolint:gosec // bounded above
	if v := q.Get("actor"); v != "" {
		p.Actor = pgtype.Text{String: v, Valid: true}
	}
	if v := q.Get("action"); v != "" {
		p.Action = pgtype.Text{String: v, Valid: true}
	}
	if v := q.Get("object"); v != "" {
		p.Object = pgtype.Text{String: v, Valid: true}
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, Problem{Error: "since: an RFC 3339 time"})
			return
		}
		p.Since = pgtype.Timestamptz{Time: t, Valid: true}
	}
	if v := q.Get("before"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, Problem{Error: "before: an entry ID"})
			return
		}
		p.BeforeID = pgtype.Int8{Int64: id, Valid: true}
	}
	rows, err := s.store.Q().AuditLog(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, e := range rows {
		a := AuditEntry{ID: e.ID, At: e.At.Time, Actor: e.Actor, ActorID: e.ActorID.String, SessionID: e.SessionID.String, Address: e.Address.String,
			Action: e.Action, Object: e.Object.String, Result: e.Result}
		if len(e.Detail) > 0 {
			a.Detail = json.RawMessage(e.Detail)
		}
		out = append(out, a)
	}
	if q.Get("format") == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="gaia-audit.jsonl"`)
		enc := json.NewEncoder(w)
		for _, a := range out {
			_ = enc.Encode(a)
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// audit writes an entry; when it cannot, the request fails: what is not
// in the log does not happen.
func (s *Server) audit(w http.ResponseWriter, r *http.Request, e audit.Entry) bool {
	if err := audit.Write(r.Context(), s.store.Q(), e); err != nil {
		s.fail(w, err)
		return false
	}
	return true
}

// auditEntry is an audit entry for the request's caller.
func auditEntry(r *http.Request, action, object string) audit.Entry {
	e := audit.Entry{Action: action, Object: object, Actor: "anonymous", Address: clientAddress(r)}
	if p := auth.From(r.Context()); p != nil {
		e.Actor, e.ActorID, e.SessionID = p.Username, p.UserID, p.SessionID
	}
	return e
}

// clientAddress is the caller's address, without its port.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

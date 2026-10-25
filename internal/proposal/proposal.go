// Package proposal is how Gaia's settings change (design 17.3): a person
// proposes adding, changing or removing settings objects; Gaia checks
// that the result loads and says which contracts' plans it changes; an
// approver who is not the proposer decides, bound to the content they
// saw; an approved proposal becomes a new settings version, and the
// contracts it changes get their own changes, approved as usual.
package proposal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/settings"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

var (
	ErrInvalid     = errors.New("the settings would not load")
	ErrNotApprover = errors.New("you may not decide on this settings change")
	ErrOwn         = errors.New("you proposed this settings change; someone else decides on it")
	ErrClosed      = errors.New("the settings change is no longer pending")
	ErrContent     = errors.New("the settings change is not the one you saw")
	ErrStale       = errors.New("the settings it changes changed meanwhile; propose it again")
)

// Desired is the server's view of the desired state.
type Desired interface {
	Snapshot() (repo.Snapshot, error)
	Kick()
}

// Service checks, records and decides settings changes.
type Service struct {
	Store    *store.Store
	Settings *settings.Store
	Repos    *repo.Mirrors
	Desired  Desired
	Notify   *notify.Notifier
	Log      *slog.Logger
}

// Edit is one object to add or change (YAML) or remove (Kind, Name and
// Delete).
type Edit struct {
	YAML   string `json:"yaml,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Name   string `json:"name,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// Object is what a change does to one object.
type Object struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Op     string `json:"op"` // add | change | remove
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Impact is a contract whose plan the change changes, or that it leaves
// without a plan.
type Impact struct {
	Contract string `json:"contract"`
	Error    string `json:"error,omitempty"`
}

// Check is what a change would do.
type Check struct {
	Base        int64    `json:"base"`
	ContentHash string   `json:"contentHash"`
	Objects     []Object `json:"objects"`
	Errors      []string `json:"errors"`
	Impact      []Impact `json:"impact"`
	// Critical changes are decided by a platform admin only.
	Critical    bool     `json:"critical"`
	WhyCritical []string `json:"whyCritical,omitempty"`

	changes map[string][]byte
}

// Person is who acts, as the audit log names them.
type Person struct {
	UserID, Username, SessionID, Address string
	Teams, Roles                         []string
}

func (p Person) entry(action, object string) audit.Entry {
	return audit.Entry{Actor: p.Username, ActorID: p.UserID, SessionID: p.SessionID, Address: p.Address, Action: action, Object: object}
}

// changes turns edits into settings files.
func changes(edits []Edit) (map[string][]byte, []string) {
	out := map[string][]byte{}
	var errs []string
	for i, e := range edits {
		if e.Delete {
			if !catalog.IsSetting(e.Kind) || e.Name == "" {
				errs = append(errs, fmt.Sprintf("change %d: remove needs a settings kind and a name", i+1))
				continue
			}
			out[catalog.SettingsPath(e.Kind, e.Name, false)] = nil
			continue
		}
		var docs []yaml.Node
		dec := yaml.NewDecoder(strings.NewReader(e.YAML))
		for {
			var n yaml.Node
			if err := dec.Decode(&n); err != nil {
				if err.Error() != "EOF" {
					errs = append(errs, fmt.Sprintf("change %d: %v", i+1, err))
				}
				break
			}
			docs = append(docs, n)
		}
		if len(docs) != 1 {
			errs = append(errs, fmt.Sprintf("change %d: one object per change", i+1))
			continue
		}
		var head struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name        string            `yaml:"name"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
		}
		if err := docs[0].Decode(&head); err != nil || head.Metadata.Name == "" {
			errs = append(errs, fmt.Sprintf("change %d: an object needs kind and metadata.name", i+1))
			continue
		}
		if !catalog.IsSetting(head.Kind) {
			errs = append(errs, fmt.Sprintf("change %d: %s is not a setting; it is changed in Git", i+1, head.Kind))
			continue
		}
		for k := range head.Metadata.Annotations {
			if strings.HasPrefix(k, "gaia/") {
				errs = append(errs, fmt.Sprintf("%s/%s: %s is Gaia's own annotation", head.Kind, head.Metadata.Name, k))
			}
		}
		p := catalog.SettingsPath(head.Kind, head.Metadata.Name, false)
		if _, dup := out[p]; dup {
			errs = append(errs, fmt.Sprintf("%s/%s: changed twice", head.Kind, head.Metadata.Name))
			continue
		}
		data := []byte(e.YAML)
		if !strings.HasSuffix(e.YAML, "\n") {
			data = append(data, '\n')
		}
		out[p] = data
	}
	return out, errs
}

// ContentHash binds a decision to the change: every path and what it
// becomes.
func ContentHash(changes map[string][]byte) string {
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		if changes[p] == nil {
			_, _ = fmt.Fprintf(h, "%s\x00remove\x00", p)
			continue
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", p, len(changes[p]))
		_, _ = h.Write(changes[p])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func generatedBy(data []byte) string {
	var head struct {
		Metadata struct {
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	_ = yaml.Unmarshal(data, &head)
	return head.Metadata.Annotations[catalog.AnnotationGeneratedBy]
}

// Check says what edits would do at the latest settings version.
func (s *Service) Check(ctx context.Context, edits []Edit) (*Check, error) {
	ch, errs := changes(edits)
	base, err := s.Settings.Latest(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.check(ctx, base, ch)
	if err != nil {
		return nil, err
	}
	c.Errors = append(errs, c.Errors...)
	return c, nil
}

func (s *Service) check(ctx context.Context, base int64, ch map[string][]byte) (*Check, error) {
	c := &Check{Base: base, ContentHash: ContentHash(ch), Objects: []Object{}, Errors: []string{}, Impact: []Impact{}, changes: ch}
	if base == 0 {
		c.Errors = append(c.Errors, "this Gaia keeps its settings in Git; change them there")
		return c, nil
	}
	if len(ch) == 0 {
		c.Errors = append(c.Errors, "nothing to change")
		return c, nil
	}
	baseFiles, err := s.Settings.Files(ctx, base)
	if err != nil {
		return nil, err
	}
	have := map[string][]byte{}
	for _, f := range baseFiles {
		have[f.Path] = f.Data
	}
	paths := make([]string, 0, len(ch))
	for p := range ch {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		kind := catalog.SettingsFileKind(p)
		name := strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".yaml")
		o := Object{Kind: kind, Name: name, Path: p, Before: string(have[p]), After: string(ch[p])}
		_, exists := have[p]
		switch {
		case ch[p] == nil && !exists:
			c.Errors = append(c.Errors, fmt.Sprintf("%s/%s: there is no such object to remove", kind, name))
			continue
		case ch[p] == nil:
			o.Op = "remove"
		case !exists:
			o.Op = "add"
		case string(have[p]) == string(ch[p]):
			continue
		default:
			o.Op = "change"
		}
		if by := generatedBy(have[p]); by != "" {
			c.Errors = append(c.Errors, fmt.Sprintf("%s/%s is written by %s; change it at its source", kind, name, by))
		}
		c.Objects = append(c.Objects, o)
	}
	if len(c.Objects) == 0 && len(c.Errors) == 0 {
		c.Errors = append(c.Errors, "nothing to change")
	}
	if len(c.Errors) > 0 {
		return c, nil
	}

	snap, err := s.Desired.Snapshot()
	if err != nil && snap.Platform.Commit == "" {
		return nil, err
	}
	snap.Settings = base
	files, _ := settings.Apply(baseFiles, ch)
	next, err := s.Repos.GraphWithSettings(ctx, snap, files)
	if err != nil {
		var errs catalog.Errors
		if errors.As(err, &errs) {
			for _, e := range errs {
				c.Errors = append(c.Errors, e.Error())
			}
		} else {
			c.Errors = append(c.Errors, err.Error())
		}
		return c, nil
	}
	cur, err := s.Repos.Graph(ctx, snap)
	if err != nil {
		return nil, err
	}
	c.Critical, c.WhyCritical = critical(cur, next, c.Objects)
	c.Impact, err = s.impact(ctx, cur, next)
	if err != nil {
		return nil, err
	}
	for _, i := range c.Impact {
		if i.Error != "" {
			c.Errors = append(c.Errors, fmt.Sprintf("%s: no plan with this change: %s", i.Contract, i.Error))
		}
	}
	return c, nil
}

// critical says whether only a platform admin may decide, and why.
func critical(cur, next *catalog.Graph, objs []Object) (bool, []string) {
	var why []string
	privileged := map[string]bool{}
	for _, g := range []*catalog.Graph{cur, next} {
		for _, o := range g.Catalog.List(catalog.KindGroup) {
			if gs := o.Spec.(*catalog.GroupSpec); gs.Privileged {
				for _, t := range gs.Teams {
					privileged[t] = true
				}
			}
		}
	}
	for _, o := range objs {
		switch {
		case slices.Contains(catalog.CriticalSettings, o.Kind):
			why = append(why, fmt.Sprintf("%s/%s: %s is decided by a platform admin", o.Kind, o.Name, o.Kind))
		case o.Kind == catalog.KindTeam && privileged[o.Name]:
			why = append(why, fmt.Sprintf("%s/%s is in a privileged group", o.Kind, o.Name))
		}
	}
	return len(why) > 0, why
}

// impact is the contracts whose plan changes, by name.
func (s *Service) impact(ctx context.Context, cur, next *catalog.Graph) ([]Impact, error) {
	names := map[string]bool{}
	for _, g := range []*catalog.Graph{cur, next} {
		for _, o := range g.Catalog.List(catalog.KindServer) {
			names[o.Metadata.Name] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	st := apply.StoreState{S: s.Store}
	out := []Impact{}
	for _, n := range sorted {
		before, berr := plan.Build(ctx, cur, []string{n}, st)
		after, aerr := plan.Build(ctx, next, []string{n}, st)
		switch {
		case aerr != nil && berr == nil:
			out = append(out, Impact{Contract: n, Error: aerr.Error()})
		case aerr != nil:
		case berr != nil || before.Hash != after.Hash:
			out = append(out, Impact{Contract: n})
		}
	}
	return out, nil
}

// Propose records a change that checks out and asks its approvers.
func (s *Service) Propose(ctx context.Context, p Person, title, reason string, edits []Edit) (*db.SettingsProposal, *Check, error) {
	c, err := s.Check(ctx, edits)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(title) == "" {
		c.Errors = append(c.Errors, "say in a few words what it changes (title)")
	}
	if len(c.Errors) > 0 {
		return nil, c, ErrInvalid
	}
	chJSON, _ := json.Marshal(stringChanges(c.changes))
	impact, _ := json.Marshal(c.Impact)
	id := store.NewProposalID()
	err = s.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertSettingsProposal(ctx, db.InsertSettingsProposalParams{
			ID: id, BaseVersion: c.Base, Changes: chJSON, ContentHash: c.ContentHash, Title: strings.TrimSpace(title), Reason: strings.TrimSpace(reason),
			RequestedBy: p.Username, RequestedByID: text(p.UserID), Critical: c.Critical, WhyCritical: nonNil(c.WhyCritical), Impact: impact,
		}); err != nil {
			return err
		}
		e := p.entry("settings.propose", id)
		e.Detail = map[string]any{"title": title, "base": c.Base, "paths": keys(c.changes), "impact": len(c.Impact), "critical": c.Critical}
		return audit.Write(ctx, q, e)
	})
	if err != nil {
		return nil, nil, err
	}
	pr, err := s.Store.Q().SettingsProposal(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	s.emit(ctx, "settings.pending", pr, "")
	return &pr, c, nil
}

// Changes is a proposal's changes as files.
func Changes(pr db.SettingsProposal) map[string][]byte {
	var m map[string]*string
	_ = json.Unmarshal(pr.Changes, &m)
	out := map[string][]byte{}
	for p, v := range m {
		if v == nil {
			out[p] = nil
		} else {
			out[p] = []byte(*v)
		}
	}
	return out
}

func stringChanges(ch map[string][]byte) map[string]*string {
	out := map[string]*string{}
	for p, v := range ch {
		if v == nil {
			out[p] = nil
			continue
		}
		s := string(v)
		out[p] = &s
	}
	return out
}

// MayDecide says whether a person may decide on a proposal.
func (s *Service) MayDecide(ctx context.Context, p Person, pr db.SettingsProposal) error {
	if pr.RequestedByID.Valid && pr.RequestedByID.String == p.UserID || pr.RequestedBy == p.Username {
		return ErrOwn
	}
	if slices.Contains(p.Roles, "platform-admin") {
		return nil
	}
	if pr.Critical {
		return fmt.Errorf("%w: %s", ErrNotApprover, strings.Join(pr.WhyCritical, "; "))
	}
	snap, _ := s.Desired.Snapshot()
	g, err := s.Repos.Graph(ctx, snap)
	if err != nil {
		return err
	}
	for _, t := range g.SettingsApprovers() {
		if slices.Contains(p.Teams, t) {
			return nil
		}
	}
	return fmt.Errorf("%w: a platform admin or the Policy's settings approvers decide", ErrNotApprover)
}

// Decision is a decision on a proposal.
type Decision struct {
	ID          string
	ContentHash string
	Approve     bool
	Comment     string
}

// Decide approves (applying the change as a new version) or rejects a
// proposal.
func (s *Service) Decide(ctx context.Context, p Person, d Decision) (*db.SettingsProposal, error) {
	q := s.Store.Q()
	pr, err := q.SettingsProposal(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if pr.Status != "pending" {
		return nil, ErrClosed
	}
	if d.ContentHash != pr.ContentHash {
		return nil, ErrContent
	}
	if err := s.MayDecide(ctx, p, pr); err != nil {
		return nil, err
	}
	comment := strings.TrimSpace(d.Comment)
	if !d.Approve {
		if comment == "" {
			return nil, errors.New("say why you reject")
		}
		err := s.Store.Tx(ctx, func(q *db.Queries) error {
			n, err := q.DecideSettingsProposal(ctx, db.DecideSettingsProposalParams{ID: pr.ID, Status: "rejected", DecidedBy: text(p.Username), DecidedByID: text(p.UserID), Comment: text(comment)})
			if err != nil || n == 0 {
				return errors.Join(err, closedIf(n == 0))
			}
			e := p.entry("settings.reject", pr.ID)
			e.Detail = map[string]string{"comment": comment}
			return audit.Write(ctx, q, e)
		})
		if err != nil {
			return nil, err
		}
		pr, _ = q.SettingsProposal(ctx, pr.ID)
		s.emit(ctx, "settings.rejected", pr, "")
		return &pr, nil
	}

	ch := Changes(pr)
	latest, err := s.Settings.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if latest != pr.BaseVersion {
		// Others changed the settings since: fine as long as not these
		// objects, and the change still checks out.
		if why, err := s.moved(ctx, pr.BaseVersion, latest, ch); err != nil || why != "" {
			if err != nil {
				return nil, err
			}
			return nil, s.stale(ctx, pr, why)
		}
		c, err := s.check(ctx, latest, ch)
		if err != nil {
			return nil, err
		}
		if len(c.Errors) > 0 {
			return nil, s.stale(ctx, pr, strings.Join(c.Errors, "; "))
		}
	}
	e := p.entry("settings.apply", "")
	e.Detail = map[string]string{"proposal": pr.ID, "requestedBy": pr.RequestedBy}
	diff, err := s.Settings.Write(ctx, settings.Write{Base: latest, Changes: ch, Author: pr.RequestedBy, Reason: pr.Title, Proposal: pr.ID, Audit: e})
	switch {
	case errors.Is(err, settings.ErrStale):
		return nil, fmt.Errorf("%w (another version was written just now; try again)", ErrStale)
	case err != nil:
		return nil, err
	}
	err = s.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DecideSettingsProposal(ctx, db.DecideSettingsProposalParams{ID: pr.ID, Status: "applied", DecidedBy: text(p.Username), DecidedByID: text(p.UserID), Comment: text(comment),
			AppliedVersion: pgtype.Int8{Int64: diff.Version, Valid: diff.Version > 0}})
		if err != nil || n == 0 {
			return errors.Join(err, closedIf(n == 0))
		}
		e := p.entry("settings.approve", pr.ID)
		e.Detail = map[string]any{"version": diff.Version, "comment": comment}
		return audit.Write(ctx, q, e)
	})
	if err != nil {
		return nil, err
	}
	pr, _ = q.SettingsProposal(ctx, pr.ID)
	s.emit(ctx, "settings.applied", pr, fmt.Sprintf("v%d", diff.Version))
	if s.Desired != nil {
		s.Desired.Kick()
	}
	return &pr, nil
}

// moved says why a proposal no longer applies when the same objects
// changed between its base and the latest version.
func (s *Service) moved(ctx context.Context, base, latest int64, ch map[string][]byte) (string, error) {
	a, err := s.Settings.Files(ctx, base)
	if err != nil {
		return "", err
	}
	b, err := s.Settings.Files(ctx, latest)
	if err != nil {
		return "", err
	}
	was := map[string]string{}
	for _, f := range a {
		was[f.Path] = string(f.Data)
	}
	now := map[string]string{}
	for _, f := range b {
		now[f.Path] = string(f.Data)
	}
	var moved []string
	for p := range ch {
		if was[p] != now[p] {
			moved = append(moved, strings.TrimSuffix(strings.TrimPrefix(p, catalog.SettingsPrefix), ".yaml"))
		}
	}
	if len(moved) == 0 {
		return "", nil
	}
	sort.Strings(moved)
	return fmt.Sprintf("v%d'den sonra değişti: %s", base, strings.Join(moved, ", ")), nil
}

func (s *Service) stale(ctx context.Context, pr db.SettingsProposal, why string) error {
	if _, err := s.Store.Q().SetSettingsProposalStale(ctx, db.SetSettingsProposalStaleParams{ID: pr.ID, StaleReason: text(why)}); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrStale, why)
}

// Withdraw takes back one's own pending proposal.
func (s *Service) Withdraw(ctx context.Context, p Person, id string) error {
	pr, err := s.Store.Q().SettingsProposal(ctx, id)
	if err != nil {
		return err
	}
	if pr.RequestedBy != p.Username {
		return fmt.Errorf("%w: only who proposed it may withdraw it", ErrNotApprover)
	}
	return s.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DecideSettingsProposal(ctx, db.DecideSettingsProposalParams{ID: id, Status: "withdrawn", DecidedBy: text(p.Username), DecidedByID: text(p.UserID)})
		if err != nil || n == 0 {
			return errors.Join(err, closedIf(n == 0))
		}
		return audit.Write(ctx, q, p.entry("settings.withdraw", id))
	})
}

func closedIf(b bool) error {
	if b {
		return ErrClosed
	}
	return nil
}

// emit tells the approvers (pending) or everyone who follows settings
// (applied, rejected).
func (s *Service) emit(ctx context.Context, trigger string, pr db.SettingsProposal, version string) {
	if s.Notify == nil {
		return
	}
	snap, _ := s.Desired.Snapshot()
	g, err := s.Repos.Graph(ctx, snap)
	if err != nil {
		return
	}
	teams := g.TeamsWithRole("platform-admin")
	if !pr.Critical {
		teams = append(teams, g.SettingsApprovers()...)
	}
	var impact []Impact
	_ = json.Unmarshal(pr.Impact, &impact)
	ev := notify.Event{
		Trigger: trigger, Change: pr.ID, Key: pr.ID + "/" + trigger, Status: pr.Title, RequestedBy: []string{pr.RequestedBy},
		Comment: pr.Reason, Summary: notify.Summary{Keep: len(impact)}, Teams: teams, Label: version,
	}
	if trigger == "settings.rejected" {
		ev.DecidedBy, ev.Comment = pr.DecidedBy.String, pr.Comment.String
	}
	if a := g.AuthConfig(); a != nil {
		ev.URL = a.URL + "/changes/settings/" + pr.ID
	}
	if err := s.Notify.Emit(ctx, g, ev); err != nil && s.Log != nil {
		s.Log.Warn("settings notification", "proposal", pr.ID, "err", err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// IsNotFound reports a proposal that does not exist.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

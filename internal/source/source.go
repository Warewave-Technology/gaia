// Package source turns outside systems into catalog objects (design 5):
// a Source reads its system, renders the objects it stands for, and when
// they differ from the platform repository Gaia writes them there in a
// signed commit. Gaia never writes a commit whose catalog does not load.
// What happened is kept as the source's report.
package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.yaml.in/yaml/v3"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/settings"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Secrets reads a bao:// reference at an OpenBao address.
type Secrets func(ctx context.Context, addr, ref string) (map[string]string, error)

// Syncer runs sources.
type Syncer struct {
	Store   *store.Store
	Repos   *repo.Mirrors
	Writer  *repo.Writer
	Secrets Secrets
	Log     *slog.Logger
	// Notify reports what needs to be seen, such as AD adding someone to
	// a privileged group's team.
	Notify *notify.Notifier
	// Settings is where networks and teams go once the settings store is
	// in use (design 17.6); roles and operations stay in Git.
	Settings *settings.Store
}

func (s *Syncer) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Report is what one run of a source did.
type Report struct {
	Source   string   `json:"source"`
	Status   string   `json:"status"` // unchanged | committed | stored | refused | failed
	Commit   string   `json:"commit,omitempty"`
	Version  int64    `json:"version,omitempty"` // the settings version it stored
	Added    []string `json:"added,omitempty"`
	Changed  []string `json:"changed,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Problems []string `json:"problems,omitempty"` // what was left out, and why
	Error    string   `json:"error,omitempty"`
	// A directory's membership sync says who joined or left a team and
	// whose account the directory closed or opened.
	Membership *Membership `json:"membership,omitempty"`
}

// Run runs every source of the catalog once and records their reports.
// It returns whether any of them wrote a commit.
func (s *Syncer) Run(ctx context.Context, snap repo.Snapshot, g *catalog.Graph) (bool, error) {
	wrote := false
	var errs []error
	run := func(rep Report) {
		wrote = wrote || rep.Status == "committed" || rep.Status == "stored"
		if err := s.save(ctx, rep); err != nil {
			errs = append(errs, err)
		}
	}
	for _, o := range g.Catalog.List(catalog.KindTeamSource) {
		run(s.teams(ctx, snap, g, o))
	}
	for _, o := range g.Catalog.List(catalog.KindNetworkSrc) {
		run(s.networks(ctx, snap, g, o))
	}
	for _, o := range g.Catalog.List(catalog.KindRoleSource) {
		run(s.roles(ctx, snap, g, o))
	}
	for _, o := range g.Catalog.List(catalog.KindSchedule) {
		run(s.schedules(ctx, snap, g, o, time.Now()))
	}
	return wrote, errors.Join(errs...)
}

func (s *Syncer) save(ctx context.Context, r Report) error {
	d := map[string]any{"added": r.Added, "changed": r.Changed, "removed": r.Removed, "problems": r.Problems, "version": r.Version}
	if m := r.Membership; m != nil {
		d = map[string]any{"joined": m.Joined, "left": m.Left, "disabled": m.Disabled, "enabled": m.Enabled}
	}
	detail, _ := json.Marshal(d)
	s.log().Info("source", "source", r.Source, "status", r.Status, "commit", r.Commit, "version", r.Version, "err", r.Error)
	return s.Store.Q().SetSourceReport(ctx, db.SetSourceReportParams{
		Source: r.Source, Status: r.Status, Commit: text(r.Commit), Detail: detail, Error: text(r.Error),
	})
}

// secret reads a provider's credentials from the Platform's OpenBao.
func (s *Syncer) secret(ctx context.Context, g *catalog.Graph, ps *catalog.ProviderSpec, name string) (map[string]string, error) {
	if ps.Credentials == "" {
		return map[string]string{}, nil
	}
	if s.Secrets == nil {
		return nil, fmt.Errorf("%s: no access to OpenBao", name)
	}
	addr := ""
	if b, ok := g.Catalog.Get(catalog.KindProvider, g.Platform().Spec.(*catalog.PlatformSpec).Secrets); ok {
		addr = b.Spec.(*catalog.ProviderSpec).Endpoint
	}
	return s.Secrets(ctx, addr, ps.Credentials)
}

// outputs is the kind each source writes.
var outputs = map[string]string{
	catalog.KindTeamSource: catalog.KindTeam, catalog.KindNetworkSrc: catalog.KindNetwork,
	catalog.KindRoleSource: catalog.KindAnsibleRole, catalog.KindSchedule: catalog.KindOperation,
}

// write compares a source's rendered files with its output directory at
// the snapshot and, when they differ and the result loads, commits them.
// Settings go to the settings store instead, once it is in use.
func (s *Syncer) write(ctx context.Context, snap repo.Snapshot, rep *Report, output string, want map[string][]byte) {
	if kind := outputs[strings.SplitN(rep.Source, "/", 2)[0]]; catalog.IsSetting(kind) && snap.Settings > 0 {
		s.store(ctx, snap, rep, kind, want)
		return
	}
	plat := snap.Platform
	current, _, err := gitsrc.Load(s.Repos.Path(plat.Repo()), plat.Commit, []string{output})
	if err != nil && !errors.Is(err, gitsrc.ErrNoDirectory) {
		rep.Status, rep.Error = "failed", err.Error()
		return
	}
	have := map[string][]byte{}
	for _, f := range current {
		have[f.Path] = f.Data
	}
	changes := diff(rep, have, want)
	if len(changes) == 0 {
		rep.Status = "unchanged"
		return
	}
	if !s.loads(rep, changes, func(changes map[string][]byte) error {
		_, err := s.Repos.GraphWith(ctx, snap, changes)
		return err
	}) {
		return
	}
	if s.Writer == nil || s.Writer.Signer == nil {
		rep.Status, rep.Error = "refused", "Gaia has no signing key to write the change (gaia server --signing-key)"
		return
	}
	msg := fmt.Sprintf("%s: %d added, %d changed, %d removed\n\n%s: %s\n", rep.Source, len(rep.Added), len(rep.Changed), len(rep.Removed), repo.TrailerSource, rep.Source)
	h, err := s.Writer.Commit(ctx, plat.Repo(), plat.Commit, msg, changes)
	if err != nil {
		rep.Status, rep.Error = "failed", err.Error()
		return
	}
	rep.Status, rep.Commit = "committed", h
}

// store writes a source's settings to the settings store: the objects it
// generated before are compared with the ones it renders now, and a new
// version holds the difference (design 17.6). It does not wait for an
// approval: the directory or the IPAM changed, and the contracts the
// version changes are approved as usual.
func (s *Syncer) store(ctx context.Context, snap repo.Snapshot, rep *Report, kind string, rendered map[string][]byte) {
	if s.Settings == nil {
		rep.Status, rep.Error = "refused", "this Gaia has no settings store to write to"
		return
	}
	base, err := s.Settings.Files(ctx, snap.Settings)
	if err != nil {
		rep.Status, rep.Error = "failed", err.Error()
		return
	}
	have, mine := map[string][]byte{}, map[string][]byte{}
	for _, f := range base {
		have[f.Path] = f.Data
		if catalog.SettingsFileKind(f.Path) == kind && !strings.HasSuffix(f.Path, ".patch.yaml") && generatedBy(f.Data) == rep.Source {
			mine[f.Path] = f.Data
		}
	}
	want := map[string][]byte{}
	for _, data := range rendered {
		var head struct {
			Metadata struct{ Name string } `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(data, &head); err != nil {
			rep.Status, rep.Error = "failed", err.Error()
			return
		}
		p := catalog.SettingsPath(kind, head.Metadata.Name, false)
		if _, taken := have[p]; taken && mine[p] == nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s/%s is written by hand in the settings; left out", kind, head.Metadata.Name))
			continue
		}
		want[p] = data
	}
	changes := diff(rep, mine, want)
	if len(changes) == 0 {
		rep.Status = "unchanged"
		return
	}
	if !s.loads(rep, changes, func(changes map[string][]byte) error {
		files, _ := settings.Apply(base, changes)
		_, err := s.Repos.GraphWithSettings(ctx, snap, files)
		return err
	}) {
		return
	}
	d, err := s.Settings.Write(ctx, settings.Write{
		Base: snap.Settings, Changes: changes, Author: "source/" + strings.SplitN(rep.Source, "/", 2)[1],
		Reason: fmt.Sprintf("%s: %d added, %d changed, %d removed", rep.Source, len(rep.Added), len(rep.Changed), len(rep.Removed)),
		Audit:  audit.Entry{Action: "settings.source", Detail: rep.Source},
	})
	switch {
	case errors.Is(err, settings.ErrStale):
		rep.Status, rep.Error = "refused", "the settings changed meanwhile; the next sync tries again"
	case err != nil:
		rep.Status, rep.Error = "failed", err.Error()
	default:
		rep.Status, rep.Version = "stored", d.Version
	}
}

// generatedBy is the Source a rendered object names as its writer.
func generatedBy(data []byte) string {
	var head struct {
		Metadata struct {
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	_ = yaml.Unmarshal(data, &head)
	return head.Metadata.Annotations[catalog.AnnotationGeneratedBy]
}

// diff is what turns have into want, by path (nil removes), noted in
// the report.
func diff(rep *Report, have, want map[string][]byte) map[string][]byte {
	changes := map[string][]byte{}
	for p, data := range want {
		old, ok := have[p]
		switch {
		case !ok:
			rep.Added = append(rep.Added, p)
			changes[p] = data
		case !bytes.Equal(old, data):
			rep.Changed = append(rep.Changed, p)
			changes[p] = data
		}
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			rep.Removed = append(rep.Removed, p)
			changes[p] = nil
		}
	}
	sort.Strings(rep.Added)
	sort.Strings(rep.Changed)
	sort.Strings(rep.Removed)
	return changes
}

// loads leaves out the objects that do not load, one by one, and reports
// whether the rest does; what cannot be put on one object of the source
// refuses them all.
func (s *Syncer) loads(rep *Report, changes map[string][]byte, try func(map[string][]byte) error) bool {
	for {
		err := try(changes)
		if err == nil {
			return true
		}
		var errs catalog.Errors
		dropped := false
		if errors.As(err, &errs) {
			for _, e := range errs {
				if data, ok := changes[e.File]; ok && data != nil {
					delete(changes, e.File)
					rep.Problems = append(rep.Problems, e.Error()+" (left out)")
					rep.Added = slices.DeleteFunc(rep.Added, func(p string) bool { return p == e.File })
					rep.Changed = slices.DeleteFunc(rep.Changed, func(p string) bool { return p == e.File })
					dropped = true
				}
			}
		}
		if !dropped {
			rep.Status, rep.Error = "refused", "the catalog would not load: "+err.Error()
			return false
		}
		if len(changes) == 0 {
			rep.Status = "unchanged"
			return false
		}
	}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func file(output, name string) string { return path.Join(strings.Trim(output, "/"), name+".yaml") }

package repo

import (
	"context"
	"fmt"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
)

// Rev is a repository at one commit, and the directories read from it.
type Rev struct {
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Ref         string   `json:"ref,omitempty"`
	Credentials string   `json:"credentials,omitempty"`
	Commit      string   `json:"commit"`
	Paths       []string `json:"paths,omitempty"`
}

// Repo is the repository of the revision.
func (r Rev) Repo() Repo {
	return Repo{Name: r.Name, URL: r.URL, Ref: r.Ref, Credentials: r.Credentials}
}

// Snapshot is a desired state: the platform repository and the contract
// repositories, each at one commit. Config pins the config repository's
// commit for the run; it is not part of the desired state's identity,
// since role code is applied at the next run (design 7.3).
type Snapshot struct {
	Platform  Rev   `json:"platform"`
	Contracts []Rev `json:"contracts,omitempty"`
	Config    *Rev  `json:"config,omitempty"`
	// Settings is the settings version (design 17.2); 0 reads the
	// settings from Git, as snapshots taken before the store did.
	Settings int64 `json:"settings,omitempty"`
}

// SettingsReader reads the settings store: its latest version, and a
// version's files (catalog.SettingsPath). A version never changes.
type SettingsReader interface {
	Latest(ctx context.Context) (int64, error)
	Files(ctx context.Context, version int64) ([]catalog.File, error)
}

// Label identifies the desired state in plans and runs: the platform
// commit, each contract repository's commit when it is another one, and
// the settings version.
func (s Snapshot) Label() string {
	parts := []string{s.Platform.Commit}
	for _, c := range s.Contracts {
		if c.Commit != s.Platform.Commit {
			parts = append(parts, c.Name+"@"+c.Commit)
		}
	}
	if s.Settings > 0 {
		parts = append(parts, fmt.Sprintf("settings@%d", s.Settings))
	}
	return strings.Join(parts, "+")
}

// settingsFiles is a version's files.
func (m *Mirrors) settingsFiles(ctx context.Context, version int64) ([]catalog.File, error) {
	if m.Settings == nil {
		return nil, fmt.Errorf("settings version %d: this Gaia has no settings store", version)
	}
	return m.Settings.Files(ctx, version)
}

// files reads the catalog files of a revision. Files of a contract
// repository are named after it, so that messages say where they are.
func (m *Mirrors) files(ctx context.Context, r Rev, prefix bool) ([]catalog.File, error) {
	if err := m.Ensure(ctx, r.Repo(), r.Commit); err != nil {
		return nil, err
	}
	files, _, err := gitsrc.Load(m.Path(r.Repo()), r.Commit, r.Paths)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Name, err)
	}
	if prefix {
		for i := range files {
			files[i].Path = r.Name + ":" + files[i].Path
		}
	}
	return files, nil
}

// Graph loads and resolves the objects of a snapshot.
func (m *Mirrors) Graph(ctx context.Context, s Snapshot) (*catalog.Graph, error) {
	return m.GraphWith(ctx, s, nil)
}

// GraphWith is Graph with some files of the platform repository replaced
// (nil removes one): the catalog a commit Gaia is about to write would
// give, so that Gaia never writes one that does not load.
func (m *Mirrors) GraphWith(ctx context.Context, s Snapshot, overlay map[string][]byte) (*catalog.Graph, error) {
	var settings []catalog.File
	if s.Settings > 0 {
		var err error
		if settings, err = m.settingsFiles(ctx, s.Settings); err != nil {
			return nil, err
		}
	}
	return m.graph(ctx, s, overlay, settings)
}

// GraphWithSettings is Graph with the settings store's files replaced by
// these: the desired state a settings version would give, so that no
// version is written that does not load.
func (m *Mirrors) GraphWithSettings(ctx context.Context, s Snapshot, settings []catalog.File) (*catalog.Graph, error) {
	if settings == nil {
		settings = []catalog.File{}
	}
	return m.graph(ctx, s, nil, settings)
}

// graph loads a snapshot: Git's files, with the overlay, and the settings
// store's when there are any (nil reads the settings from Git).
func (m *Mirrors) graph(ctx context.Context, s Snapshot, overlay map[string][]byte, settings []catalog.File) (*catalog.Graph, error) {
	files, err := m.files(ctx, s.Platform, false)
	if err != nil {
		return nil, err
	}
	if overlay != nil {
		kept := files[:0]
		for _, f := range files {
			if _, ok := overlay[f.Path]; !ok {
				kept = append(kept, f)
			}
		}
		files = kept
		for p, data := range overlay {
			if data != nil && inPaths(p, s.Platform.Paths) {
				files = append(files, catalog.File{Path: p, Data: data})
			}
		}
	}
	for _, c := range s.Contracts {
		fs, err := m.files(ctx, c, true)
		if err != nil {
			return nil, err
		}
		files = append(files, fs...)
	}
	load := catalog.Load
	if settings != nil {
		files = append(files, settings...)
		load = catalog.LoadSplit
	}
	cat, err := load(s.Label(), files)
	if err != nil {
		return nil, err
	}
	return catalog.Resolve(cat)
}

func inPaths(p string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, d := range paths {
		d = strings.Trim(d, "/")
		if d == "" || d == "." || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// Head syncs every repository a platform repository leads to and returns
// the snapshot at their heads: the platform repository first, then the
// contract and config repositories its Platform names.
func (m *Mirrors) Head(ctx context.Context, platform Rev) (Snapshot, error) {
	commit, err := m.Sync(ctx, platform.Repo())
	if err != nil {
		return Snapshot{}, err
	}
	platform.Commit = commit
	return m.At(ctx, platform)
}

// At returns the snapshot of a platform repository at its commit, with
// the latest settings version and the contract and config repositories
// its Platform names at their heads.
func (m *Mirrors) At(ctx context.Context, platform Rev) (Snapshot, error) {
	s := Snapshot{Platform: platform}
	if m.Settings != nil {
		v, err := m.Settings.Latest(ctx)
		if err != nil {
			return s, err
		}
		s.Settings = v
	}
	files, err := m.files(ctx, platform, false)
	if err != nil {
		return s, err
	}
	// The Platform alone says where the rest is; the catalog is resolved
	// once everything is read. With a settings version, the Platform and
	// the providers are settings.
	load := catalog.Load
	if s.Settings > 0 {
		settings, err := m.settingsFiles(ctx, s.Settings)
		if err != nil {
			return s, err
		}
		files = append(files, settings...)
		load = catalog.LoadSplit
	}
	cat, err := load(platform.Commit, files)
	if err != nil {
		return s, err
	}
	plats := cat.List(catalog.KindPlatform)
	if len(plats) != 1 {
		where := platform.Name
		if s.Settings > 0 {
			where = fmt.Sprintf("settings@%d", s.Settings)
		}
		return s, fmt.Errorf("%s: want one Platform, found %d", where, len(plats))
	}
	ps := plats[0].Spec.(*catalog.PlatformSpec)
	provider := func(name string) (*catalog.ProviderSpec, error) {
		o, ok := cat.Get(catalog.KindProvider, name)
		if !ok {
			return nil, fmt.Errorf("no Provider %q", name)
		}
		return o.Spec.(*catalog.ProviderSpec), nil
	}
	for _, c := range ps.Contracts {
		p, err := provider(c.Provider)
		if err != nil {
			return s, err
		}
		rev := Rev{Name: c.Provider, URL: p.URL, Ref: p.Ref, Credentials: p.Credentials, Paths: c.Paths}
		if rev.Commit, err = m.Sync(ctx, rev.Repo()); err != nil {
			return s, err
		}
		s.Contracts = append(s.Contracts, rev)
	}
	if src := ps.Engines.Configuration.Source; src != "" {
		p, err := provider(src)
		if err != nil {
			return s, err
		}
		rev := Rev{Name: src, URL: p.URL, Ref: p.Ref, Credentials: p.Credentials}
		if p.Path != "" {
			rev.Paths = []string{p.Path}
		}
		if rev.Commit, err = m.Sync(ctx, rev.Repo()); err != nil {
			return s, err
		}
		s.Config = &rev
	}
	return s, nil
}

// PlatformFiles is the platform repository's catalog files at the
// snapshot, as Git has them.
func (m *Mirrors) PlatformFiles(ctx context.Context, s Snapshot) ([]catalog.File, error) {
	return m.files(ctx, s.Platform, false)
}

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
)

// source is where commands read objects from: directories of one commit.
type source struct {
	repo, ref string
	paths     []string
	settings  string
}

func (s *source) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.repo, "repo", ".", "repository (any directory inside it)")
	cmd.Flags().StringVar(&s.ref, "ref", "HEAD", "commit, branch or tag to read")
	cmd.Flags().StringSliceVar(&s.paths, "path", nil, "directory inside the repository; repeatable (default: the whole tree)")
	cmd.Flags().StringVar(&s.settings, "settings", "", "Gaia's settings, as gaia settings export wrote them; Git's copies of the settings kinds are then left out")
}

// settingsFiles reads an exported settings directory as the settings
// store's files.
func settingsFiles(dir string) ([]catalog.File, error) {
	var out []catalog.File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(p) != ".yaml" && filepath.Ext(p) != ".yml") {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // the operator's directory
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, catalog.File{Path: catalog.SettingsPrefix + filepath.ToSlash(rel), Data: data})
		return nil
	})
	return out, err
}

// graph loads and resolves the objects. It prints which commit was read
// and, on problems, each of them; the error then carries their count.
func (s *source) graph(out io.Writer) (*catalog.Graph, error) {
	return s.graphWith(out, nil)
}

// graphWith is graph with the settings store's files, when Gaia keeps
// its settings in the database.
func (s *source) graphWith(out io.Writer, stored []catalog.File) (*catalog.Graph, error) {
	all, commit, err := gitsrc.Load(s.repo, s.ref, s.paths)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(out, "commit %s (%s), %d files\n", commit[:12], s.ref, len(all)); err != nil {
		return nil, err
	}
	load := catalog.Load
	if stored != nil {
		if _, err := fmt.Fprintf(out, "settings from the database, %d files\n", len(stored)); err != nil {
			return nil, err
		}
		all, load = append(all, stored...), catalog.LoadSplit
	} else if s.settings != "" {
		files, err := settingsFiles(s.settings)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(out, "settings %s, %d files\n", s.settings, len(files)); err != nil {
			return nil, err
		}
		all, load = append(all, files...), catalog.LoadSplit
	}
	cat, err := load(commit, all)
	if err != nil {
		return nil, report(out, err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		return nil, report(out, err)
	}
	return g, nil
}

func newValidateCommand() *cobra.Command {
	var src source
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Load and check catalog and contract files at a commit",
		Long: "validate reads every .yaml/.yml file under --path at --ref of the repository and\n" +
			"checks it as Gaia objects. Uncommitted changes are not read.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			g, err := src.graph(out)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "%d objects (%s), %d references\n", g.Catalog.Len(), summary(g.Catalog), refCount(g))
			return err
		},
	}
	src.flags(cmd)
	return cmd
}

// report prints each problem and returns an error with their count.
func report(out io.Writer, err error) error {
	var errs catalog.Errors
	if !errors.As(err, &errs) {
		return err
	}
	for _, e := range errs {
		if _, werr := fmt.Fprintln(out, e.Error()); werr != nil {
			return werr
		}
	}
	return fmt.Errorf("%d problem(s) found", len(errs))
}

func refCount(g *catalog.Graph) int {
	n := 0
	for _, k := range catalog.Kinds() {
		for _, o := range g.Catalog.List(k) {
			n += len(g.Refs(o.Key()))
		}
	}
	return n
}

func summary(cat *catalog.Catalog) string {
	var parts []string
	for _, k := range catalog.Kinds() {
		if n := len(cat.List(k)); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return strings.Join(parts, ", ")
}

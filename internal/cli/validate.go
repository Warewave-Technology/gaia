package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
)

// source is where commands read objects from: directories of one commit.
type source struct {
	repo, ref string
	paths     []string
}

func (s *source) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.repo, "repo", ".", "repository (any directory inside it)")
	cmd.Flags().StringVar(&s.ref, "ref", "HEAD", "commit, branch or tag to read")
	cmd.Flags().StringSliceVar(&s.paths, "path", nil, "directory inside the repository; repeatable (default: the whole tree)")
}

// graph loads and resolves the objects. It prints which commit was read
// and, on problems, each of them; the error then carries their count.
func (s *source) graph(out io.Writer) (*catalog.Graph, error) {
	all, commit, err := gitsrc.Load(s.repo, s.ref, s.paths)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(out, "commit %s (%s), %d files\n", commit[:12], s.ref, len(all)); err != nil {
		return nil, err
	}
	cat, err := catalog.Load(commit, all)
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

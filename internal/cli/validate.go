package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
)

func newValidateCommand() *cobra.Command {
	var repo, ref string
	var paths []string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Load and check catalog and contract files at a commit",
		Long: "validate reads every .yaml/.yml file under --path at --ref of the repository and\n" +
			"checks it as Gaia objects. Uncommitted changes are not read.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(paths) == 0 {
				paths = []string{""}
			}
			var all []catalog.File
			var commit string
			for _, p := range paths {
				files, c, err := gitsrc.Files(repo, ref, p)
				if err != nil {
					return err
				}
				all = append(all, files...)
				commit = c
			}

			out := cmd.OutOrStdout()
			cat, err := catalog.Load(commit, all)
			if _, werr := fmt.Fprintf(out, "commit %s (%s), %d files\n", commit[:12], ref, len(all)); werr != nil {
				return werr
			}
			var errs catalog.Errors
			if errors.As(err, &errs) {
				for _, e := range errs {
					if _, werr := fmt.Fprintln(out, e.Error()); werr != nil {
						return werr
					}
				}
				return fmt.Errorf("%d problem(s) found", len(errs))
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "%d objects: %s\n", cat.Len(), summary(cat))
			return err
		},
	}
	cmd.Flags().StringVar(&repo, "repo", ".", "repository (any directory inside it)")
	cmd.Flags().StringVar(&ref, "ref", "HEAD", "commit, branch or tag to read")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "directory inside the repository; repeatable (default: the whole tree)")
	return cmd
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

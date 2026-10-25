package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/settings"
)

// newSettingsCommand reads and loads Gaia's settings store (design 17.2)
// straight in the database: export for backups and for copying an
// environment, import for the first settings of a new installation.
func newSettingsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Export, import and list Gaia's settings (database)",
		Long: "settings works on Gaia's settings store directly. export writes a version as YAML\n" +
			"files, one per object; import loads such files as the first version of a new\n" +
			"installation. Later changes go through Gaia, where they are approved.",
	}
	var dsn string

	var version int64
	export := &cobra.Command{
		Use: "export DIR", Short: "Write a settings version as YAML files, one per object", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(cmd, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			st := &settings.Store{DB: s}
			v := version
			if v == 0 {
				if v, err = st.Latest(cmd.Context()); err != nil {
					return err
				}
				if v == 0 {
					return errors.New("there are no settings yet")
				}
			}
			files, err := st.Files(cmd.Context(), v)
			if err != nil {
				return err
			}
			for _, f := range files {
				p := filepath.Join(args[0], filepath.FromSlash(strings.TrimPrefix(f.Path, catalog.SettingsPrefix)))
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					return err
				}
				if err := os.WriteFile(p, f.Data, 0o640); err != nil { //nolint:gosec // the operator's directory
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "settings@%d: %d objects written to %s\n", v, len(files), args[0])
			return err
		},
	}
	export.Flags().Int64Var(&version, "version", 0, "the version to export (default the latest)")

	var reason string
	imp := &cobra.Command{
		Use: "import DIR", Short: "Load YAML files as the first settings of a new installation", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var files []catalog.File
			err := filepath.WalkDir(args[0], func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" {
					return nil
				}
				data, err := os.ReadFile(p) //nolint:gosec // the operator's directory
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(args[0], p)
				files = append(files, catalog.File{Path: filepath.ToSlash(rel), Data: data})
				return nil
			})
			if err != nil {
				return err
			}
			changes, err := settings.Split(files)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				return fmt.Errorf("%s: no objects of the settings kinds (%s)", args[0], strings.Join(catalog.SettingKinds(), ", "))
			}
			var as []catalog.File
			for p, data := range changes {
				as = append(as, catalog.File{Path: p, Data: data})
			}
			// References are resolved once Gaia loads them with Git; here
			// each object must stand on its own.
			if _, err := catalog.LoadSplit("import", as); err != nil {
				return err
			}
			s, err := openStore(cmd, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			st := &settings.Store{DB: s}
			latest, err := st.Latest(cmd.Context())
			if err != nil {
				return err
			}
			if latest > 0 {
				return fmt.Errorf("there are settings already (version %d); change them through Gaia, where changes are approved", latest)
			}
			who := "cli"
			if u, err := user.Current(); err == nil {
				who = "cli:" + u.Username
			}
			if reason == "" {
				reason = "gaia settings import " + args[0]
			}
			d, err := st.Write(cmd.Context(), settings.Write{Changes: changes, Author: who, Reason: reason, Audit: audit.Entry{Action: "settings.import"}})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "settings@%d: %d objects\n", d.Version, len(d.Added))
			return err
		},
	}
	imp.Flags().StringVar(&reason, "reason", "", "why, for the audit log")

	versions := &cobra.Command{
		Use: "versions", Short: "List the settings versions", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore(cmd, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			rows, err := s.Q().SettingsVersions(cmd.Context(), 50)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "VERSION\tWHEN\tAUTHOR\tOBJECTS\tREASON")
			for _, v := range rows {
				_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", v.Version, v.CreatedAt.Time.Format("2006-01-02 15:04"), v.Author, v.Objects, v.Reason)
			}
			return tw.Flush()
		},
	}

	for _, c := range []*cobra.Command{export, imp, versions} {
		dsnFlag(c, &dsn)
		cmd.AddCommand(c)
	}
	return cmd
}

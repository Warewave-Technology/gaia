package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/settings"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// newUserCommand manages accounts straight in the database: the first
// platform admin, and break-glass accounts when sign-in itself is broken.
func newUserCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage accounts in the database (bootstrap and break-glass)",
		Long: "user works on Gaia's database directly, with the catalog's AuthConfig for the\n" +
			"enrollment link. Use it for the first platform admin and when sign-in is broken;\n" +
			"otherwise platform admins manage accounts through the API.",
	}
	var src source
	var dsn, display string
	var teams []string
	var breakGlass bool

	open := func(cmd *cobra.Command) (*store.Store, *auth.Service, error) {
		d, err := resolveDSN(dsn)
		if err != nil {
			return nil, nil, err
		}
		s, err := store.Open(cmd.Context(), d)
		if err != nil {
			return nil, nil, err
		}
		// AuthConfig is a setting: the database's when Gaia keeps them there.
		var stored []catalog.File
		st := &settings.Store{DB: s}
		if v, err := st.Latest(cmd.Context()); err == nil && v > 0 {
			if stored, err = st.Files(cmd.Context(), v); err != nil {
				s.Close()
				return nil, nil, err
			}
		}
		g, err := src.graphWith(cmd.ErrOrStderr(), stored)
		if err != nil {
			s.Close()
			return nil, nil, err
		}
		if g.AuthConfig() == nil {
			s.Close()
			return nil, nil, fmt.Errorf("the catalog has no AuthConfig")
		}
		return s, auth.New(s, func() *catalog.Graph { return g }, nil), nil
	}

	add := &cobra.Command{
		Use: "add USERNAME", Short: "Create a local account and print its enrollment link", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, a, err := open(cmd)
			if err != nil {
				return err
			}
			defer s.Close()
			link, err := a.CreateLocalUser(cmd.Context(), args[0], display, teams, breakGlass)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "created %s; send this link to them (valid %s, once):\n%s\n", args[0], auth.EnrollTTL, link)
			return err
		},
	}
	add.Flags().StringVar(&display, "display-name", "", "full name")
	add.Flags().StringSliceVar(&teams, "team", nil, "team; repeatable (roles come from Policy.gaiaRoles)")
	add.Flags().BoolVar(&breakGlass, "break-glass", false, "a break-glass platform admin, when people sign in with the directory")

	enroll := &cobra.Command{
		Use: "enroll USERNAME", Short: "Print a new enrollment link for a local account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, a, err := open(cmd)
			if err != nil {
				return err
			}
			defer s.Close()
			u, err := s.Q().UserByName(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			link, err := a.Enroll(cmd.Context(), u.ID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), link)
			return err
		},
	}

	list := &cobra.Command{
		Use: "list", Short: "List accounts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			s, err := store.Open(cmd.Context(), d)
			if err != nil {
				return err
			}
			defer s.Close()
			users, err := s.Q().Users(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "USERNAME\tPROVIDER\tENABLED\tTEAMS")
			for _, u := range users {
				ts, err := s.Q().Teams(cmd.Context(), u.ID)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", u.Username, u.Provider, u.Enabled, strings.Join(ts, ","))
			}
			return tw.Flush()
		},
	}

	disable := &cobra.Command{
		Use: "disable USERNAME", Short: "Disable an account and end its sessions", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			s, err := store.Open(cmd.Context(), d)
			if err != nil {
				return err
			}
			defer s.Close()
			ctx := cmd.Context()
			u, err := s.Q().UserByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			err = s.Tx(ctx, func(q *db.Queries) error {
				if err := q.SetUserEnabled(ctx, db.SetUserEnabledParams{ID: u.ID, Enabled: false}); err != nil {
					return err
				}
				return q.RevokeUserSessions(ctx, u.ID)
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "disabled %s\n", args[0])
			return err
		},
	}

	for _, c := range []*cobra.Command{add, enroll} {
		src.flags(c)
	}
	for _, c := range []*cobra.Command{add, enroll, list, disable} {
		dsnFlag(c, &dsn)
		cmd.AddCommand(c)
	}
	return cmd
}

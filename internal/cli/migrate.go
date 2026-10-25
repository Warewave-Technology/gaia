package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// dsnFlag adds --dsn, defaulting to $GAIA_DSN.
func dsnFlag(cmd *cobra.Command, dsn *string) {
	cmd.Flags().StringVar(dsn, "dsn", "", "PostgreSQL DSN (default $GAIA_DSN, or the file $GAIA_DSN_FILE)")
}

func resolveDSN(dsn string) (string, error) {
	if dsn == "" {
		dsn = os.Getenv("GAIA_DSN")
	}
	if f := os.Getenv("GAIA_DSN_FILE"); dsn == "" && f != "" {
		// A file an OpenBao agent or CSI driver writes (design 13.3).
		b, err := os.ReadFile(f) //nolint:gosec // the operator's path
		if err != nil {
			return "", err
		}
		dsn = strings.TrimSpace(string(b))
	}
	if dsn == "" {
		return "", errors.New("no database: set --dsn, GAIA_DSN or GAIA_DSN_FILE")
	}
	return dsn, nil
}

func newMigrateCommand() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Bring Gaia's database schema up to date",
		Long:  "migrate applies Gaia's schema migrations. Run it with the role that owns the\nschema (gaia_owner).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			n, err := store.MigrateCount(cmd.Context(), d)
			if err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			if n > 0 {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%d migration(s) applied; schema up to date\n", n)
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "schema up to date")
			return err
		},
	}
	dsnFlag(cmd, &dsn)
	return cmd
}

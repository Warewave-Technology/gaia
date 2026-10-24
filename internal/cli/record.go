package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newRecordCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Read and verify run records",
	}
	var dsn string
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check the hash chain of every run record",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore(cmd, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			n, err := runrecord.Verify(cmd.Context(), s.Q())
			if err != nil {
				return fmt.Errorf("chain broken after %d records: %w", n, err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%d records, chain intact\n", n)
			return err
		},
	}
	show := &cobra.Command{
		Use:   "show RUN",
		Short: "Print a run's record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(cmd, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			r, err := s.Q().RunRecord(cmd.Context(), args[0])
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%s: no record", args[0])
			}
			if err != nil {
				return err
			}
			var body bytes.Buffer
			if err := json.Indent(&body, []byte(r.Body), "", "  "); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, err = fmt.Fprintf(out, "record %d %s\nprev   %s\narchive %s\n%s\n", r.Seq, r.RecordHash, orNone(r.PrevHash), orNone(r.Archived.String), body.String())
			return err
		},
	}
	for _, c := range []*cobra.Command{verify, show} {
		dsnFlag(c, &dsn)
		cmd.AddCommand(c)
	}
	return cmd
}

func openStore(cmd *cobra.Command, dsn string) (*store.Store, error) {
	d, err := resolveDSN(dsn)
	if err != nil {
		return nil, err
	}
	return store.Open(cmd.Context(), d)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/ipam"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
)

// tokenFlag adds --token-file, defaulting to $GAIA_IPAM_TOKEN.
func tokenFlag(cmd *cobra.Command, file *string) {
	cmd.Flags().StringVar(file, "token-file", "", "file holding the API token (default $GAIA_IPAM_TOKEN)")
}

func readToken(file string) (string, error) {
	if file == "" {
		if t := os.Getenv("GAIA_IPAM_TOKEN"); t != "" {
			return t, nil
		}
		return "", errors.New("no API token: set --token-file or GAIA_IPAM_TOKEN")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func newIPAMServeCommand() *cobra.Command {
	var dsn, listen, tokenFile string
	var quarantine time.Duration
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the IPAM API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			token, err := readToken(tokenFile)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			srv, err := ipam.NewServer(ctx, d, token, quarantine)
			if err != nil {
				return err
			}
			defer srv.Close()

			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "gaia-ipam listening on %s\n", ln.Addr()); err != nil {
				return err
			}
			errc := make(chan error, 1)
			go func() { errc <- hs.Serve(ln) }()
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return hs.Shutdown(shutdown)
			}
		},
	}
	dsnFlag(cmd, &dsn)
	tokenFlag(cmd, &tokenFile)
	cmd.Flags().StringVar(&listen, "listen", ":8090", "address to listen on")
	cmd.Flags().DurationVar(&quarantine, "quarantine", 14*24*time.Hour, "how long a released address is held back")
	return cmd
}

func newIPAMMigrateCommand() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Bring the IPAM database schema up to date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			if err := ipam.Migrate(cmd.Context(), d); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "schema up to date")
			return err
		},
	}
	dsnFlag(cmd, &dsn)
	return cmd
}

func newIPAMPoolCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "pool", Short: "Manage pools"}
	var endpoint, tokenFile, gateway, rng, name string
	var labels, tags []string
	set := &cobra.Command{
		Use:   "set CIDR",
		Short: "Create or update a pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := readToken(tokenFile)
			if err != nil {
				return err
			}
			start, end, ok := strings.Cut(rng, "-")
			if !ok {
				return fmt.Errorf("--range %q: want start-end", rng)
			}
			p := ipam.Pool{CIDR: args[0], Gateway: gateway, RangeStart: start, RangeEnd: end, Name: name, Tags: tags, Labels: map[string]string{}}
			for _, l := range labels {
				k, v, ok := strings.Cut(l, "=")
				if !ok || k == "" {
					return fmt.Errorf("--label %q: want key=value", l)
				}
				p.Labels[k] = v
			}
			if err := ipamclient.New(endpoint, token).SetPool(cmd.Context(), p); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "pool %s: %s-%s\n", p.CIDR, start, end)
			return err
		},
	}
	set.Flags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:8090", "IPAM API")
	tokenFlag(set, &tokenFile)
	set.Flags().StringVar(&gateway, "gateway", "", "gateway address (never allocated)")
	set.Flags().StringVar(&rng, "range", "", "addresses to allocate from, start-end")
	set.Flags().StringVar(&name, "name", "", "what the pool is called")
	set.Flags().StringSliceVar(&labels, "label", nil, "key=value, such as site=ank or vlan=120; repeatable")
	set.Flags().StringSliceVar(&tags, "tag", nil, "a tag such as gaia-managed; repeatable")
	_ = set.MarkFlagRequired("range")
	cmd.AddCommand(set)
	return cmd
}

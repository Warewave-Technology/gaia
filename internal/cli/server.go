package cli

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newServerCommand() *cobra.Command {
	var dsn, listen, role string
	var workers int
	var runTimeout, stopTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the API and the workers that apply queued runs",
		Long: "server runs the API and the workers in one process; --role splits them\n" +
			"(design 13.1). Workers reach OpenBao with $GAIA_BAO_TOKEN and run Pulumi\n" +
			"and ansible-runner (podman) like gaia apply.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			cfg := server.Config{RunTimeout: runTimeout, StopTimeout: stopTimeout,
				Log: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))}
			switch role {
			case "all":
				cfg.Listen, cfg.Workers = listen, workers
			case "api":
				cfg.Listen = listen
			case "worker":
				cfg.Workers = workers
			default:
				return errors.New("--role: all, api or worker")
			}
			s, err := store.Open(ctx, d)
			if err != nil {
				return err
			}
			defer s.Close()
			if cfg.Workers > 0 {
				token := os.Getenv("GAIA_BAO_TOKEN")
				if token == "" {
					return errors.New("workers need OpenBao: set GAIA_BAO_TOKEN")
				}
				cfg.Systems = func(g *catalog.Graph) (apply.Deps, error) {
					return apply.Systems(s, openbao.New(secretsEndpoint(g), token), io.Discard), nil
				}
			}
			srv, err := server.New(cfg, s)
			if err != nil {
				return err
			}
			return srv.Run(ctx)
		},
	}
	dsnFlag(cmd, &dsn)
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "API address")
	cmd.Flags().StringVar(&role, "role", "all", "all, api or worker")
	cmd.Flags().IntVar(&workers, "workers", 2, "runs at once on this server")
	cmd.Flags().DurationVar(&runTimeout, "run-timeout", 2*time.Hour, "longest a run may take")
	cmd.Flags().DurationVar(&stopTimeout, "stop-timeout", 5*time.Minute, "on shutdown, how long runs get to finish before they are cancelled")
	return cmd
}

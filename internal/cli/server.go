package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newServerCommand() *cobra.Command {
	var dsn, listen, role string
	var workers int
	var unsafeNoAuth bool
	var repo, ref string
	var paths []string
	var runTimeout, stopTimeout, eventRetention time.Duration
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the API and the workers that apply queued runs",
		Long: "server runs the API and the workers in one process; --role splits them\n" +
			"(design 13.1). Workers run Pulumi and ansible-runner (podman) like gaia apply.\n\n" +
			"Workers log in to OpenBao with AppRole: $GAIA_BAO_ADDR, $GAIA_BAO_ROLE_ID and\n" +
			"the secret_id from $GAIA_BAO_SECRET_ID_FILE, the systemd credential bao-secret-id\n" +
			"or $GAIA_BAO_SECRET_ID. $GAIA_BAO_TOKEN is used instead when set (development).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			cfg := server.Config{RunTimeout: runTimeout, StopTimeout: stopTimeout, EventRetention: eventRetention,
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
			var cat *server.CatalogSource
			if repo != "" {
				abs, err := filepath.Abs(repo)
				if err != nil {
					return err
				}
				cat = &server.CatalogSource{Src: queue.Source{Repo: abs, Commit: ref, Paths: paths}, Log: cfg.Log}
				if err := cat.Load(); err != nil {
					return fmt.Errorf("catalog: %w", err)
				}
				go cat.Run(ctx, time.Minute)
				if cat.Graph().AuthConfig() != nil {
					cfg.Auth = auth.New(s, cat.Graph, cfg.Log)
				}
			}
			if cfg.Listen != "" && cfg.Auth == nil && !unsafeNoAuth {
				if err := loopbackOnly(cfg.Listen); err != nil {
					return err
				}
			}
			mfa := cfg.Auth != nil && cat.Graph().AuthConfig().Require == "mfa"
			if cfg.Workers > 0 || mfa {
				bao, err := baoLogin(ctx, cfg.Log)
				if err != nil {
					return err
				}
				if cfg.Workers > 0 {
					cfg.Systems = func(g *catalog.Graph) (apply.Deps, error) {
						return apply.Systems(s, bao(secretsEndpoint(g)), io.Discard), nil
					}
				}
				if mfa {
					// TOTP seeds are encrypted with the transit key gaia-auth.
					cfg.Auth.WithTransit(bao(secretsEndpoint(cat.Graph())))
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
	cmd.Flags().DurationVar(&eventRetention, "event-retention", 90*24*time.Hour, "how long run events stay in the database; the run log archive keeps them")
	cmd.Flags().BoolVar(&unsafeNoAuth, "unsafe-no-auth", false, "listen on a non-loopback address without an AuthConfig")
	cmd.Flags().StringVar(&repo, "repo", "", "repository with the catalog (AuthConfig, Policy, contracts); without it the API is open, on loopback only")
	cmd.Flags().StringVar(&ref, "ref", "HEAD", "ref of --repo to read, reloaded every minute")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "directory inside --repo; repeatable")
	cmd.Flags().DurationVar(&stopTimeout, "stop-timeout", 5*time.Minute, "on shutdown, how long runs get to finish before they are cancelled")
	return cmd
}

// loopbackOnly refuses API addresses other machines can reach: the API
// has no authentication until roadmap phase 2.
func loopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--listen %s: the API has no authentication yet; listen on a loopback address or pass --unsafe-no-auth", addr)
}

// baoLogin returns how workers reach OpenBao: an AppRole session kept
// alive for as long as ctx, or a fixed token for development.
func baoLogin(ctx context.Context, log *slog.Logger) (func(addr string) *openbao.Client, error) {
	if token := os.Getenv("GAIA_BAO_TOKEN"); token != "" {
		log.Warn("using $GAIA_BAO_TOKEN; use AppRole outside development")
		return func(addr string) *openbao.Client { return openbao.New(addr, token) }, nil
	}
	addr, roleID := os.Getenv("GAIA_BAO_ADDR"), os.Getenv("GAIA_BAO_ROLE_ID")
	if addr == "" || roleID == "" {
		return nil, errors.New("workers need OpenBao: set GAIA_BAO_ADDR and GAIA_BAO_ROLE_ID with a secret_id, or GAIA_BAO_TOKEN")
	}
	secretID, err := baoSecretID()
	if err != nil {
		return nil, err
	}
	sess, err := openbao.Login(ctx, addr, roleID, secretID, log)
	if err != nil {
		return nil, err
	}
	go sess.Keep(ctx)
	log.Info("openbao: logged in with approle", "addr", addr)
	return func(addr string) *openbao.Client { return sess.Client(addr) }, nil
}

// baoSecretID reads the AppRole secret_id: a file, a systemd credential or
// the environment, in that order.
func baoSecretID() (string, error) {
	path := os.Getenv("GAIA_BAO_SECRET_ID_FILE")
	if path == "" {
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			path = filepath.Join(dir, "bao-secret-id")
		}
	}
	if path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // the operator's path
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := os.Getenv("GAIA_BAO_SECRET_ID"); v != "" {
		return v, nil
	}
	return "", errors.New("no AppRole secret_id: set GAIA_BAO_SECRET_ID_FILE or GAIA_BAO_SECRET_ID")
}

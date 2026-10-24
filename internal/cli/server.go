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
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newServerCommand() *cobra.Command {
	var dsn, listen, role string
	var workers int
	var unsafeNoAuth bool
	var repoURL, ref, repoCreds, dataDir string
	var refresh time.Duration
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
			bao := &baoAccess{log: cfg.Log}
			cfg.Repos = &repo.Mirrors{Dir: dataDir, Secrets: bao}
			var cat *server.CatalogSource
			if repoURL != "" {
				platform := repo.Rev{Name: "platform", URL: repoURL, Ref: ref, Credentials: repoCreds, Paths: paths}
				if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
					if platform.URL, err = filepath.Abs(repoURL); err != nil {
						return err
					}
				}
				cat = &server.CatalogSource{Repos: cfg.Repos, Platform: platform, Log: cfg.Log}
				if err := cat.Load(ctx); err != nil {
					return fmt.Errorf("desired state: %w", err)
				}
				go cat.Run(ctx, refresh, nil)
				if cat.Graph().AuthConfig() != nil {
					cfg.Auth = auth.New(s, cat.Graph, cfg.Log)
				}
			}
			if cfg.Listen != "" && cfg.Auth == nil && !unsafeNoAuth {
				if err := loopbackOnly(cfg.Listen); err != nil {
					return err
				}
			}
			if cfg.Workers > 0 {
				if _, err := bao.client(ctx, ""); err != nil {
					return err
				}
				cfg.Systems = func(g *catalog.Graph) (apply.Deps, error) {
					c, err := bao.client(ctx, secretsEndpoint(g))
					if err != nil {
						return apply.Deps{}, err
					}
					return apply.Systems(s, c, io.Discard), nil
				}
			}
			if cfg.Auth != nil {
				ac := cat.Graph().AuthConfig()
				needsBao := ac.Require == "mfa"
				for _, p := range ac.Providers {
					needsBao = needsBao || p.Type == "oidc" && p.ClientSecret != ""
				}
				if needsBao {
					c, err := bao.client(ctx, secretsEndpoint(cat.Graph()))
					if err != nil {
						return err
					}
					// TOTP seeds use the transit key gaia-auth; OIDC client
					// secrets are read from the KV store.
					cfg.Auth.WithTransit(c).WithSecrets(c)
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
	cmd.Flags().StringVar(&repoURL, "repo", "", "platform repository: a local path or a URL; without it the API is open, on loopback only")
	cmd.Flags().StringVar(&ref, "ref", "", "branch of --repo to follow (default: HEAD for a path, main for a URL)")
	cmd.Flags().StringSliceVar(&paths, "path", nil, "directory inside --repo; repeatable")
	cmd.Flags().StringVar(&repoCreds, "repo-credentials", "", "bao:// reference with a token (https) or private_key and known_hosts (ssh); needs $GAIA_BAO_ADDR")
	cmd.Flags().DurationVar(&refresh, "refresh", 3*time.Minute, "how often the repositories are fetched; a webhook fetches at once")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaultDataDir(), "where repository mirrors are kept")
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

func defaultDataDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "gaia", "repos")
	}
	return filepath.Join(os.TempDir(), "gaia-repos")
}

// baoAccess logs in to OpenBao the first time something needs it.
type baoAccess struct {
	log *slog.Logger
	mu  sync.Mutex
	fn  func(addr string) *openbao.Client
}

// client returns a client for addr ("" for $GAIA_BAO_ADDR).
func (b *baoAccess) client(ctx context.Context, addr string) (*openbao.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fn == nil {
		fn, err := baoLogin(ctx, b.log)
		if err != nil {
			return nil, err
		}
		b.fn = fn
	}
	if addr == "" {
		addr = os.Getenv("GAIA_BAO_ADDR")
	}
	return b.fn(addr), nil
}

// Read reads a secret at $GAIA_BAO_ADDR, for repository credentials.
func (b *baoAccess) Read(ctx context.Context, ref string) (map[string]string, error) {
	c, err := b.client(ctx, "")
	if err != nil {
		return nil, err
	}
	if c.Addr() == "" {
		return nil, errors.New("repository credentials need $GAIA_BAO_ADDR")
	}
	return c.Read(ctx, ref)
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

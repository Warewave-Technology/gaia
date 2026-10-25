package cli

import (
	"net/mail"

	"context"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
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
	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/drift"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/signature"
	gsource "github.com/Warewave-Technology/gaia/internal/source"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newServerCommand() *cobra.Command {
	var dsn, listen, role string
	var workers int
	var unsafeNoAuth bool
	var repoURL, ref, repoCreds, dataDir, webhookSecret, signingKey, author string
	var refresh, sourceEvery time.Duration
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
			var engine *change.Engine
			if repoURL != "" {
				platform := repo.Rev{Name: "platform", URL: repoURL, Ref: ref, Credentials: repoCreds, Paths: paths}
				if !strings.Contains(repoURL, "://") && !strings.Contains(repoURL, "@") {
					if platform.URL, err = filepath.Abs(repoURL); err != nil {
						return err
					}
				}
				cat = &server.CatalogSource{Repos: cfg.Repos, Platform: platform, Log: cfg.Log}
				engine = &change.Engine{Store: s, Repos: cfg.Repos, Log: cfg.Log, Keys: func(ctx context.Context) ([]signature.Key, error) {
					return signingKeys(ctx, auth.New(s, cat.Graph, cfg.Log))
				}}
				cat.After = func(ctx context.Context, snap repo.Snapshot, g *catalog.Graph) {
					if engine.Queue == nil {
						return
					}
					if err := engine.Evaluate(ctx, snap, g); err != nil {
						cfg.Log.Warn("change engine", "err", err)
					}
				}
				if err := cat.Load(ctx); err != nil {
					return fmt.Errorf("desired state: %w", err)
				}
				cfg.Changes, cfg.Catalog = engine, cat
				cfg.Sources = &server.Sources{Catalog: cat, Store: s, Syncer: &gsource.Syncer{
					Store: s, Repos: cfg.Repos, Log: cfg.Log,
					Secrets: func(ctx context.Context, addr, ref string) (map[string]string, error) {
						c, err := bao.client(ctx, addr)
						if err != nil {
							return nil, err
						}
						return c.Read(ctx, ref)
					},
				}}
				if signingKey != "" {
					w, err := gaiaWriter(ctx, s, bao, cfg.Repos, signingKey, author)
					if err != nil {
						return fmt.Errorf("--signing-key: %w", err)
					}
					cfg.Writer = w
					cfg.Sources.Syncer.Writer = w
				}
				if webhookSecret != "" {
					sec, err := bao.Read(ctx, webhookSecret)
					if err != nil {
						return fmt.Errorf("--webhook-secret: %w", err)
					}
					if sec["secret"] == "" {
						return errors.New("--webhook-secret: the secret has no \"secret\" field")
					}
					cfg.WebhookSecret = []byte(sec["secret"])
				}
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
				cfg.NotifySecrets = func(ctx context.Context, addr, ref string) (map[string]string, error) {
					c, err := bao.client(ctx, addr)
					if err != nil {
						return nil, err
					}
					return c.Read(ctx, ref)
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
			var closer *change.Engine
			if cfg.Workers > 0 {
				// Every worker closes the changes of the runs it finishes,
				// with or without a repository of its own.
				closer = engine
				if closer == nil {
					closer = &change.Engine{Store: s, Repos: cfg.Repos, Log: cfg.Log}
				}
				cfg.OperationDone = func(ctx context.Context, a queue.OperationArgs, status string) error {
					err := closer.OperationDone(ctx, a, status)
					if cat != nil {
						cat.Kick()
					}
					return err
				}
				cfg.Finished = func(ctx context.Context, a queue.ApplyArgs, g *catalog.Graph) error {
					err := closer.Finished(ctx, a, g)
					if cat != nil {
						cat.Kick()
					}
					return err
				}
			}
			srv, err := server.New(cfg, s)
			if err != nil {
				return err
			}
			notifier := &notify.Notifier{Store: s, Queue: srv.Queue(), Log: cfg.Log}
			if closer != nil {
				closer.Notify = notifier
			}
			if engine != nil {
				// The engine queues runs with the server's queue client; the
				// first evaluation happens at the first refresh.
				engine.Queue = srv.Queue()
				engine.Notify = notifier
				if cfg.Sources != nil {
					cfg.Sources.Queue = srv.Queue()
				}
				if cfg.Auth != nil {
					cfg.Auth.OnLoginKeys = func(ctx context.Context, userID string) {
						teams, err := s.Q().Teams(ctx, userID)
						if err == nil {
							_, err = queue.Redistribute(ctx, s, srv.Queue(), teams)
						}
						if err != nil {
							cfg.Log.Warn("access runs after a key change", "err", err)
						}
					}
				}
				if cfg.Sources != nil {
					cfg.Sources.Syncer.Notify = notifier
				}
				if cfg.Sources != nil {
					go cfg.Sources.Run(ctx, sourceEvery)
				}
				if cfg.Systems != nil {
					// Drift is checked where the change engine runs: it opens
					// the changes that put drift right.
					sc := &drift.Scanner{Store: s, Repos: cfg.Repos, Systems: cfg.Systems, Open: engine.OpenDrift, Notify: notifier, Log: cfg.Log, Graph: cat.Graph,
						Policy: func() *catalog.PolicySpec {
							if o := cat.Graph().Policy(); o != nil {
								return o.Spec.(*catalog.PolicySpec)
							}
							return nil
						}}
					go sc.Run(ctx, time.Minute)
				}
				go cat.Run(ctx, refresh)
				cat.Kick()
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
	cmd.Flags().StringVar(&signingKey, "signing-key", "", "bao:// reference whose private_key (OpenSSH) signs Gaia's own commits, such as reverts; the key needs push access through --repo-credentials and the providers' credentials")
	cmd.Flags().StringVar(&author, "commit-author", "Gaia <gaia@localhost>", "author of Gaia's commits")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "bao:// reference whose secret field checks GitHub push webhooks (POST /api/v1/webhooks/github)")
	cmd.Flags().DurationVar(&sourceEvery, "source-every", 10*time.Minute, "how often sources (TeamSource...) read their systems; POST /api/v1/sources/sync runs them at once")
	cmd.Flags().DurationVar(&refresh, "refresh", 3*time.Minute, "how often the repositories are fetched; a webhook fetches at once")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaultDataDir(), "where repository mirrors are kept")
	cmd.Flags().DurationVar(&stopTimeout, "stop-timeout", 5*time.Minute, "on shutdown, how long runs get to finish before they are cancelled")
	return cmd
}

// gaiaWriter reads Gaia's signing key and registers its public half, so
// that Gaia's commits pass the signature gate.
func gaiaWriter(ctx context.Context, s *store.Store, bao *baoAccess, m *repo.Mirrors, ref, author string) (*repo.Writer, error) {
	sec, err := bao.Read(ctx, ref)
	if err != nil {
		return nil, err
	}
	if sec["private_key"] == "" {
		return nil, errors.New("the secret has no private_key field")
	}
	key, err := ssh.ParsePrivateKey([]byte(sec["private_key"]))
	if err != nil {
		return nil, err
	}
	if err := change.RegisterGaiaKey(ctx, s, key.PublicKey()); err != nil {
		return nil, err
	}
	a, err := mail.ParseAddress(author)
	if err != nil {
		return nil, fmt.Errorf("--commit-author: %w", err)
	}
	return &repo.Writer{Mirrors: m, Signer: signature.SSHSigner{Key: key}, Name: a.Name, Email: a.Address}, nil
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

// signingKeys are the registered keys in the form the signature check
// takes.
func signingKeys(ctx context.Context, a *auth.Service) ([]signature.Key, error) {
	signers, err := a.Signers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]signature.Key, 0, len(signers))
	for _, sg := range signers {
		out = append(out, signature.Key{UserID: sg.UserID, Username: sg.Username, Kind: sg.Kind, Fingerprint: sg.Fingerprint, PublicKey: sg.PublicKey})
	}
	return out, nil
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
	if role := os.Getenv("GAIA_BAO_K8S_ROLE"); role != "" && addr != "" {
		// In Kubernetes: the pod's service account (design 13.3).
		mount, file := os.Getenv("GAIA_BAO_K8S_MOUNT"), os.Getenv("GAIA_BAO_K8S_TOKEN_FILE")
		if mount == "" {
			mount = "kubernetes"
		}
		if file == "" {
			file = "/var/run/secrets/kubernetes.io/serviceaccount/token"
		}
		sess, err := openbao.LoginKubernetes(ctx, addr, mount, role, file, log)
		if err != nil {
			return nil, err
		}
		go sess.Keep(ctx)
		log.Info("openbao: logged in with kubernetes auth", "addr", addr, "role", role)
		return func(addr string) *openbao.Client { return sess.Client(addr) }, nil
	}
	if addr == "" || roleID == "" {
		return nil, errors.New("workers need OpenBao: set GAIA_BAO_ADDR with GAIA_BAO_ROLE_ID and a secret_id (AppRole) or GAIA_BAO_K8S_ROLE (Kubernetes), or GAIA_BAO_TOKEN")
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

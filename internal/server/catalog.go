package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// CatalogSource keeps the server's view of the desired state: the platform
// repository and the repositories it leads to, at their heads, refreshed
// periodically and on demand (a webhook). Sign-in reads AuthConfig and
// Policy from it, the API the contracts' owners, the change engine the
// contracts themselves.
type CatalogSource struct {
	Repos    *repo.Mirrors
	Platform repo.Rev // Commit is ignored: the ref's head is followed
	Log      *slog.Logger
	// After is called after every refresh that loaded: the change engine
	// evaluates there, also when only the database changed.
	After func(context.Context, repo.Snapshot, *catalog.Graph)

	kick chan struct{}
	mu   sync.RWMutex
	snap repo.Snapshot
	g    *catalog.Graph
	err  error
}

// Load reads the heads now. A catalog that does not load keeps the last
// good one; the error is kept for the API to show.
func (c *CatalogSource) Load(ctx context.Context) error {
	snap, err := c.Repos.Head(ctx, c.Platform)
	var g *catalog.Graph
	if err == nil {
		g, err = c.Repos.Graph(ctx, snap)
	}
	c.mu.Lock()
	c.err = err
	if err == nil {
		c.snap, c.g = snap, g
	}
	c.mu.Unlock()
	if err == nil && c.After != nil {
		c.After(ctx, snap, g)
	}
	return err
}

// Kick asks for a refresh now: a webhook, an approval, a finished run.
func (c *CatalogSource) Kick() {
	c.mu.Lock()
	if c.kick == nil {
		c.kick = make(chan struct{}, 1)
	}
	k := c.kick
	c.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// Graph is the last catalog that loaded, or nil.
func (c *CatalogSource) Graph() *catalog.Graph {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.g
}

// Snapshot is the last snapshot that loaded, and the last load's error.
func (c *CatalogSource) Snapshot() (repo.Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap, c.err
}

// Run refreshes every interval, or at once when kicked, until ctx ends.
func (c *CatalogSource) Run(ctx context.Context, every time.Duration) {
	c.mu.Lock()
	if c.kick == nil {
		c.kick = make(chan struct{}, 1)
	}
	kick := c.kick
	c.mu.Unlock()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
		if err := c.Load(ctx); err != nil && c.Log != nil {
			c.Log.Warn("the desired state did not load; keeping the last one", "err", err)
		}
	}
}

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
	// OnChange is called after a refresh found a new snapshot.
	OnChange func(context.Context, repo.Snapshot, *catalog.Graph)

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
	changed := err == nil && snap.Label() != c.snap.Label()
	if err == nil {
		c.snap, c.g = snap, g
	}
	c.mu.Unlock()
	if changed && c.OnChange != nil {
		c.OnChange(ctx, snap, g)
	}
	return err
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
func (c *CatalogSource) Run(ctx context.Context, every time.Duration, kick <-chan struct{}) {
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

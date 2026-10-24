package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/queue"
)

// CatalogSource keeps the server's view of the catalog: the objects at the
// head of a local repository, reloaded periodically. Sign-in reads
// AuthConfig and Policy from it, the API the contracts' owners. The change
// engine replaces it with the tracked repositories (roadmap phase 3).
type CatalogSource struct {
	Src queue.Source // Commit is a ref, usually HEAD
	Log *slog.Logger

	mu sync.RWMutex
	g  *catalog.Graph
}

// Load reads the catalog now. A catalog that does not load keeps the last
// good one.
func (c *CatalogSource) Load() error {
	g, err := c.Src.Graph()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.g = g
	c.mu.Unlock()
	return nil
}

// Graph is the last catalog that loaded, or nil.
func (c *CatalogSource) Graph() *catalog.Graph {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.g
}

// Run reloads the catalog every interval until ctx ends.
func (c *CatalogSource) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Load(); err != nil && c.Log != nil {
				c.Log.Warn("catalog did not load; keeping the last one", "err", err)
			}
		}
	}
}

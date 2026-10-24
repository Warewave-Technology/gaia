package plan

import (
	"context"
	"fmt"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// Host is a server Gaia already manages.
type Host struct {
	UID      string
	Hostname string
	Site     string
	Network  string
	Seq      int
	Status   string
}

// State is what a plan reads about the servers that already exist.
type State interface {
	// LiveHosts returns the contract's servers that are not deleted.
	LiveHosts(ctx context.Context, contract string) ([]Host, error)
	// NameHolder returns the UID that holds a hostname, or "".
	NameHolder(ctx context.Context, name string) (string, error)
}

// NoState is an empty State: nothing exists yet.
type NoState struct{}

func (NoState) LiveHosts(context.Context, string) ([]Host, error)  { return nil, nil }
func (NoState) NameHolder(context.Context, string) (string, error) { return "", nil }

// maxSeq bounds the search for a free name.
const maxSeq = 999

// nextName picks the lowest sequence number unused in the site whose
// hostname nobody holds; generated names skip names taken by others
// (design 9.2). It marks the number used.
func nextName(ctx context.Context, g *catalog.Graph, o *catalog.Object, st State, site string, used map[string]map[int]bool) (int, string, error) {
	if used[site] == nil {
		used[site] = map[int]bool{}
	}
	for seq := 1; seq <= maxSeq; seq++ {
		if used[site][seq] {
			continue
		}
		host, err := g.Hostname(o, site, seq)
		if err != nil {
			return 0, "", err
		}
		holder, err := st.NameHolder(ctx, host)
		if err != nil {
			return 0, "", err
		}
		if holder != "" {
			continue
		}
		used[site][seq] = true
		return seq, host, nil
	}
	return 0, "", fmt.Errorf("no free name in site %s up to sequence %d", site, maxSeq)
}

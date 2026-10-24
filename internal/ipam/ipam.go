package ipam

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/Warewave-Technology/gaia/internal/ipam/db"
)

//go:generate sqlc generate

//go:embed migrations/*.sql
var migrations embed.FS

// Pool is a range of addresses inside a network.
type Pool struct {
	CIDR       string `json:"cidr"`
	Gateway    string `json:"gateway,omitempty"`
	RangeStart string `json:"rangeStart"`
	RangeEnd   string `json:"rangeEnd"`
	// What the pool is for, for a NetworkSource (design 5.2): a name,
	// labels such as site, env, zone, vlan or dnsZone, and tags.
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Tags   []string          `json:"tags,omitempty"`
}

// AllocateRequest asks for an address for a host.
type AllocateRequest struct {
	Pool     string `json:"pool"`
	Owner    string `json:"owner"` // the host's UID
	Hostname string `json:"hostname"`
}

// Allocation is an address given to a host.
type Allocation struct {
	Pool     string `json:"pool"`
	IP       string `json:"ip"`
	Prefix   int    `json:"prefix"`
	Gateway  string `json:"gateway,omitempty"`
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
}

var (
	errUnknownPool = errors.New("unknown pool")
	errExhausted   = errors.New("pool exhausted")
)

// Migrate brings the IPAM schema up to date.
func Migrate(ctx context.Context, dsn string) (err error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// Server is the IPAM service.
type Server struct {
	pool       *pgxpool.Pool
	token      string
	quarantine time.Duration
}

// NewServer connects to the IPAM database. Requests must carry token as a
// bearer token; released addresses are held back for quarantine.
func NewServer(ctx context.Context, dsn, token string, quarantine time.Duration) (*Server, error) {
	if token == "" {
		return nil, errors.New("an API token is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Server{pool: pool, token: token, quarantine: quarantine}, nil
}

// Close releases the database connections.
func (s *Server) Close() { s.pool.Close() }

// SetPool creates or updates a pool.
func (s *Server) SetPool(ctx context.Context, p Pool) error {
	cidr, err := netip.ParsePrefix(p.CIDR)
	if err != nil {
		return badRequest("cidr: %v", err)
	}
	if cidr != cidr.Masked() {
		return badRequest("cidr %s has host bits set (%s)", p.CIDR, cidr.Masked())
	}
	start, err1 := netip.ParseAddr(p.RangeStart)
	end, err2 := netip.ParseAddr(p.RangeEnd)
	if err1 != nil || err2 != nil || !cidr.Contains(start) || !cidr.Contains(end) || end.Less(start) {
		return badRequest("range %s-%s must be inside %s, start first", p.RangeStart, p.RangeEnd, p.CIDR)
	}
	var gw *netip.Addr
	if p.Gateway != "" {
		a, err := netip.ParseAddr(p.Gateway)
		if err != nil || !cidr.Contains(a) {
			return badRequest("gateway %s must be inside %s", p.Gateway, p.CIDR)
		}
		gw = &a
	}
	labels := p.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	lj, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return db.New(s.pool).UpsertPool(ctx, db.UpsertPoolParams{Cidr: cidr, Gateway: gw, RangeStart: start, RangeEnd: end, Name: p.Name, Labels: lj, Tags: tags})
}

// Allocate gives owner the lowest free address of the pool. Asking again
// for the same owner returns the address it already has.
func (s *Server) Allocate(ctx context.Context, r AllocateRequest) (Allocation, error) {
	cidr, err := netip.ParsePrefix(r.Pool)
	if err != nil {
		return Allocation{}, badRequest("pool: %v", err)
	}
	if r.Owner == "" || r.Hostname == "" {
		return Allocation{}, badRequest("owner and hostname are required")
	}
	var out Allocation
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		// Locking the pool row serializes allocations from one pool.
		pool, err := q.LockPool(ctx, cidr)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownPool
		}
		if err != nil {
			return err
		}
		if a, err := q.LiveAllocationOf(ctx, db.LiveAllocationOfParams{Pool: cidr, Owner: r.Owner}); err == nil {
			out = toAllocation(pool, a)
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		used, err := q.UnavailableIPs(ctx, cidr)
		if err != nil {
			return err
		}
		ip, ok := nextFree(pool, used)
		if !ok {
			return errExhausted
		}
		a, err := q.InsertAllocation(ctx, db.InsertAllocationParams{Pool: cidr, Ip: ip, Owner: r.Owner, Hostname: r.Hostname})
		if err != nil {
			return err
		}
		out = toAllocation(pool, a)
		return nil
	})
	return out, err
}

// Release frees every address of owner; they go into quarantine.
func (s *Server) Release(ctx context.Context, owner string) ([]Allocation, error) {
	if owner == "" {
		return nil, badRequest("owner is required")
	}
	rows, err := db.New(s.pool).ReleaseOwner(ctx, db.ReleaseOwnerParams{QuarantineSecs: s.quarantine.Seconds(), Owner: owner})
	if err != nil {
		return nil, err
	}
	return s.withPools(ctx, rows)
}

// Allocations returns the live addresses of owner.
func (s *Server) Allocations(ctx context.Context, owner string) ([]Allocation, error) {
	rows, err := db.New(s.pool).AllocationsOf(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.withPools(ctx, rows)
}

func (s *Server) withPools(ctx context.Context, rows []db.Allocation) ([]Allocation, error) {
	pools, err := db.New(s.pool).Pools(ctx)
	if err != nil {
		return nil, err
	}
	byCIDR := map[netip.Prefix]db.Pool{}
	for _, p := range pools {
		byCIDR[p.Cidr] = p
	}
	out := []Allocation{}
	for _, a := range rows {
		out = append(out, toAllocation(byCIDR[a.Pool], a))
	}
	return out, nil
}

// nextFree is the lowest address of the range that is not the network or
// broadcast address, not the gateway and not used.
func nextFree(p db.Pool, used []netip.Addr) (netip.Addr, bool) {
	taken := map[netip.Addr]bool{}
	for _, a := range used {
		taken[a] = true
	}
	if p.Gateway != nil {
		taken[*p.Gateway] = true
	}
	network := p.Cidr.Masked().Addr()
	broadcast := lastAddr(p.Cidr)
	for a := p.RangeStart; a.IsValid() && !p.RangeEnd.Less(a); a = a.Next() {
		if p.Cidr.Bits() < p.Cidr.Addr().BitLen()-1 && (a == network || a == broadcast) {
			continue
		}
		if !taken[a] {
			return a, true
		}
	}
	return netip.Addr{}, false
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	host := p.Addr().BitLen() - p.Bits()
	for i := len(b) - 1; host > 0; i-- {
		n := min(host, 8)
		b[i] |= byte(1<<n - 1)
		host -= n
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

func toAllocation(p db.Pool, a db.Allocation) Allocation {
	out := Allocation{Pool: a.Pool.String(), IP: a.Ip.String(), Prefix: a.Pool.Bits(), Owner: a.Owner, Hostname: a.Hostname}
	if p.Gateway != nil {
		out.Gateway = p.Gateway.String()
	}
	return out
}

type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, a ...any) error { return &requestError{fmt.Sprintf(format, a...)} }

// Handler serves the API:
//
//	POST   /v1/pools                 create or update a pool
//	GET    /v1/pools
//	POST   /v1/allocations           allocate for an owner
//	GET    /v1/allocations?owner=    an owner's live addresses
//	DELETE /v1/allocations?owner=    release an owner's addresses
//	GET    /healthz                  no token needed
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.pool.Ping(r.Context()); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/pools", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var p Pool
		if !decode(w, r, &p) {
			return
		}
		if err := s.SetPool(r.Context(), p); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("GET /v1/pools", s.auth(func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.New(s.pool).Pools(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		out := []Pool{}
		for _, p := range rows {
			v := Pool{CIDR: p.Cidr.String(), RangeStart: p.RangeStart.String(), RangeEnd: p.RangeEnd.String(), Name: p.Name, Tags: p.Tags}
			_ = json.Unmarshal(p.Labels, &v.Labels)
			if p.Gateway != nil {
				v.Gateway = p.Gateway.String()
			}
			out = append(out, v)
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /v1/allocations", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var req AllocateRequest
		if !decode(w, r, &req) {
			return
		}
		a, err := s.Allocate(r.Context(), req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("GET /v1/allocations", s.auth(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.Allocations(r.Context(), r.URL.Query().Get("owner"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("DELETE /v1/allocations", s.auth(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.Release(r.Context(), r.URL.Query().Get("owner"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body: " + err.Error()})
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	var re *requestError
	switch {
	case errors.As(err, &re):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": re.msg})
	case errors.Is(err, errUnknownPool):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errExhausted):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

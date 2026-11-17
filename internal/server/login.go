package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// Login protection (T18). Every password check (login, and the current password of a password change) goes through
// checkPassword:
//   - a user is locked out for login_lockout after every login_max_failures-th wrong password in a row; the lockout is
//     in the catalog (it survives a restart) and is a state change; an admin can end it (POST /users/{id}/unlock);
//   - a username that does not exist is counted the same way, in memory, so a lockout does not reveal which names exist;
//   - a client address with login_address_failures wrong passwords within login_address_window is refused until the
//     oldest of them leaves the window.
//
// A refused attempt is answered 429 with Retry-After, without checking the password, and is an audit entry like every
// login.

var errTooManyLogins = errors.New("too many failed logins")

// loginGuard holds the in-memory counts: unknown usernames and client addresses.
type loginGuard struct {
	mu    sync.Mutex
	names map[string]*nameCount
	addrs map[string][]time.Time // failure times within the window, oldest first
}

type nameCount struct {
	failures int
	until    time.Time
	last     time.Time
}

// maxTracked bounds each map: past it, entries that no longer matter are dropped first.
const maxTracked = 10000

func newLoginGuard() *loginGuard {
	return &loginGuard{names: map[string]*nameCount{}, addrs: map[string][]time.Time{}}
}

// addrBlocked returns until when an address is refused (zero: it is not), and how many failures it has in the window.
func (g *loginGuard) addrBlocked(addr string, now time.Time, limit int, window time.Duration) (time.Time, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ts := recent(g.addrs[addr], now, window)
	g.addrs[addr] = ts
	if len(ts) >= limit {
		return ts[len(ts)-limit].Add(window), len(ts)
	}
	return time.Time{}, len(ts)
}

func (g *loginGuard) addrFailed(addr string, now time.Time, window time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.addrs) >= maxTracked {
		for a, ts := range g.addrs {
			if len(recent(ts, now, window)) == 0 {
				delete(g.addrs, a)
			}
		}
	}
	g.addrs[addr] = append(recent(g.addrs[addr], now, window), now)
}

func recent(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= window {
		i++
	}
	return ts[i:]
}

// nameLocked: an unknown username's lockout.
func (g *loginGuard) nameLocked(name string, now time.Time) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c := g.names[name]; c != nil && now.Before(c.until) {
		return c.until
	}
	return time.Time{}
}

// nameFailed counts a failure for an unknown username as LoginFailed does for a user.
func (g *loginGuard) nameFailed(name string, now time.Time, max int, lockout time.Duration) (int, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.names) >= maxTracked {
		for n, c := range g.names {
			if now.After(c.until) && now.Sub(c.last) > lockout {
				delete(g.names, n)
			}
		}
	}
	c := g.names[name]
	if c == nil {
		c = &nameCount{}
		g.names[name] = c
	}
	c.failures++
	c.last = now
	if c.failures%max == 0 {
		c.until = now.Add(lockout)
		return c.failures, c.until
	}
	return c.failures, time.Time{}
}

// refuse answers a refused attempt: 429 with Retry-After.
func refuse(c *call, until, now time.Time, why string) error {
	secs := int(until.Sub(now).Seconds() + 0.999)
	c.w.Header().Set("Retry-After", fmt.Sprint(max(secs, 1)))
	c.detail = "login refused: " + why
	return fmt.Errorf("%w: %s; try again after %s", errTooManyLogins, why, until.UTC().Format(time.RFC3339))
}

// checkPassword checks a username and password under the login protection above, and sets the audit detail.
func (s *Server) checkPassword(ctx context.Context, c *call, username, password string) (store.User, error) {
	now := s.now()
	addr := remote(c.r)
	window := s.cfg.loginAddressWindow()
	if until, n := s.logins.addrBlocked(addr, now, s.cfg.LoginAddressFailures, window); !until.IsZero() {
		return store.User{}, refuse(c, until, now, fmt.Sprintf("%d wrong passwords from %s within %s", n, addr, window))
	}
	u, err := s.db.UserByName(ctx, username)
	known := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, err
	}
	locked, until := u.Locked(now)
	if !known {
		until = s.logins.nameLocked(username, now)
		locked = !until.IsZero()
	}
	if locked {
		return store.User{}, refuse(c, until, now, "too many wrong passwords for this username")
	}
	got, err := s.db.Authenticate(ctx, username, password)
	if !errors.Is(err, store.ErrBadLogin) {
		if err == nil && (got.FailedLogins > 0 || got.LockedUntil != "") {
			err = s.db.LoginSucceeded(ctx, got.ID)
		}
		return got, err
	}
	s.logins.addrFailed(addr, now, window)
	lockout := s.cfg.loginLockout()
	var n int
	var lockedNow time.Time
	if known {
		after, didLock, lerr := s.db.LoginFailed(ctx, u.ID, s.cfg.LoginMaxFailures, lockout)
		if lerr != nil {
			return store.User{}, lerr
		}
		n = after.FailedLogins
		if didLock {
			_, lockedNow = after.Locked(now)
		}
	} else {
		n, lockedNow = s.logins.nameFailed(username, now, s.cfg.LoginMaxFailures, lockout)
	}
	c.detail = fmt.Sprintf("wrong password (%d in a row)", n)
	if !lockedNow.IsZero() {
		c.detail += "; locked out until " + lockedNow.UTC().Format(time.RFC3339)
	}
	return store.User{}, err
}

func (s *Server) unlockUser(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	u, err := s.db.UnlockUser(c.r.Context(), c.user.Username, id)
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + u.Username
	c.detail = "unlocked"
	return http.StatusOK, u, nil
}

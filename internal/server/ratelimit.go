package server

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// What-if is not audited and every role may call it (ADR-007 C13), so it is rate-limited per session (or API token):
// whatIfLimit calls in any whatIfWindow. A refusal is 429 too_many_requests with reason rate_limited and
// Retry-After, the shape of a refused sign-in attempt.
const (
	whatIfLimit  = 10
	whatIfWindow = 10 * time.Second
)

var errRateLimited = errors.New("too many requests")

// rateError is a refused request: reason rate_limited.
type rateError struct{ err error }

func (e *rateError) Error() string { return e.err.Error() }
func (e *rateError) Unwrap() error { return e.err }

// rateLimiter counts each key's calls in a sliding window.
type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// allow records a call for key at now when fewer than n were made in the last per; otherwise it says how long until
// the oldest of them leaves the window.
func (l *rateLimiter) allow(key string, now time.Time, n int, per time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	h := l.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) >= per {
		i++
	}
	h = h[i:]
	if len(h) >= n {
		l.hits[key] = h
		return false, per - now.Sub(h[0])
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 { // forget idle keys: sessions end
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= per {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}

// limitWhatIf refuses a what-if call over the session's limit.
func (s *Server) limitWhatIf(c *call) error {
	key := fmt.Sprintf("session:%d", c.session.ID)
	if c.token != nil {
		key = fmt.Sprintf("token:%d", c.token.ID)
	}
	ok, wait := s.whatIfRate.allow(key, time.Now(), whatIfLimit, whatIfWindow)
	if ok {
		return nil
	}
	secs := max(1, int(wait.Seconds()+0.999))
	c.w.Header().Set("Retry-After", fmt.Sprint(secs))
	return &rateError{fmt.Errorf("%w: at most %d what-if calls in %d seconds per session; try again in %d s", errRateLimited, whatIfLimit,
		int(whatIfWindow.Seconds()), secs)}
}

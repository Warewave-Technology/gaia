package verify

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is what one probe saw. It is printed as JSON by `gaia probe`, which runs
// inside the sandbox's network namespace.
type ProbeResult struct {
	OK       bool    `json:"ok"`
	Attempts int     `json:"attempts"`
	Seconds  float64 `json:"seconds"` // until success, or until giving up
	Detail   string  `json:"detail"`
	Status   int     `json:"status,omitempty"`
	BodyHead string  `json:"body_head,omitempty"`
}

// Probe retries check c against host until it passes or timeout expires.
// Only tcp and http checks are probes.
func Probe(ctx context.Context, c Check, host string, timeout time.Duration) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r ProbeResult
	for {
		r.Attempts++
		ok, detail, status, body := probeOnce(ctx, c, host)
		r.Detail, r.Status, r.BodyHead = detail, status, body
		if ok {
			r.OK = true
			break
		}
		select {
		case <-ctx.Done():
			r.Detail = fmt.Sprintf("gave up after %s: %s", timeout, detail)
			r.Seconds = time.Since(start).Seconds()
			return r
		case <-time.After(2 * time.Second):
		}
	}
	r.Seconds = time.Since(start).Seconds()
	return r
}

func probeOnce(ctx context.Context, c Check, host string) (ok bool, detail string, status int, body string) {
	addr := net.JoinHostPort(host, strconv.Itoa(c.Port))
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch c.Type {
	case CheckTCP:
		var d net.Dialer
		conn, err := d.DialContext(actx, "tcp", addr)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		conn.Close()
		return true, "connected to " + addr, 0, ""
	case CheckHTTP:
		url := "http://" + addr + c.Path
		req, err := http.NewRequestWithContext(actx, http.MethodGet, url, nil)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		head := string(b[:min(len(b), 256)])
		switch {
		case resp.StatusCode != c.ExpectStatus:
			return false, fmt.Sprintf("GET %s: status %d, want %d", url, resp.StatusCode, c.ExpectStatus), resp.StatusCode, head
		case c.ExpectBody != "" && !strings.Contains(string(b), c.ExpectBody):
			return false, fmt.Sprintf("GET %s: body does not contain %q", url, c.ExpectBody), resp.StatusCode, head
		}
		return true, fmt.Sprintf("GET %s: %d", url, resp.StatusCode), resp.StatusCode, head
	}
	return false, "not a probe: " + c.Type, 0, ""
}

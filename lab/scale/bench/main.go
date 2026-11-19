// Command bench is a fixture (T24 gate a and c): it measures a running Gaia server's view endpoints on the synthetic
// catalog (lab/scale/gen), the full recompute of the status table, and the server's memory.
//
//	bench -url https://127.0.0.1:8443 -cert /var/lib/gaia/tls/cert.pem -password-file FILE [-user admin] [-n 200] [-pid PID]
//
// Every endpoint gets n requests, one at a time, with parameters drawn from the catalog (hosts, tags, codes, deep
// pages, names, point ids), so no answer is a cache hit of the one before. Times are wall time to the last byte.
// Limits (CLAUDE.md T24): p95 <= 300 ms for a view, <= 150 ms for search; a full recompute <= 30 s. It prints one
// JSON document and exits 0 when every limit holds, 2 when one does not, 3 when it could not measure.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/client"
)

type endpoint struct {
	Name    string  `json:"name"`
	Example string  `json:"example"`
	N       int     `json:"n"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	Max     float64 `json:"max_ms"`
	Bytes   int     `json:"mean_bytes"`
	LimitMS float64 `json:"limit_p95_ms"`
	Pass    bool    `json:"pass"`
}

type result struct {
	At        string         `json:"at"`
	Server    string         `json:"server_version"`
	Machine   map[string]any `json:"machine"`
	Fleet     map[string]any `json:"fleet"`
	Endpoints []endpoint     `json:"endpoints"`
	Recompute []any          `json:"recompute"` // POST /status/recompute answers
	Check     any            `json:"check"`     // GET /status/check
	RSS       map[string]any `json:"server_rss"`
	Verdict   string         `json:"verdict"`
	Failures  []string       `json:"failures"`
}

func main() {
	base := flag.String("url", "https://127.0.0.1:8443", "the server")
	certFile := flag.String("cert", "/var/lib/gaia/tls/cert.pem", "the server's TLS certificate (its fingerprint is pinned)")
	user := flag.String("user", "admin", "an admin (the recompute needs one)")
	pwFile := flag.String("password-file", "", "a file holding the user's password")
	n := flag.Int("n", 200, "requests per endpoint")
	pid := flag.Int("pid", 0, "the server's process id, for its memory")
	recomputes := flag.Int("recomputes", 3, "full recomputes to time")
	flag.Parse()
	ctx := context.Background()
	res := result{At: time.Now().UTC().Format(time.RFC3339), Failures: []string{}, Machine: machine()}

	fp, err := fingerprint(*certFile)
	check(err)
	pw, err := os.ReadFile(*pwFile)
	check(err)
	anon := client.New(client.Config{Server: *base, CertSHA256: fp})
	b, _, err := anon.Do(ctx, "POST", "/api/v1/login", map[string]string{"username": *user, "password": strings.TrimSpace(string(pw))})
	check(err)
	var login struct {
		Token string `json:"token"`
	}
	check(json.Unmarshal(b, &login))
	c := client.New(client.Config{Server: *base, CertSHA256: fp, Token: login.Token})
	get := func(path string, v any) {
		b, _, err := c.Do(ctx, "GET", path, nil)
		check(err)
		if v != nil {
			check(json.Unmarshal(b, v))
		}
	}
	var sys struct {
		Version string `json:"version"`
	}
	get("/api/v1/system", &sys)
	res.Server = sys.Version
	res.RSS = map[string]any{"before": rss(*pid)}

	// What the parameters are drawn from.
	var fleet map[string]any
	get("/api/v1/views/fleet", &fleet)
	res.Fleet = map[string]any{"vms": fleet["vms"], "can_return": fleet["can_return"], "by_status": fleet["by_status"], "causes": fleet["causes"]}
	var cov struct {
		Groups []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"groups"`
	}
	get("/api/v1/views/coverage?by=host&all=true&limit=200", &cov)
	var hosts []int64
	for _, g := range cov.Groups {
		hosts = append(hosts, g.ID)
	}
	get("/api/v1/views/coverage?by=tag&all=true&limit=200", &cov)
	var tags []string
	for _, g := range cov.Groups {
		tags = append(tags, g.Name)
	}
	var att struct {
		Groups []struct {
			Code  string `json:"code"`
			Scope *struct {
				Kind string `json:"kind"`
				ID   int64  `json:"id"`
			} `json:"scope"`
		} `json:"groups"`
	}
	get("/api/v1/views/attention?limit=200", &att)
	var codes []string
	for _, g := range att.Groups {
		q := "code=" + g.Code
		if g.Scope != nil {
			q += fmt.Sprintf("&scope_kind=%s&scope_id=%d", g.Scope.Kind, g.Scope.ID)
		}
		codes = append(codes, q)
	}
	var cursors, names []string
	var vmIDs []int64
	after := ""
	for page := 0; page < 40; page++ { // cursors of deep pages, and names and ids from across the list
		var l struct {
			Rows []struct {
				VMID   int64  `json:"vm_id"`
				VM     string `json:"vm"`
				Proven *struct {
					PointID int64 `json:"point_id"`
				} `json:"proven"`
			} `json:"rows"`
			Next string `json:"next"`
		}
		get("/api/v1/views/vm-list?limit=100&after="+url.QueryEscape(after), &l)
		for _, r := range l.Rows {
			vmIDs, names = append(vmIDs, r.VMID), append(names, r.VM)
		}
		if l.Next == "" {
			break
		}
		cursors, after = append(cursors, l.Next), l.Next
	}
	if len(hosts) == 0 || len(names) == 0 || len(tags) == 0 || len(codes) == 0 || len(cursors) == 0 {
		fmt.Fprintf(os.Stderr, "UNKNOWN: bench: the catalog has too little to draw from (hosts %d, VMs %d, tags %d, causes %d, pages %d)\n",
			len(hosts), len(names), len(tags), len(codes), len(cursors))
		os.Exit(3)
	}
	var points []int64
	for _, id := range vmIDs[:min(50, len(vmIDs))] {
		var ps []struct {
			ID int64 `json:"id"`
		}
		get(fmt.Sprintf("/api/v1/points?vm_id=%d&limit=5", id), &ps)
		for _, p := range ps {
			points = append(points, p.ID)
		}
	}
	rng := rand.New(rand.NewPCG(7, 11))
	one := func(xs []string) string { return xs[rng.IntN(len(xs))] }
	frag := func() string { // a piece of a VM name: a prefix, a policy word or a number
		nm := one(names)
		parts := strings.Split(nm, "-")
		return parts[rng.IntN(len(parts))]
	}

	type spec struct {
		name  string
		limit float64
		path  func() string
	}
	specs := []spec{
		{"fleet", 300, func() string { return "/api/v1/views/fleet" }},
		{"attention", 300, func() string { return "/api/v1/views/attention" }},
		{"coverage_host", 300, func() string { return "/api/v1/views/coverage?by=host" }},
		{"coverage_policy", 300, func() string { return "/api/v1/views/coverage?by=policy" }},
		{"coverage_tag", 300, func() string { return "/api/v1/views/coverage?by=tag" }},
		{"vm_list", 300, func() string { return "/api/v1/views/vm-list" }},
		{"vm_list_attention", 300, func() string { return "/api/v1/views/vm-list?attention=true" }},
		{"vm_list_status", 300, func() string {
			return "/api/v1/views/vm-list?status=" + one([]string{"CRITICAL", "UNKNOWN", "WARNING", "OK", "CRITICAL,UNKNOWN"})
		}},
		{"vm_list_host", 300, func() string { return fmt.Sprintf("/api/v1/views/vm-list?host_id=%d", hosts[rng.IntN(len(hosts))]) }},
		{"vm_list_tag", 300, func() string { return "/api/v1/views/vm-list?tag=" + url.QueryEscape(one(tags)) }},
		{"vm_list_code", 300, func() string { return "/api/v1/views/vm-list?" + one(codes) }},
		{"vm_list_deep_page", 300, func() string { return "/api/v1/views/vm-list?after=" + url.QueryEscape(one(cursors)) }},
		{"vm_list_by_name", 300, func() string { return "/api/v1/views/vm-list?sort=name" }},
		{"vm_list_text", 300, func() string { return "/api/v1/views/vm-list?q=" + url.QueryEscape(frag()) }},
		{"vm_page", 300, func() string { return fmt.Sprintf("/api/v1/views/vms/%d", vmIDs[rng.IntN(len(vmIDs))]) }},
		{"overview_t20", 300, func() string { return "/api/v1/views/overview?limit=100" }},
		{"status_page", 300, func() string { return fmt.Sprintf("/api/v1/status?after=%d&limit=100", vmIDs[rng.IntN(len(vmIDs))]) }},
		{"search_name", 150, func() string { return "/api/v1/search?q=" + url.QueryEscape(frag()) }},
		{"search_point", 150, func() string { return fmt.Sprintf("/api/v1/search?q=%d", points[rng.IntN(len(points))]) }},
	}
	for _, sp := range specs {
		times := make([]float64, 0, *n)
		bytes := 0
		ex := sp.path()
		for range *n {
			p := sp.path()
			t0 := time.Now()
			b, _, err := c.Do(ctx, "GET", p, nil)
			ms := float64(time.Since(t0).Microseconds()) / 1000
			if err != nil {
				fmt.Fprintf(os.Stderr, "UNKNOWN: bench: %s: %v\n", p, err)
				os.Exit(3)
			}
			times, bytes = append(times, ms), bytes+len(b)
		}
		slices.Sort(times)
		q := func(f float64) float64 { return times[min(len(times)-1, int(f*float64(len(times))))] }
		e := endpoint{Name: sp.name, Example: ex, N: len(times), P50: q(0.50), P95: q(0.95), P99: q(0.99), Max: times[len(times)-1],
			Bytes: bytes / len(times), LimitMS: sp.limit}
		e.Pass = e.P95 <= sp.limit
		if !e.Pass {
			res.Failures = append(res.Failures, fmt.Sprintf("%s: p95 %.1f ms > %.0f ms", sp.name, e.P95, sp.limit))
		}
		res.Endpoints = append(res.Endpoints, e)
	}
	res.RSS["after_views"] = rss(*pid)

	for range *recomputes {
		b, _, err := c.Do(ctx, "POST", "/api/v1/status/recompute", nil)
		check(err)
		var r map[string]any
		check(json.Unmarshal(b, &r))
		res.Recompute = append(res.Recompute, r)
		if s, _ := r["seconds"].(float64); s > 30 {
			res.Failures = append(res.Failures, fmt.Sprintf("full recompute %.1f s > 30 s", s))
		}
	}
	var chk map[string]any
	get("/api/v1/status/check", &chk)
	res.Check = chk
	if chk["verdict"] != "OK" {
		res.Failures = append(res.Failures, "the status table differs from the status model")
	}
	res.RSS["after_recompute"] = rss(*pid)

	res.Verdict = "OK"
	if len(res.Failures) > 0 {
		res.Verdict = "CRITICAL"
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	check(enc.Encode(res))
	if res.Verdict != "OK" {
		os.Exit(2)
	}
}

func fingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", fmt.Errorf("%s: no PEM certificate", path)
	}
	if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
		return "", err
	}
	return client.Fingerprint(blk.Bytes), nil
}

// rss is the server's resident memory now and at its peak (Linux /proc), or from ps elsewhere; nil without a pid.
func rss(pid int) map[string]any {
	if pid <= 0 {
		return nil
	}
	out := map[string]any{}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			for _, k := range []string{"VmRSS", "VmHWM"} {
				if v, ok := strings.CutPrefix(l, k+":"); ok {
					kb, _ := strconv.Atoi(strings.Fields(v)[0])
					out[k+"_mib"] = float64(kb) / 1024
				}
			}
		}
		return out
	}
	if b, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		kb, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		out["rss_mib"] = float64(kb) / 1024
	}
	return out
}

// machine is where the measurement ran.
func machine() map[string]any {
	m := map[string]any{"cpus": runtime.NumCPU(), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
				kb, _ := strconv.Atoi(strings.Fields(v)[0])
				m["mem_total_mib"] = kb / 1024
			}
		}
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "model name"); ok {
				m["cpu_model"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":"))
				break
			}
		}
	}
	return m
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "UNKNOWN: bench:", err)
		os.Exit(3)
	}
}

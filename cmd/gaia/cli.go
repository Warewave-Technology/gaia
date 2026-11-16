package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Warewave-Technology/gaia/internal/client"
)

// The CLI side of gaia server: every command is an API call (ADR-007 §6).
//
//	gaia login -server https://HOST:8443 -user NAME      gaia logout | whoami | system | password
//	gaia RESOURCE list [key=value ...]                   query filters, e.g. vm_id=3 limit=20
//	gaia RESOURCE get ID
//	gaia RESOURCE add key=value ...                      a JSON body; values that parse as JSON are typed
//	gaia RESOURCE set ID key=value ...
//	gaia RESOURCE rm ID
//	gaia status [VM ID]                                  every VM's status (exit code: the worst status)
//	gaia vms disks ID | add-disk ID target=vda size_bytes=N | backup ID | test ID | status ID
//	gaia hosts scan ID | trust ID fingerprint=SHA256:... | check ID
//	gaia points disks ID | restore-check ID
//	gaia repositories verify ID mode=sampled|full | maintenance ID [force=true]
//	gaia evidence export ID [-o FILE]                    gaia audit verify          gaia state-backups run
//	gaia api METHOD PATH [JSON]                          any API call
//
// -json anywhere prints the server's JSON instead of a table.

type resource struct {
	path  string
	verbs []string
}

var resources = map[string]resource{
	"users":         {"/api/v1/users", []string{"list", "get", "add", "set"}},
	"hosts":         {"/api/v1/hosts", []string{"list", "get", "add", "set", "rm", "scan", "trust", "check"}},
	"vms":           {"/api/v1/vms", []string{"list", "get", "add", "set", "rm", "disks", "add-disk", "backup", "test", "status"}},
	"repositories":  {"/api/v1/repositories", []string{"list", "get", "add", "verify", "maintenance"}},
	"policies":      {"/api/v1/policies", []string{"list", "get", "add", "set", "rm"}},
	"jobs":          {"/api/v1/jobs", []string{"list", "get"}},
	"points":        {"/api/v1/points", []string{"list", "get", "disks", "restore-check"}},
	"verifications": {"/api/v1/verifications", []string{"list", "get"}},
	"tests":         {"/api/v1/restore-tests", []string{"list", "get"}},
	"evidence":      {"/api/v1/evidence", []string{"list", "get", "export"}},
	"audit":         {"/api/v1/audit", []string{"list", "verify"}},
	"state-changes": {"/api/v1/state-changes", []string{"list"}},
	"state-backups": {"/api/v1/state-backups", []string{"list", "run"}},
}

// isCLICommand reports whether args[0] is handled by runCLI.
func isCLICommand(name string) bool {
	_, ok := resources[name]
	return ok || slices.Contains([]string{"login", "logout", "whoami", "system", "password", "api", "status"}, name)
}

// stripJSON removes -json / --json from args.
func stripJSON(args []string) ([]string, bool) {
	var out []string
	asJSON := false
	for _, a := range args {
		if a == "-json" || a == "--json" {
			asJSON = true
			continue
		}
		out = append(out, a)
	}
	return out, asJSON
}

func runCLI(ctx context.Context, args []string) int {
	args, asJSON := stripJSON(args)
	cmd := args[0]
	if cmd == "login" {
		return cliLogin(ctx, args[1:], asJSON)
	}
	cfg, err := client.Load()
	if err != nil {
		return fail(asJSON, cmd, err)
	}
	c := client.New(cfg)
	call := func(method, path string, body any) int {
		b, _, err := c.Do(ctx, method, path, body)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		return show(b, asJSON)
	}
	switch cmd {
	case "logout":
		if _, _, err := c.Do(ctx, "POST", "/api/v1/logout", nil); err != nil {
			fmt.Fprintln(os.Stderr, "the server did not end the session:", err)
		}
		cfg.Token, cfg.ExpiresAt = "", ""
		if err := cfg.Save(); err != nil {
			return fail(asJSON, cmd, err)
		}
		fmt.Println("logged out")
		return 0
	case "whoami":
		return call("GET", "/api/v1/me", nil)
	case "status":
		return cliStatus(ctx, c, args[1:], asJSON)
	case "system":
		return call("GET", "/api/v1/system", nil)
	case "password":
		cur, err := readPassword("", "Current password: ", false)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		next, err := readPassword("", "New password: ", true)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		if _, _, err := c.Do(ctx, "PUT", "/api/v1/me/password", map[string]string{"current_password": cur, "new_password": next}); err != nil {
			return fail(asJSON, cmd, err)
		}
		fmt.Println("password changed; log in again")
		return 0
	case "api":
		if len(args) < 3 {
			return fail(asJSON, cmd, errors.New("usage: gaia api METHOD PATH [JSON]"))
		}
		var body any
		if len(args) > 3 {
			body = []byte(args[3])
		}
		b, _, err := c.Do(ctx, strings.ToUpper(args[1]), args[2], body)
		if err != nil {
			return fail(true, cmd, err)
		}
		os.Stdout.Write(b)
		return 0
	}
	res := resources[cmd]
	if len(args) < 2 || !slices.Contains(res.verbs, args[1]) {
		return fail(asJSON, cmd, fmt.Errorf("usage: gaia %s %s", cmd, strings.Join(res.verbs, "|")))
	}
	verb, rest := args[1], args[2:]
	needID := func() (string, []string, error) {
		if len(rest) == 0 || strings.Contains(rest[0], "=") {
			return "", nil, fmt.Errorf("usage: gaia %s %s ID ...", cmd, verb)
		}
		return url.PathEscape(rest[0]), rest[1:], nil
	}
	switch verb {
	case "list":
		q := url.Values{}
		for _, kv := range rest {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fail(asJSON, cmd, fmt.Errorf("filter %q: want key=value", kv))
			}
			q.Set(k, v)
		}
		p := res.path
		if len(q) > 0 {
			p += "?" + q.Encode()
		}
		return call("GET", p, nil)
	case "run":
		return call("POST", res.path, nil)
	case "add":
		body, err := keyValues(rest)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		if cmd == "users" {
			if err := askPassword(body, "Password for the new user: "); err != nil {
				return fail(asJSON, cmd, err)
			}
		}
		return call("POST", res.path, body)
	}
	if verb == "verify" && cmd == "audit" {
		return call("GET", res.path+"/verify", nil)
	}
	id, rest, err := needID()
	if err != nil {
		return fail(asJSON, cmd, err)
	}
	switch verb {
	case "get":
		return call("GET", res.path+"/"+id, nil)
	case "scan", "check", "restore-check":
		return call("POST", res.path+"/"+id+"/"+verb, nil)
	case "backup":
		return call("POST", res.path+"/"+id+"/backups", nil)
	case "test":
		return call("POST", res.path+"/"+id+"/tests", nil)
	case "status":
		return call("GET", res.path+"/"+id+"/status", nil)
	case "trust", "verify", "maintenance":
		body, err := keyValues(rest)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		return call("POST", res.path+"/"+id+"/"+verb, body)
	case "rm":
		if _, _, err := c.Do(ctx, "DELETE", res.path+"/"+id, nil); err != nil {
			return fail(asJSON, cmd, err)
		}
		fmt.Printf("removed %s %s\n", strings.TrimSuffix(cmd, "s"), id)
		return 0
	case "set":
		body, err := keyValues(rest)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		if cmd == "users" && body["password"] == "-" {
			if err := askPassword(body, "New password: "); err != nil {
				return fail(asJSON, cmd, err)
			}
		}
		return call("PATCH", res.path+"/"+id, body)
	case "disks":
		return call("GET", res.path+"/"+id+"/disks", nil)
	case "add-disk":
		body, err := keyValues(rest)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		return call("POST", res.path+"/"+id+"/disks", body)
	case "export":
		out := "gaia-evidence-" + id + ".tar.gz"
		for i := 0; i+1 < len(rest); i++ {
			if rest[i] == "-o" {
				out = rest[i+1]
			}
		}
		b, _, err := c.Do(ctx, "GET", res.path+"/"+id+"/export", nil)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return fail(asJSON, cmd, err)
		}
		fmt.Printf("wrote %s (%d bytes); verify it with the README inside\n", out, len(b))
		return 0
	}
	return fail(asJSON, cmd, fmt.Errorf("verb %s", verb))
}

// askPassword fills body["password"] from the terminal unless it was given (passwords do not belong in argv).
func askPassword(body map[string]any, prompt string) error {
	if p, ok := body["password"].(string); ok && p != "-" {
		return errors.New("do not put a password on the command line: leave it out (or password=- with set) and type it when asked")
	}
	pw, err := readPassword("", prompt, true)
	if err != nil {
		return err
	}
	body["password"] = pw
	return nil
}

// keyValues turns key=value arguments into a JSON object; a value that is valid JSON keeps its type.
func keyValues(args []string) (map[string]any, error) {
	body := map[string]any{}
	for _, kv := range args {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("argument %q: want key=value", kv)
		}
		var j any
		if err := json.Unmarshal([]byte(v), &j); err == nil {
			body[k] = j
		} else {
			body[k] = v
		}
	}
	return body, nil
}

func cliLogin(ctx context.Context, args []string, asJSON bool) int {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	srv := fs.String("server", "", "https://HOST:PORT of gaia server")
	user := fs.String("user", "", "username")
	fp := fs.String("fingerprint", "", "the server certificate's sha256 (from gaia server init); asked for when not given")
	pwFile := fs.String("password-file", "", "file holding the password (default: ask)")
	fs.Parse(args)
	if *srv == "" || *user == "" {
		return fail(asJSON, "login", errors.New("-server and -user are required"))
	}
	*srv = strings.TrimSuffix(*srv, "/")
	got, err := client.ServerFingerprint(ctx, *srv)
	if err != nil {
		return fail(asJSON, "login", err)
	}
	norm := func(s string) string { return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(s)) }
	if *fp != "" {
		if norm(*fp) != norm(got) {
			return fail(asJSON, "login", fmt.Errorf("the server's certificate is %s, not %s: not logging in", got, *fp))
		}
	} else if old, err := client.Load(); err == nil && old.Server == *srv && norm(old.CertSHA256) == norm(got) {
		// the same server as before
	} else {
		fmt.Fprintf(os.Stderr, "The server's TLS certificate has sha256\n  %s\nIs this the one gaia server init printed? [y/N] ", got)
		line, _ := readLine()
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return fail(asJSON, "login", errors.New("certificate not confirmed: not logging in"))
		}
	}
	pw, err := readPassword(*pwFile, "Password: ", false)
	if err != nil {
		return fail(asJSON, "login", err)
	}
	cfg := client.Config{Server: *srv, CertSHA256: got, Username: *user}
	b, _, err := client.New(cfg).Do(ctx, "POST", "/api/v1/login", map[string]string{"username": *user, "password": pw})
	if err != nil {
		return fail(asJSON, "login", err)
	}
	var lr struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		User      struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(b, &lr); err != nil {
		return fail(asJSON, "login", err)
	}
	cfg.Token, cfg.ExpiresAt = lr.Token, lr.ExpiresAt
	if err := cfg.Save(); err != nil {
		return fail(asJSON, "login", err)
	}
	p, _ := client.ConfigPath()
	if asJSON {
		emit(map[string]any{"command": "login", "verdict": "OK", "user": *user, "role": lr.User.Role, "expires_at": lr.ExpiresAt, "config": p})
	} else {
		fmt.Printf("logged in to %s as %s (%s) until %s; saved in %s\n", *srv, *user, lr.User.Role, lr.ExpiresAt, p)
	}
	return 0
}

func readLine() (string, error) {
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return string(b), nil
			}
			b = append(b, one[0])
		}
		if err != nil {
			return string(b), err
		}
	}
}

// show prints a JSON answer: as is with -json, else an object as key: value lines and an array as a table.
func show(b []byte, asJSON bool) int {
	if asJSON || len(bytes.TrimSpace(b)) == 0 {
		os.Stdout.Write(b)
		return 0
	}
	t := bytes.TrimSpace(b)
	switch t[0] {
	case '[':
		var rows []json.RawMessage
		if err := json.Unmarshal(t, &rows); err != nil {
			os.Stdout.Write(b)
			return 0
		}
		if len(rows) == 0 {
			fmt.Println("(none)")
			return 0
		}
		cols := keys(rows[0])
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, strings.ToUpper(strings.Join(cols, "\t")))
		for _, r := range rows {
			var m map[string]json.RawMessage
			json.Unmarshal(r, &m)
			var cells []string
			for _, c := range cols {
				cells = append(cells, cell(m[c], 48))
			}
			fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		w.Flush()
	case '{':
		var m map[string]json.RawMessage
		if err := json.Unmarshal(t, &m); err != nil {
			os.Stdout.Write(b)
			return 0
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, k := range keys(t) {
			fmt.Fprintf(w, "%s:\t%s\n", k, cell(m[k], 0))
		}
		w.Flush()
	default:
		os.Stdout.Write(b)
	}
	return 0
}

// keys returns an object's member names in the order the server wrote them.
func keys(obj []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return out
		}
		out = append(out, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return out
		}
	}
	return out
}

func cell(v json.RawMessage, max int) string {
	var s string
	switch {
	case len(v) == 0 || string(v) == "null":
		s = "-"
	case v[0] == '"':
		json.Unmarshal(v, &s)
	default:
		var buf bytes.Buffer
		if json.Compact(&buf, v) == nil {
			s = buf.String()
		} else {
			s = string(v)
		}
	}
	if s == "" {
		s = "-"
	}
	if max > 0 && len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// cliStatus prints every VM's status (or one VM's, with all its reasons) and exits with the worst status's code:
// 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN (no VMs is UNKNOWN: nothing is proven).
func cliStatus(ctx context.Context, c *client.Client, args []string, asJSON bool) int {
	type vmStatus struct {
		VMID           int64    `json:"vm_id"`
		VM             string   `json:"vm"`
		Status         string   `json:"status"`
		ChangedAt      string   `json:"changed_at"`
		TheoreticalRPO *float64 `json:"theoretical_rpo_s"`
		ProvenRPO      *float64 `json:"proven_rpo_s"`
		ProvenRTO      *float64 `json:"proven_rto_s"`
		ProofAge       *float64 `json:"proof_age_s"`
		Reasons        []struct {
			Status string `json:"status"`
			Text   string `json:"text"`
		} `json:"reasons"`
		Notes []string `json:"notes"`
	}
	path := "/api/v1/status"
	if len(args) > 0 {
		path = "/api/v1/vms/" + url.PathEscape(args[0]) + "/status"
	}
	b, _, err := c.Do(ctx, "GET", path, nil)
	if err != nil {
		return fail(asJSON, "status", err)
	}
	var vs []vmStatus
	if len(args) > 0 {
		var v vmStatus
		err = json.Unmarshal(b, &v)
		vs = []vmStatus{v}
	} else {
		err = json.Unmarshal(b, &vs)
	}
	if err != nil {
		return fail(asJSON, "status", err)
	}
	worst := "OK"
	if len(vs) == 0 {
		worst = "UNKNOWN"
	}
	rank := map[string]int{"OK": 0, "WARNING": 1, "UNKNOWN": 2, "CRITICAL": 3}
	for _, v := range vs {
		if r, ok := rank[v.Status]; !ok || r > rank[worst] {
			worst = v.Status
			if !ok {
				worst = "UNKNOWN"
			}
		}
	}
	code := map[string]int{"OK": 0, "WARNING": 1, "CRITICAL": 2}[worst]
	if worst == "UNKNOWN" {
		code = 3
	}
	if asJSON {
		os.Stdout.Write(b)
		return code
	}
	d := func(s *float64) string {
		if s == nil {
			return "-"
		}
		return time.Duration(*s * float64(time.Second)).Round(time.Second).String()
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VM\tSTATUS\tSINCE\tTHEORETICAL RPO\tPROVEN RPO\tPROVEN RTO\tPROOF AGE\tREASON")
	for _, v := range vs {
		reason := "-"
		if len(v.Reasons) > 0 {
			reason = v.Reasons[0].Text
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.VM, v.Status, v.ChangedAt, d(v.TheoreticalRPO), d(v.ProvenRPO),
			d(v.ProvenRTO), d(v.ProofAge), reason)
	}
	tw.Flush()
	if len(args) > 0 && len(vs) == 1 {
		for _, r := range vs[0].Reasons[min(1, len(vs[0].Reasons)):] {
			fmt.Printf("  also %s: %s\n", r.Status, r.Text)
		}
		for _, n := range vs[0].Notes {
			fmt.Printf("  note: %s\n", n)
		}
	}
	return code
}

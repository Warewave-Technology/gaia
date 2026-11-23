package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
//	gaia login -server https://HOST:8443 -user NAME      password and a TOTP or recovery code (signin.go)
//	gaia login -server https://HOST:8443 -browser        approved in a signed-in browser (a security key)
//	gaia login -server https://HOST:8443 -sso            at the identity provider (device grant)
//	gaia logout | whoami | system | password | recovery-codes
//	gaia sessions list | rm ID                           every live session (admins); gaia users reset-factors ID
//	gaia tokens list | add name=N role=operator|auditor [expires_at=...] [allowed_addrs=[...]] | rm ID
//	gaia factors list | rm ID                            one's own second factors (enrol more in the web UI)
//	gaia RESOURCE list [key=value ...]                   query filters, e.g. vm_id=3 limit=20
//	gaia RESOURCE get ID
//	gaia RESOURCE add key=value ...                      a JSON body; values that parse as JSON are typed
//	gaia RESOURCE set ID key=value ...
//	gaia RESOURCE rm ID
//	gaia status [VM ID]                                  every VM's status (exit code: the worst status)
//	gaia vms disks ID | add-disk ID target=vda size_bytes=N | backup ID | test ID | status ID
//	gaia hosts scan ID | trust ID fingerprint=SHA256:... | check ID
//	gaia points disks ID | restore-check ID | forget ID reason="..."   (forget: a failed point only, admins)
//	gaia repositories verify ID mode=sampled|full | maintenance ID [force=true]
//	gaia evidence export ID [-o FILE]                    gaia audit verify          gaia state-backups run
//	gaia evidence export-all [-o FILE]                   the auditor's export: every bundle, the ledger, the keys, ...
//	gaia kit status | save | confirm | regenerate | show  the recovery kit (kit.go)
//	gaia channels add name=N kind=smtp|webhook smtp={...}|webhook={...} [categories=[...]] [daily_report=false] | test ID
//	gaia channels set ID enabled=false | categories='["status","jobs","integrity","security","migration"]' | daily_report=true
//	gaia ledger list | check                             gaia daily-reports list | get ID | run
//	gaia sources add name=N url=https://ESXI/sdk username=root [insecure=true] | inventory ID | check ID vm=NAME [template=T]
//	gaia migration-checks list [source_id=N] | get ID | report ID [-o FILE]   (ADR-003: read-only migration checks)
//	gaia api METHOD PATH [JSON]                          any API call
//
// -json anywhere prints the server's JSON instead of a table.

type resource struct {
	path  string
	verbs []string
}

var resources = map[string]resource{
	"users":            {"/api/v1/users", []string{"list", "get", "add", "set", "unlock", "reset-factors"}},
	"sessions":         {"/api/v1/sessions", []string{"list", "rm"}},
	"tokens":           {"/api/v1/api-tokens", []string{"list", "add", "rm"}},
	"factors":          {"/api/v1/me/factors", []string{"list", "rm"}},
	"hosts":            {"/api/v1/hosts", []string{"list", "get", "add", "set", "rm", "scan", "trust", "check"}},
	"vms":              {"/api/v1/vms", []string{"list", "get", "add", "set", "rm", "disks", "add-disk", "backup", "test", "status", "tags"}},
	"repositories":     {"/api/v1/repositories", []string{"list", "get", "add", "verify", "maintenance"}},
	"policies":         {"/api/v1/policies", []string{"list", "get", "add", "set", "rm"}},
	"jobs":             {"/api/v1/jobs", []string{"list", "get"}},
	"points":           {"/api/v1/points", []string{"list", "get", "disks", "restore-check", "forget"}},
	"verifications":    {"/api/v1/verifications", []string{"list", "get"}},
	"quarantine":       {"/api/v1/quarantined-contents", []string{"list"}},
	"tests":            {"/api/v1/restore-tests", []string{"list", "get"}},
	"evidence":         {"/api/v1/evidence", []string{"list", "get", "export", "export-all"}},
	"audit":            {"/api/v1/audit", []string{"list", "verify"}},
	"state-changes":    {"/api/v1/state-changes", []string{"list"}},
	"state-backups":    {"/api/v1/state-backups", []string{"list", "run"}},
	"channels":         {"/api/v1/notification-channels", []string{"list", "get", "add", "set", "test"}},
	"notifications":    {"/api/v1/notifications", []string{"list"}},
	"ledger":           {"/api/v1/ledger", []string{"list", "check"}},
	"daily-reports":    {"/api/v1/daily-reports", []string{"list", "get", "run"}},
	"sources":          {"/api/v1/migration-sources", []string{"list", "get", "add", "rm", "inventory", "check"}},
	"migration-checks": {"/api/v1/migration-checks", []string{"list", "get", "report"}},
}

// isCLICommand reports whether args[0] is handled by runCLI.
func isCLICommand(name string) bool {
	_, ok := resources[name]
	return ok || slices.Contains([]string{"login", "logout", "whoami", "system", "password", "recovery-codes", "api", "status", "kit"}, name)
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
	if cmd == "kit" {
		return cliKit(ctx, args[1:], asJSON)
	}
	cfg, err := client.Load()
	if err != nil {
		return fail(asJSON, cmd, err)
	}
	c := newClient(cfg)
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
	case "recovery-codes": // new recovery codes, shown once; the old ones stop working
		return call("POST", "/api/v1/me/recovery-codes", nil)
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
		fmt.Println("password changed; your other sessions ended")
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
		if cmd == "channels" {
			if err := channelSecrets(body); err != nil {
				return fail(asJSON, cmd, err)
			}
		}
		if cmd == "sources" {
			if err := sourcePassword(body); err != nil {
				return fail(asJSON, cmd, err)
			}
		}
		if cmd == "repositories" {
			if err := repoS3Secrets(body); err != nil {
				return fail(asJSON, cmd, err)
			}
			if err := repoKitPassphrase(ctx, c, body); err != nil {
				return fail(asJSON, cmd, err)
			}
		}
		return call("POST", res.path, body)
	}
	if verb == "verify" && cmd == "audit" {
		return call("GET", res.path+"/verify", nil)
	}
	if verb == "check" && cmd == "ledger" { // exit code: the ledger's verdict
		b, _, err := c.Do(ctx, "POST", res.path+"/verify", nil)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		show(b, asJSON)
		var lc struct {
			Verdict string `json:"verdict"`
		}
		json.Unmarshal(b, &lc)
		switch lc.Verdict {
		case "OK":
			return 0
		case "WARNING":
			return 1
		case "CRITICAL":
			return 2
		}
		return 3
	}
	if verb == "export-all" && cmd == "evidence" {
		return writeExport(ctx, c, "/api/v1/evidence-export", "gaia-evidence-export.tar.gz", rest, asJSON, cmd)
	}
	id, rest, err := needID()
	if err != nil {
		return fail(asJSON, cmd, err)
	}
	if cmd == "sources" && verb == "check" { // a migration check of one VM (body vm=NAME ...)
		body, err := keyValues(rest)
		if err != nil {
			return fail(asJSON, cmd, err)
		}
		return call("POST", res.path+"/"+id+"/checks", body)
	}
	switch verb {
	case "get":
		return call("GET", res.path+"/"+id, nil)
	case "scan", "check", "restore-check", "unlock":
		return call("POST", res.path+"/"+id+"/"+verb, nil)
	case "reset-factors": // the user enrols again at the next sign-in (ADR-008 §3)
		return call("POST", res.path+"/"+id+"/factors/reset", nil)
	case "backup":
		return call("POST", res.path+"/"+id+"/backups", nil)
	case "test":
		if cmd == "channels" {
			return call("POST", res.path+"/"+id+"/test", nil)
		}
		return call("POST", res.path+"/"+id+"/tests", nil)
	case "status":
		return call("GET", res.path+"/"+id+"/status", nil)
	case "tags": // gaia vms tags ID [key|key:value ...]: replaces the VM's tags (none: removes them)
		return call("PUT", res.path+"/"+id+"/tags", map[string]any{"tags": append([]string{}, rest...)})
	case "trust", "verify", "maintenance", "forget":
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
		return writeExport(ctx, c, res.path+"/"+id+"/export", "gaia-evidence-"+id+".tar.gz", rest, asJSON, cmd)
	case "report":
		return writeExport(ctx, c, res.path+"/"+id+"/report", "gaia-readiness-"+id+".tar.gz", rest, asJSON, cmd)
	case "inventory":
		return call("GET", res.path+"/"+id+"/inventory", nil)
	}
	return fail(asJSON, cmd, fmt.Errorf("verb %s", verb))
}

// writeExport saves a tar.gz the server sends (a bundle's or the auditor's export) to -o FILE or a default name.
func writeExport(ctx context.Context, c *client.Client, path, out string, rest []string, asJSON bool, cmd string) int {
	for i := 0; i+1 < len(rest); i++ {
		if rest[i] == "-o" {
			out = rest[i+1]
		}
	}
	b, _, err := c.Do(ctx, "GET", path, nil)
	if err != nil {
		return fail(asJSON, cmd, err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return fail(asJSON, cmd, err)
	}
	if asJSON {
		emit(map[string]any{"command": cmd, "file": out, "bytes": len(b)})
	} else {
		fmt.Printf("wrote %s (%d bytes); verify it with the README inside\n", out, len(b))
	}
	return 0
}

// sourcePassword takes a migration source's password from GAIA_VSPHERE_PASSWORD, or asks for it; never from argv.
func sourcePassword(body map[string]any) error {
	if _, ok := body["password"]; ok {
		return errors.New("do not put the source's password on the command line: set GAIA_VSPHERE_PASSWORD, or type it when asked")
	}
	if v := os.Getenv("GAIA_VSPHERE_PASSWORD"); v != "" {
		body["password"] = v
		return nil
	}
	pw, err := readPassword("", "Password of the vSphere user: ", false)
	if err != nil {
		return err
	}
	body["password"] = pw
	return nil
}

// channelSecrets takes an SMTP password or a webhook secret from the environment (GAIA_SMTP_PASSWORD,
// GAIA_WEBHOOK_SECRET), never from argv.
func channelSecrets(body map[string]any) error {
	if _, ok := body["smtp_password"]; ok {
		return errors.New("do not put the SMTP password on the command line: set GAIA_SMTP_PASSWORD")
	}
	if _, ok := body["webhook_secret"]; ok {
		return errors.New("do not put the webhook secret on the command line: set GAIA_WEBHOOK_SECRET (or leave it out for a new one)")
	}
	if v := os.Getenv("GAIA_SMTP_PASSWORD"); v != "" {
		body["smtp_password"] = v
	}
	if v := os.Getenv("GAIA_WEBHOOK_SECRET"); v != "" {
		body["webhook_secret"] = v
	}
	// ca_file=PATH: the CA certificates (PEM) the mail server's or webhook's certificate is checked against.
	if f, ok := body["ca_file"].(string); ok {
		delete(body, "ca_file")
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		set := false
		for _, k := range []string{"smtp", "webhook"} {
			if m, ok := body[k].(map[string]any); ok {
				m["ca_pem"], set = string(b), true
			}
		}
		if !set {
			return errors.New("ca_file needs smtp={...} or webhook={...}")
		}
	}
	return nil
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
			Code   string `json:"code"`
			Text   string `json:"text"`
		} `json:"reasons"`
		Notes []string `json:"notes"`
	}
	if len(args) > 0 && (args[0] == "check" || args[0] == "recompute") {
		return cliStatusTable(ctx, c, args[0], asJSON)
	}
	var vs []vmStatus
	var b []byte
	if len(args) > 0 {
		var err error
		if b, _, err = c.Do(ctx, "GET", "/api/v1/vms/"+url.PathEscape(args[0])+"/status", nil); err != nil {
			return fail(asJSON, "status", err)
		}
		var v vmStatus
		if err := json.Unmarshal(b, &v); err != nil {
			return fail(asJSON, "status", err)
		}
		vs = []vmStatus{v}
	} else {
		// Every page: the exit code is the worst status of every VM, never of the first thousand.
		var all []json.RawMessage
		for after := int64(0); ; {
			page, _, err := c.Do(ctx, "GET", fmt.Sprintf("/api/v1/status?after=%d&limit=1000", after), nil)
			if err != nil {
				return fail(asJSON, "status", err)
			}
			var raw []json.RawMessage
			var got []vmStatus
			if err := json.Unmarshal(page, &raw); err != nil {
				return fail(asJSON, "status", err)
			}
			if err := json.Unmarshal(page, &got); err != nil {
				return fail(asJSON, "status", err)
			}
			all, vs = append(all, raw...), append(vs, got...)
			if len(got) < 1000 {
				break
			}
			after = got[len(got)-1].VMID
		}
		var err error
		if b, err = json.Marshal(all); err != nil {
			return fail(asJSON, "status", err)
		}
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
	fmt.Fprintln(tw, "VM\tSTATUS\tSINCE\tTHEORETICAL RPO\tPROVEN RPO\tPROVEN RTO\tPROOF AGE\tCODE\tREASON")
	for _, v := range vs {
		reason, code := "-", "-"
		if len(v.Reasons) > 0 {
			reason, code = v.Reasons[0].Text, v.Reasons[0].Code
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.VM, v.Status, v.ChangedAt, d(v.TheoreticalRPO), d(v.ProvenRPO),
			d(v.ProvenRTO), d(v.ProofAge), code, reason)
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
	// The recovery kit is not a VM's status, but an operator looking here must see when the server cannot be rebuilt.
	if kb, _, err := c.Do(ctx, "GET", "/api/v1/recovery-kit", nil); err != nil {
		fmt.Printf("\nrecovery kit: UNKNOWN (%v)\n", err)
	} else {
		var info struct {
			Summary kitSummary `json:"summary"`
		}
		if err := json.Unmarshal(kb, &info); err != nil || info.Summary.State != "confirmed" {
			fmt.Printf("\nWARNING recovery kit: %s\n", info.Summary.Text)
		}
	}
	// So are the evidence ledger and whether anybody is told (T19).
	if sb, _, err := c.Do(ctx, "GET", "/api/v1/system", nil); err != nil {
		fmt.Printf("ledger, notifications: UNKNOWN (%v)\n", err)
	} else {
		var info struct {
			Ledger struct {
				Verdict string `json:"verdict"`
				Text    string `json:"text"`
			} `json:"ledger"`
			Notifications struct {
				State string `json:"state"`
				Text  string `json:"text"`
			} `json:"notifications"`
		}
		if err := json.Unmarshal(sb, &info); err != nil {
			fmt.Printf("ledger, notifications: UNKNOWN (%v)\n", err)
		} else {
			if info.Ledger.Verdict != "OK" {
				fmt.Printf("%s evidence ledger: %s\n", info.Ledger.Verdict, info.Ledger.Text)
			}
			if info.Notifications.State != "OK" {
				fmt.Printf("%s notifications: %s\n", info.Notifications.State, info.Notifications.Text)
			}
		}
	}
	return code
}

// cliStatusTable is `gaia status check` (the status table against the status model, exit 0 OK / 2 when a VM differs)
// and `gaia status recompute` (every VM's status now; admins).
func cliStatusTable(ctx context.Context, c *client.Client, what string, asJSON bool) int {
	method, path := "GET", "/api/v1/status/check"
	if what == "recompute" {
		method, path = "POST", "/api/v1/status/recompute"
	}
	b, _, err := c.Do(ctx, method, path, nil)
	if err != nil {
		return fail(asJSON, "status "+what, err)
	}
	var r struct {
		VMs     int     `json:"vms"`
		Equal   int     `json:"equal"`
		Due     int     `json:"due"`
		Changed int     `json:"changed"`
		Errors  int     `json:"errors"`
		Seconds float64 `json:"seconds"`
		Verdict string  `json:"verdict"`
		Differ  []struct {
			VMID   int64  `json:"vm_id"`
			Stored string `json:"stored"`
			Model  string `json:"model"`
		} `json:"differ"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return fail(asJSON, "status "+what, err)
	}
	code := 0
	if what == "check" && r.Verdict != "OK" {
		code = 2
	}
	if what == "recompute" && r.Errors > 0 {
		code = 3
	}
	if asJSON {
		os.Stdout.Write(b)
		return code
	}
	if what == "recompute" {
		fmt.Printf("recomputed %d VMs in %.3f s: %d changed status, %d errors\n", r.VMs, r.Seconds, r.Changed, r.Errors)
		return code
	}
	fmt.Printf("%s: %d VMs, %d equal to the status model, %d due at the next tick, %d differ (%.3f s)\n", r.Verdict, r.VMs, r.Equal, r.Due, len(r.Differ), r.Seconds)
	for _, d := range r.Differ {
		fmt.Printf("  VM %d\n    stored %s\n    model  %s\n", d.VMID, d.Stored, d.Model)
	}
	return code
}

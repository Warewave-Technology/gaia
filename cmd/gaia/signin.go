package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/client"
)

// Signing in from the CLI (ADR-008 §9): the same sign-in as the browser, and a session with the same limits.
//
//	gaia login -server URL -user NAME   the password, then a code from the authenticator app or a recovery code; at the
//	                                    first sign-in the app is enrolled here (an admin enrols a security key in a
//	                                    browser first: its factor cannot be answered in a terminal)
//	gaia login -server URL -browser     a code to approve in a browser signed in as the user (any factor, any role)
//	gaia login -server URL -sso         at the identity provider, with the device authorization grant (RFC 8628)
//
// An action that needs a step-up asks for a code from the app, or, for a security key or a single sign-on user, for
// an approval in the browser.

// signinStep is the server's answer to a sign-in step (server.SigninStep).
type signinStep struct {
	Stage          string   `json:"stage"`
	SigninToken    string   `json:"signin_token"`
	Username       string   `json:"username"`
	Factors        []string `json:"factors"`
	Enrol          []string `json:"enrol"`
	PasswordChange string   `json:"password_change"`
	Token          string   `json:"token"`
	AbsoluteAt     string   `json:"absolute_at"`
	IdleTimeoutS   int      `json:"idle_timeout_s"`
	NewDevice      bool     `json:"new_device"`
	RecoveryCodes  []string `json:"recovery_codes"`
	User           struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

type challenge struct {
	Handle                  string `json:"handle"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresAt               string `json:"expires_at"`
	IntervalS               int    `json:"interval_s"`
}

func post(ctx context.Context, c *client.Client, path string, in, out any) error {
	b, _, err := c.Do(ctx, "POST", path, in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func cliLogin(ctx context.Context, args []string, asJSON bool) int {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	srv := fs.String("server", "", "https://HOST:PORT of gaia server")
	user := fs.String("user", "", "username (a local account)")
	fp := fs.String("fingerprint", "", "the server certificate's sha256 (from gaia server init); asked for when not given")
	pwFile := fs.String("password-file", "", "file holding the password (default: ask)")
	browser := fs.Bool("browser", false, "approve the sign-in in a browser signed in as you (for a security key)")
	sso := fs.Bool("sso", false, "sign in at the identity provider (single sign-on)")
	fs.Parse(args)
	if *srv == "" || (*user == "" && !*browser && !*sso) {
		return fail(asJSON, "login", errors.New("-server, and -user (or -browser, or -sso), are required"))
	}
	*srv = strings.TrimSuffix(*srv, "/")
	got, err := client.ServerFingerprint(ctx, *srv)
	if err != nil {
		return fail(asJSON, "login", err)
	}
	norm := func(s string) string { return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(s)) }
	old, oerr := client.Load()
	if *fp != "" {
		if norm(*fp) != norm(got) {
			return fail(asJSON, "login", fmt.Errorf("the server's certificate is %s, not %s: not signing in", got, *fp))
		}
	} else if oerr == nil && old.Server == *srv && norm(old.CertSHA256) == norm(got) {
		// the same server as before
	} else {
		fmt.Fprintf(os.Stderr, "The server's TLS certificate has sha256\n  %s\nIs this the one gaia server init printed? [y/N] ", got)
		line, _ := readLine()
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return fail(asJSON, "login", errors.New("certificate not confirmed: not signing in"))
		}
	}
	dev := old.DeviceID
	if dev == "" {
		b := make([]byte, 24)
		rand.Read(b)
		dev = "cli_" + hex.EncodeToString(b)
	}
	cfg := client.Config{Server: *srv, CertSHA256: got, Username: *user, DeviceID: dev}
	c := client.New(cfg)
	var st signinStep
	switch {
	case *sso:
		err = waitApproval(ctx, c, "/api/v1/oidc/device", "/api/v1/oidc/device/token", dev, "Sign in at the identity provider", &st)
	case *browser:
		err = waitApproval(ctx, c, "/api/v1/login/cli", "/api/v1/login/cli/token", dev, "Approve this sign-in in a browser signed in as you", &st)
	default:
		err = passwordSignin(ctx, c, *user, *pwFile, dev, &st)
	}
	if err != nil {
		return fail(asJSON, "login", err)
	}
	cfg.Username, cfg.Token, cfg.ExpiresAt = st.User.Username, st.Token, st.AbsoluteAt
	if err := cfg.Save(); err != nil {
		return fail(asJSON, "login", err)
	}
	p, _ := client.ConfigPath()
	if len(st.RecoveryCodes) > 0 {
		fmt.Fprintf(os.Stderr, "\nYour recovery codes, shown once. Keep them where you keep passwords; each signs you in once if you lose the app:\n")
		for _, rc := range st.RecoveryCodes {
			fmt.Fprintln(os.Stderr, "  "+rc)
		}
		fmt.Fprintln(os.Stderr)
	}
	idle := time.Duration(st.IdleTimeoutS) * time.Second
	if asJSON {
		emit(map[string]any{"command": "login", "verdict": "OK", "user": st.User.Username, "role": st.User.Role, "expires_at": st.AbsoluteAt,
			"idle_timeout_s": st.IdleTimeoutS, "new_device": st.NewDevice, "config": p})
	} else {
		fmt.Printf("signed in to %s as %s (%s) until %s at the latest, or after %s without a command; saved in %s\n", *srv, st.User.Username,
			st.User.Role, st.AbsoluteAt, idle, p)
	}
	return 0
}

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

// passwordSignin goes through the steps of a local sign-in in the terminal.
func passwordSignin(ctx context.Context, c *client.Client, user, pwFile, dev string, st *signinStep) error {
	pw, err := readPassword(pwFile, "Password: ", false)
	if err != nil {
		return err
	}
	if err := post(ctx, c, "/api/v1/login", map[string]any{"username": user, "password": pw, "device_id": dev}, st); err != nil {
		return err
	}
	for {
		switch st.Stage {
		case "done":
			return nil
		case "second_factor":
			if !contains(st.Factors, "totp") && !contains(st.Factors, "recovery_code") {
				return errors.New("your second factor is a security key, which a terminal cannot use: run gaia login -browser")
			}
			code, err := readPassword("", "Code from your authenticator app (or a recovery code): ", false)
			if err != nil {
				return err
			}
			method := "recovery_code"
			if sixDigits.MatchString(strings.ReplaceAll(code, " ", "")) {
				method = "totp"
			}
			if err := post(ctx, c, "/api/v1/login/factor", map[string]any{"signin_token": st.SigninToken, "method": method, "code": code}, st); err != nil {
				return err
			}
		case "enrol":
			if !contains(st.Enrol, "totp") {
				return errors.New("an admin's second factor is a security key: sign in once in a browser to enrol it, then use gaia login -browser")
			}
			var ch struct {
				TOTPSecret string `json:"totp_secret"`
				TOTPURI    string `json:"totp_uri"`
			}
			if err := post(ctx, c, "/api/v1/login/enrol", map[string]any{"signin_token": st.SigninToken, "kind": "totp"}, &ch); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "A second factor is required (ADR-008). Add this account to your authenticator app:\n  key  %s\n  or   %s\n",
				ch.TOTPSecret, ch.TOTPURI)
			code, err := readPassword("", "The code the app shows now: ", false)
			if err != nil {
				return err
			}
			if err := post(ctx, c, "/api/v1/login/enrol/finish", map[string]any{"signin_token": st.SigninToken, "kind": "totp", "code": code,
				"name": "authenticator app (enrolled in the CLI)"}, st); err != nil {
				return err
			}
		case "change_password":
			fmt.Fprintf(os.Stderr, "Your password must be changed first (%s).\n", st.PasswordChange)
			np, err := readPassword("", "New password (at least 15 characters): ", true)
			if err != nil {
				return err
			}
			if err := post(ctx, c, "/api/v1/login/password", map[string]any{"signin_token": st.SigninToken, "new_password": np}, st); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the server asks for %q, which this CLI does not know", st.Stage)
		}
	}
}

// waitApproval starts a sign-in that the user finishes in a browser, and polls until it is done.
func waitApproval(ctx context.Context, c *client.Client, start, poll, dev, what string, st *signinStep) error {
	var ch challenge
	if err := post(ctx, c, start, map[string]any{"device_id": dev}, &ch); err != nil {
		return err
	}
	u := ch.VerificationURIComplete
	if u == "" {
		u = ch.VerificationURI
	}
	fmt.Fprintf(os.Stderr, "%s:\n  open  %s\n  code  %s\nWaiting until %s ...\n", what, u, ch.UserCode, ch.ExpiresAt)
	every := time.Duration(max(ch.IntervalS, 2)) * time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
		if err := post(ctx, c, poll, map[string]any{"handle": ch.Handle}, st); err != nil {
			return err
		}
		if st.Stage == "done" {
			return nil
		}
	}
}

// newClient is a client of the saved login that makes step-ups when an action needs one.
func newClient(cfg client.Config) *client.Client {
	c := client.New(cfg)
	c.StepUp = func(ctx context.Context, method, path string) (map[string]string, error) {
		var ch struct {
			ID         string   `json:"id"`
			Factors    []string `json:"factors"`
			Redirect   string   `json:"redirect_url"`
			ApproveURL string   `json:"approve_url"`
			ExpiresAt  string   `json:"expires_at"`
		}
		b, _, err := c.Do(ctx, "POST", "/api/v1/step-up", map[string]string{"method": method, "path": path})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &ch); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "%s %s is a destructive or security action: it needs your second factor again (ADR-008 §4).\n", method, path)
		if contains(ch.Factors, "totp") {
			code, err := readPassword("", "Code from your authenticator app: ", false)
			if err != nil {
				return nil, err
			}
			proof, _ := json.Marshal(map[string]string{"method": "totp", "code": code})
			return map[string]string{"X-Gaia-Step-Up": ch.ID, "X-Gaia-Step-Up-Proof": base64.RawURLEncoding.EncodeToString(proof)}, nil
		}
		u := ch.ApproveURL
		if ch.Redirect != "" {
			u = ch.Redirect
		}
		fmt.Fprintf(os.Stderr, "Approve it in a browser before %s:\n  %s\nThen press Enter. ", ch.ExpiresAt, u)
		if _, err := readLine(); err != nil {
			return nil, err
		}
		return map[string]string{"X-Gaia-Step-Up": ch.ID}, nil
	}
	return c
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

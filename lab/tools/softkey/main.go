// Command softkey is a lab fixture for the upgrade path (lab/lab-vm/t17-drill.sh upgrade): it signs a drill's admin in
// with a WebAuthn key in software, which a terminal cannot do with a real key (ADR-008 §3), and makes the step-ups
// of the drill's own calls. The key is kept in a file (0600) next to the drill's other secrets. Not part of the product.
//
//	softkey login -cli-config CLI.json -server URL -fingerprint FP -user NAME -password-file F -key KEY.json
//	        signs in (enrolling the key at the first sign-in after the upgrade), and writes the session into the
//	        gaia command's config, so `gaia` works as after gaia login
//	softkey call -cli-config CLI.json -key KEY.json [-o FILE] METHOD PATH [JSON]
//	        a call with that session, with a step-up made with the key when the server asks for one
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/client"
	"github.com/Warewave-Technology/gaia/lab/tools/webauthnsoft"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: softkey login|call ...")
		os.Exit(3)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = login(os.Args[2:])
	case "call":
		err = call(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "softkey:", err)
		os.Exit(3)
	}
}

func post(ctx context.Context, c *client.Client, path string, in, out any) error {
	b, _, err := c.Do(ctx, "POST", path, in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	cfgPath := fs.String("cli-config", "", "the gaia command's config to write")
	srv := fs.String("server", "", "https://HOST:PORT")
	fp := fs.String("fingerprint", "", "the server certificate's sha256")
	user := fs.String("user", "admin", "the user")
	pwFile := fs.String("password-file", "", "the user's password")
	keyFile := fs.String("key", "", "the key's file (made at the first sign-in)")
	fs.Parse(args)
	ctx := context.Background()
	pw, err := os.ReadFile(*pwFile)
	if err != nil {
		return err
	}
	os.Setenv("GAIA_CLI_CONFIG", *cfgPath)
	old, _ := client.Load()
	dev := old.DeviceID
	if dev == "" {
		b := make([]byte, 24)
		rand.Read(b)
		dev = "cli_" + hex.EncodeToString(b)
	}
	cfg := client.Config{Server: strings.TrimSuffix(*srv, "/"), CertSHA256: *fp, Username: *user, DeviceID: dev}
	c := client.New(cfg)
	var origin struct {
		PublicURL string `json:"public_url"`
	}
	var st struct {
		Stage       string          `json:"stage"`
		SigninToken string          `json:"signin_token"`
		Token       string          `json:"token"`
		AbsoluteAt  string          `json:"absolute_at"`
		WebAuthn    json.RawMessage `json:"webauthn"`
	}
	if err := post(ctx, c, "/api/v1/login", map[string]any{"username": *user, "password": strings.TrimRight(string(pw), "\r\n"), "device_id": dev}, &st); err != nil {
		return err
	}
	key, err := webauthnsoft.Load(*keyFile)
	if err != nil {
		return err
	}
	for st.Stage != "done" {
		switch st.Stage {
		case "enrol":
			var ch struct {
				WebAuthn json.RawMessage `json:"webauthn"`
			}
			if err := post(ctx, c, "/api/v1/login/enrol", map[string]string{"signin_token": st.SigninToken, "kind": "webauthn"}, &ch); err != nil {
				return err
			}
			// The relying party is the server's public URL, which the creation options name (rp.id) only by host.
			var rp struct {
				PublicKey struct {
					RP struct{ ID string } `json:"rp"`
				} `json:"publicKey"`
			}
			json.Unmarshal(ch.WebAuthn, &rp)
			origin.PublicURL = publicURL(cfg.Server, rp.PublicKey.RP.ID)
			key = webauthnsoft.New(origin.PublicURL)
			if err := key.Save(*keyFile); err != nil {
				return err
			}
			if err := post(ctx, c, "/api/v1/login/enrol/finish", map[string]any{"signin_token": st.SigninToken, "kind": "webauthn", "name": "drill key (lab fixture)",
				"webauthn": key.Create(ch.WebAuthn)}, &st); err != nil {
				return err
			}
		case "second_factor":
			if key == nil {
				return errors.New("the user has a factor but no key file: reset its factors")
			}
			if err := post(ctx, c, "/api/v1/login/factor", map[string]any{"signin_token": st.SigninToken, "method": "webauthn", "webauthn": key.Get(st.WebAuthn)}, &st); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the sign-in stopped at %q", st.Stage)
		}
	}
	cfg.Token, cfg.ExpiresAt = st.Token, st.AbsoluteAt
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("signed in as %s until %s (%s)\n", *user, st.AbsoluteAt, time.Now().UTC().Format(time.RFC3339))
	return nil
}

// publicURL is the origin a browser would have: the server's scheme and port with the relying party's host name.
func publicURL(server, rpID string) string {
	rest := strings.TrimPrefix(server, "https://")
	port := ""
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[i:], "]") {
		port = rest[i:]
	}
	return "https://" + rpID + port
}

func call(args []string) error {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	cfgPath := fs.String("cli-config", "", "the gaia command's config (softkey login wrote it)")
	keyFile := fs.String("key", "", "the key's file")
	outFile := fs.String("o", "", "write the answer here (default: stdout)")
	fs.Parse(args)
	if fs.NArg() < 2 {
		return errors.New("usage: softkey call METHOD PATH [JSON]")
	}
	os.Setenv("GAIA_CLI_CONFIG", *cfgPath)
	cfg, err := client.Load()
	if err != nil {
		return err
	}
	key, err := webauthnsoft.Load(*keyFile)
	if err != nil || key == nil {
		return fmt.Errorf("key %s: %v", *keyFile, err)
	}
	c := client.New(cfg)
	ctx := context.Background()
	c.StepUp = func(ctx context.Context, method, path string) (map[string]string, error) {
		var ch struct {
			ID       string          `json:"id"`
			WebAuthn json.RawMessage `json:"webauthn"`
		}
		if err := post(ctx, c, "/api/v1/step-up", map[string]string{"method": method, "path": path}, &ch); err != nil {
			return nil, err
		}
		proof, _ := json.Marshal(map[string]any{"method": "webauthn", "webauthn": key.Get(ch.WebAuthn)})
		return map[string]string{"X-Gaia-Step-Up": ch.ID, "X-Gaia-Step-Up-Proof": base64.RawURLEncoding.EncodeToString(proof)}, nil
	}
	var body any
	if fs.NArg() > 2 {
		body = []byte(fs.Arg(2))
	}
	b, _, err := c.Do(ctx, strings.ToUpper(fs.Arg(0)), fs.Arg(1), body)
	if err != nil {
		return err
	}
	if *outFile != "" {
		return os.WriteFile(*outFile, b, 0o644)
	}
	_, err = os.Stdout.Write(bytes.TrimRight(b, "\n"))
	fmt.Println()
	return err
}

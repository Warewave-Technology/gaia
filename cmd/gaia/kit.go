package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/client"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/server"
)

// The recovery kit (ADR-007 B5).
//
//	gaia kit status                        whether the kit can rebuild the server, and every kit written
//	gaia kit save [-o FILE]                download the newest kit here, open it with the passphrase, confirm it
//	gaia kit confirm -kit FILE             confirm a copy made another way (scp), after opening it
//	gaia kit regenerate [-new-passphrase]  write a new kit (asks the current passphrase)
//	gaia kit show -kit FILE                open a kit here and show what it holds, without the secrets (no login)

type kitSummary struct {
	State      string `json:"state"`
	Generation int64  `json:"generation"`
	Text       string `json:"text"`
}

func cliKit(ctx context.Context, args []string, asJSON bool) int {
	if len(args) == 0 {
		return fail(asJSON, "kit", errors.New("usage: gaia kit status | save [-o FILE] | confirm -kit FILE | regenerate [-new-passphrase] | show -kit FILE"))
	}
	verb, rest := args[0], args[1:]
	fs := flag.NewFlagSet("kit "+verb, flag.ExitOnError)
	out := fs.String("o", "", "file to write (save; default gaia-recovery-kit-<generation>.age here)")
	kitPath := fs.String("kit", "", "the kit file (confirm, show)")
	passFile := fs.String("passphrase-file", "", "file holding the kit's passphrase (default: $GAIA_KIT_PASSPHRASE, or ask)")
	newPass := fs.Bool("new-passphrase", false, "regenerate: encrypt the new kit with a new passphrase")
	fs.Parse(rest)
	if verb == "show" {
		return kitShow(*kitPath, *passFile, asJSON)
	}
	cfg, err := client.Load()
	if err != nil {
		return fail(asJSON, "kit", err)
	}
	c := client.New(cfg)
	switch verb {
	case "status":
		b, _, err := c.Do(ctx, "GET", "/api/v1/recovery-kit", nil)
		if err != nil {
			return fail(asJSON, "kit status", err)
		}
		var info struct {
			Summary kitSummary `json:"summary"`
		}
		if err := json.Unmarshal(b, &info); err != nil {
			return fail(asJSON, "kit status", err)
		}
		code := 0
		if info.Summary.State != "confirmed" {
			code = 1 // WARNING: the server cannot be rebuilt from what is stored off it
		}
		if asJSON {
			os.Stdout.Write(b)
			return code
		}
		fmt.Printf("%s: %s\n\n", strings.ToUpper(info.Summary.State), info.Summary.Text)
		show(b, false)
		return code
	case "save":
		b, h, err := c.Do(ctx, "GET", "/api/v1/recovery-kit/file", nil)
		if err != nil {
			return fail(asJSON, "kit save", err)
		}
		gen := h.Get("X-Gaia-Kit-Generation")
		if *out == "" {
			*out = "gaia-recovery-kit-" + gen + ".age"
		}
		if err := writeNew(*out, b); err != nil {
			return fail(asJSON, "kit save", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (recovery kit %s)\n", *out, gen)
		return kitConfirm(ctx, c, *out, b, *passFile, asJSON)
	case "confirm":
		if *kitPath == "" {
			return fail(asJSON, "kit confirm", errors.New("-kit FILE is required"))
		}
		b, err := os.ReadFile(*kitPath)
		if err != nil {
			return fail(asJSON, "kit confirm", err)
		}
		return kitConfirm(ctx, c, *kitPath, b, *passFile, asJSON)
	case "regenerate":
		first, err := noKitYet(ctx, c)
		if err != nil {
			return fail(asJSON, "kit regenerate", err)
		}
		prompt := "Current passphrase of the recovery kit: "
		if first {
			prompt = fmt.Sprintf("There is no recovery kit yet. Passphrase for it (at least %d characters; Gaia does not store it): ", recoverykit.MinPassphrase)
		}
		cur, err := readKitPassphrase(*passFile, prompt, first)
		if err != nil {
			return fail(asJSON, "kit regenerate", err)
		}
		body := server.KitPassphrase{Passphrase: cur}
		if *newPass {
			if body.NewPassphrase, err = readSecret("", "GAIA_KIT_NEW_PASSPHRASE", fmt.Sprintf("New passphrase (at least %d characters): ", recoverykit.MinPassphrase), true); err != nil {
				return fail(asJSON, "kit regenerate", err)
			}
		}
		b, _, err := c.Do(ctx, "POST", "/api/v1/recovery-kit", body)
		if err != nil {
			return fail(asJSON, "kit regenerate", err)
		}
		if !asJSON {
			fmt.Println("A new recovery kit was written. Save it off this server now: gaia kit save")
		}
		return show(b, asJSON)
	}
	return fail(asJSON, "kit", fmt.Errorf("verb %s: status, save, confirm, regenerate or show", verb))
}

// writeNew writes a file that must not exist yet, readable only by its owner.
func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// kitConfirm opens a local copy of the kit with the passphrase and tells the server which kit it is.
func kitConfirm(ctx context.Context, c *client.Client, path string, b []byte, passFile string, asJSON bool) int {
	pass, err := readKitPassphrase(passFile, "Passphrase of the recovery kit: ", false)
	if err != nil {
		return fail(asJSON, "kit confirm", err)
	}
	k, err := recoverykit.Open(b, pass)
	if err != nil {
		return fail(asJSON, "kit confirm", fmt.Errorf("%s: %w", path, err))
	}
	rb, _, err := c.Do(ctx, "POST", "/api/v1/recovery-kit/confirm", server.KitConfirm{Generation: k.Generation, SHA256: recoverykit.SHA256(b)})
	if err != nil {
		return fail(asJSON, "kit confirm", err)
	}
	if asJSON {
		os.Stdout.Write(rb)
		return 0
	}
	fmt.Printf(`Recovery kit %d opens with the passphrase and is the server's newest: confirmed.
Keep %s off the server (a password manager, an encrypted vault, a USB stick in a safe) and delete any other copy
on the server itself. Keep the passphrase separately.
`, k.Generation, path)
	return 0
}

// kitShow opens a kit locally and prints what it holds, without its secrets.
func kitShow(path, passFile string, asJSON bool) int {
	if path == "" {
		return fail(asJSON, "kit show", errors.New("-kit FILE is required"))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fail(asJSON, "kit show", err)
	}
	pass, err := readKitPassphrase(passFile, "Passphrase of the recovery kit: ", false)
	if err != nil {
		return fail(asJSON, "kit show", err)
	}
	k, err := recoverykit.Open(b, pass)
	if err != nil {
		return fail(asJSON, "kit show", err)
	}
	type repo struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Location string `json:"location"`
	}
	v := struct {
		Generation      int64  `json:"generation"`
		CreatedAt       string `json:"created_at"`
		Reason          string `json:"reason"`
		Hostname        string `json:"hostname"`
		GaiaVersion     string `json:"gaia_version"`
		EvidenceKeyID   string `json:"evidence_key_id"`
		SSHPublicKey    string `json:"ssh_public_key"`
		StateRepository string `json:"state_repository"`
		Repositories    []repo `json:"repositories"`
		SHA256          string `json:"sha256"`
	}{k.Generation, k.CreatedAt, k.Reason, k.Hostname, k.GaiaVersion, k.EvidenceKeyID, k.SSHPub, k.StateRepository, []repo{}, recoverykit.SHA256(b)}
	for _, r := range k.Repositories {
		v.Repositories = append(v.Repositories, repo{r.Name, r.Kind, r.Location})
	}
	if asJSON {
		emit(map[string]any{"command": "kit show", "verdict": "OK", "kit": v})
		return 0
	}
	fmt.Printf("recovery kit %d, written %s on %s (%s)\nevidence key  %s\nstate backups in repository %q\n", k.Generation, k.CreatedAt, k.Hostname, k.Reason,
		k.EvidenceKeyID, k.StateRepository)
	for _, r := range v.Repositories {
		fmt.Printf("repository    %-12s %s %s\n", r.Name, r.Kind, r.Location)
	}
	return 0
}

// noKitYet reports whether the server has never written a recovery kit (an installation from before schema 5).
func noKitYet(ctx context.Context, c *client.Client) (bool, error) {
	b, _, err := c.Do(ctx, "GET", "/api/v1/recovery-kit", nil)
	if err != nil {
		return false, err
	}
	var info struct {
		Summary kitSummary `json:"summary"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return false, err
	}
	return info.Summary.Generation == 0, nil
}

// repoKitPassphrase asks for the kit passphrase when a repository is added: twice if it sets the first kit.
func repoKitPassphrase(ctx context.Context, c *client.Client, body map[string]any) error {
	if _, ok := body["kit_passphrase"]; ok {
		return errors.New("do not put the kit passphrase on the command line: leave it out and type it when asked")
	}
	first, err := noKitYet(ctx, c)
	if err != nil {
		return err
	}
	prompt := "Passphrase of the recovery kit (a new repository writes a new kit): "
	if first {
		prompt = fmt.Sprintf("There is no recovery kit yet. Passphrase for it (at least %d characters): ", recoverykit.MinPassphrase)
	}
	pass, err := readKitPassphrase("", prompt, first)
	if err != nil {
		return err
	}
	body["kit_passphrase"] = pass
	return nil
}

func cmdServerRestoreState(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("server restore-state", flag.ExitOnError)
	kitPath := fs.String("kit", "", "the recovery kit file (required)")
	config := fs.String("config", "/etc/gaia/server.json", "config file to create")
	dataDir := fs.String("data-dir", "", "data directory, empty or missing (default: the lost server's)")
	backup := fs.String("state-backup", "", "the state backup to restore: archive ID or state backup ID (default: the newest)")
	names := fs.String("hostname", "", "comma-separated names for a NEW TLS certificate (default: keep the lost server's certificate)")
	passFile := fs.String("passphrase-file", "", "file holding the kit's passphrase (default: $GAIA_KIT_PASSPHRASE, or ask)")
	asJSON := fs.Bool("json", false, "print JSON")
	locs := map[string]string{}
	fs.Func("repository", "NAME=PATH: the repository is reachable at PATH now (repeat for each moved repository)", func(v string) error {
		n, p, ok := strings.Cut(v, "=")
		if !ok || n == "" || p == "" {
			return errors.New("want NAME=PATH")
		}
		locs[n] = p
		return nil
	})
	fs.Parse(args)
	if *kitPath == "" {
		return fail(*asJSON, "server restore-state", errors.New("-kit FILE is required"))
	}
	kit, err := os.ReadFile(*kitPath)
	if err != nil {
		return fail(*asJSON, "server restore-state", err)
	}
	pass, err := readKitPassphrase(*passFile, "Passphrase of the recovery kit: ", false)
	if err != nil {
		return fail(*asJSON, "server restore-state", err)
	}
	var hosts []string
	if *names != "" {
		hosts = strings.Split(*names, ",")
	}
	r, err := server.RestoreState(ctx, server.RestoreOptions{ConfigPath: *config, DataDir: *dataDir, Kit: kit, Passphrase: pass, Locations: locs,
		StateBackup: *backup, Hostnames: hosts})
	if err != nil {
		return fail(*asJSON, "server restore-state", err)
	}
	code := map[string]int{"OK": 0, "WARNING": 1, "CRITICAL": 2}[r.Verdict]
	if *asJSON {
		emit(map[string]any{"command": "server restore-state", "verdict": r.Verdict, "result": r})
		return code
	}
	fmt.Printf(`%s: Gaia's state is restored.

  config                 %s
  data directory         %s
  state backup           %s (%s), written by %s, schema %d
  catalog                %d hosts, %d VMs, %d points (%d present), %d restore tests, %d evidence bundles
  evidence               %d bundles restored; ledger %s (%d entries)
  evidence trust anchor  %s
  TLS certificate sha256 %s
`, r.Verdict, r.ConfigPath, r.DataDir, r.StateBackup.ID, r.StateBackup.Created, r.StateBackup.Hostname, r.StateBackup.SchemaVersion,
		r.Catalog.Hosts, r.Catalog.VMs, r.Catalog.Points, r.Catalog.PresentPoints, r.Catalog.RestoreTests, r.Catalog.EvidenceBundles,
		r.Evidence.Restored, r.Ledger.Verdict, r.Ledger.Entries, r.EvidenceKeyID, r.CertSHA256)
	for _, rr := range r.Repositories {
		state := "connected"
		if !rr.Connected {
			state = "NOT CONNECTED: " + rr.Error
		}
		fmt.Printf("  repository %-11s %s (%s)\n", rr.Name, rr.Location, state)
	}
	if len(r.Problems) > 0 {
		fmt.Println("\nWhat could not be restored:")
		for _, p := range r.Problems {
			fmt.Println("  - " + p)
		}
	}
	fmt.Printf(`
A new recovery kit (generation %d) is %s: it knows this server. Start the service, log in, and save it off the
server again (gaia kit save).
`, r.KitGeneration, r.Kit)
	return code
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// cmdServer: gaia server init | run | migrate.
func cmdServer(ctx context.Context, args []string) int {
	// The database holds password hashes: nothing Gaia writes is readable by others (the unit sets the same).
	syscall.Umask(0o027)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gaia server init | run | migrate | restore-state [flags]  (-h for flags)")
		return 3
	}
	switch args[0] {
	case "init":
		return cmdServerInit(ctx, args[1:])
	case "restore-state":
		return cmdServerRestoreState(ctx, args[1:])
	case "run":
		return cmdServerRun(ctx, args[1:])
	case "migrate":
		return cmdServerMigrate(ctx, args[1:])
	}
	fmt.Fprintln(os.Stderr, "usage: gaia server init | run | migrate | restore-state [flags]")
	return 3
}

// readPassword reads a password from a file, $GAIA_PASSWORD, or the terminal without echo (twice when confirm).
func readPassword(file, prompt string, confirm bool) (string, error) {
	return readSecret(file, "GAIA_PASSWORD", prompt, confirm)
}

// readKitPassphrase reads the recovery kit's passphrase from a file, $GAIA_KIT_PASSPHRASE, or the terminal.
func readKitPassphrase(file, prompt string, confirm bool) (string, error) {
	return readSecret(file, "GAIA_KIT_PASSPHRASE", prompt, confirm)
}

func readSecret(file, env, prompt string, confirm bool) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if p := os.Getenv(env); p != "" {
		return p, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("no input: give the file flag, set %s, or run in a terminal", env)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Again: ")
		b2, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(b2) {
			return "", errors.New("the two passwords differ")
		}
	}
	return string(b), nil
}

func cmdServerInit(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("server init", flag.ExitOnError)
	config := fs.String("config", "/etc/gaia/server.json", "config file to create")
	dataDir := fs.String("data-dir", "/var/lib/gaia", "data directory (database, keys, evidence)")
	listen := fs.String("listen", ":8443", "address the API listens on (HTTPS)")
	names := fs.String("hostname", "", "comma-separated names or addresses clients use to reach this server (for the TLS certificate)")
	publicURL := fs.String("public-url", "", "https://NAME:PORT browsers use (WebAuthn needs a name, not an address); default https://<host name>:<port>")
	admin := fs.String("admin", "admin", "the first admin's username")
	pwFile := fs.String("password-file", "", "file holding the first admin's password (default: ask)")
	kitFile := fs.String("kit-passphrase-file", "", "file holding the recovery kit's passphrase (default: $GAIA_KIT_PASSPHRASE, or ask)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	pw, err := readPassword(*pwFile, fmt.Sprintf("Password for %s (at least %d characters): ", *admin, store.MinPasswordLength), true)
	if err != nil {
		return fail(*asJSON, "server init", err)
	}
	pass, err := readKitPassphrase(*kitFile, fmt.Sprintf("Passphrase for the recovery kit (at least %d characters; Gaia does not store it): ", recoverykit.MinPassphrase), true)
	if err != nil {
		return fail(*asJSON, "server init", err)
	}
	r, err := server.Init(ctx, server.InitOptions{ConfigPath: *config, DataDir: *dataDir, Listen: *listen, Hostnames: strings.Split(*names, ","),
		AdminUser: *admin, AdminPassword: pw, KitPassphrase: pass, PublicURL: *publicURL})
	if err != nil {
		return fail(*asJSON, "server init", err)
	}
	if *asJSON {
		emit(map[string]any{"command": "server init", "verdict": "OK", "result": r})
		return 0
	}
	fmt.Printf(`Gaia is set up.

  config                 %s
  data directory         %s
  listens on             %s (HTTPS)
  public URL             %s
  admin user             %s (break-glass)

Write these two down; you will need them:

  TLS certificate sha256 %s
      (gaia login shows it; log in only if it matches)
  evidence trust anchor  %s
      (the key_id that signs every evidence bundle; an auditor checks evidence against it)

The certificate is valid for: %s

The recovery kit (generation %d) is %s.
It is encrypted with the passphrase you chose; keep that passphrase in your password manager. Every new
repository writes a new kit: save it off this server after creating the repository (gaia kit save).

Next: start the service (systemctl enable --now gaia), open %s in a browser and sign in as %s: the
first sign-in enrols the admin's security key (WebAuthn; the browser must trust this server's certificate and reach
it by the public URL's name). Then, in a terminal: gaia login -server %s -browser
`, *config, r.Config.DataDir, r.Config.Listen, r.Config.PublicURL, r.Admin, r.CertSHA256, r.EvidenceKeyID, strings.Join(r.CertNames, ", "), r.KitGeneration, r.Kit, r.Config.PublicURL, r.Admin, r.Config.PublicURL)
	return 0
}

func cmdServerRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("server run", flag.ExitOnError)
	config := fs.String("config", "/etc/gaia/server.json", "config file")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := server.LoadConfig(*config)
	if err != nil {
		log.Error("config", "error", err)
		return 3
	}
	s, err := server.New(ctx, cfg, *config, log)
	if err != nil {
		log.Error("start", "error", err)
		return 3
	}
	defer s.Close()
	if err := s.Run(ctx); err != nil {
		log.Error("server stopped", "error", err)
		return 3
	}
	return 0
}

func cmdServerMigrate(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("server migrate", flag.ExitOnError)
	config := fs.String("config", "/etc/gaia/server.json", "config file")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	cfg, err := server.LoadConfig(*config)
	if err != nil {
		return fail(*asJSON, "server migrate", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fail(*asJSON, "server migrate", err)
	}
	defer db.Close()
	done, err := db.Migrate(ctx, 0)
	if err != nil {
		return fail(*asJSON, "server migrate", err)
	}
	applied, err := db.Applied(ctx)
	if err != nil {
		return fail(*asJSON, "server migrate", err)
	}
	if *asJSON {
		emit(map[string]any{"command": "server migrate", "verdict": "OK", "applied_now": done, "migrations": applied})
		return 0
	}
	fmt.Printf("schema version %d (applied now: %v)\n", len(applied), done)
	for _, a := range applied {
		fmt.Printf("  %d %-28s %s\n", a.Version, a.Name, a.AppliedAt)
	}
	return 0
}

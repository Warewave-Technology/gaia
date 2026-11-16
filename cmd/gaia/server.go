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

	"github.com/Warewave-Technology/gaia/internal/server"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// cmdServer: gaia server init | run | migrate.
func cmdServer(ctx context.Context, args []string) int {
	// The database holds password hashes: nothing Gaia writes is readable by others (the unit sets the same).
	syscall.Umask(0o027)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gaia server init | run | migrate [flags]  (-h for flags)")
		return 3
	}
	switch args[0] {
	case "init":
		return cmdServerInit(ctx, args[1:])
	case "run":
		return cmdServerRun(ctx, args[1:])
	case "migrate":
		return cmdServerMigrate(ctx, args[1:])
	}
	fmt.Fprintln(os.Stderr, "usage: gaia server init | run | migrate [flags]")
	return 3
}

// readPassword reads a password from a file, $GAIA_PASSWORD, or the terminal without echo (twice when confirm).
func readPassword(file, prompt string, confirm bool) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if p := os.Getenv("GAIA_PASSWORD"); p != "" {
		return p, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password: give -password-file, set GAIA_PASSWORD, or run in a terminal")
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
	admin := fs.String("admin", "admin", "the first admin's username")
	pwFile := fs.String("password-file", "", "file holding the first admin's password (default: ask)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	pw, err := readPassword(*pwFile, fmt.Sprintf("Password for %s (at least %d characters): ", *admin, store.MinPasswordLength), true)
	if err != nil {
		return fail(*asJSON, "server init", err)
	}
	r, err := server.Init(ctx, server.InitOptions{ConfigPath: *config, DataDir: *dataDir, Listen: *listen, Hostnames: strings.Split(*names, ","),
		AdminUser: *admin, AdminPassword: pw})
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
  admin user             %s

Write these two down; you will need them:

  TLS certificate sha256 %s
      (gaia login shows it; log in only if it matches)
  evidence trust anchor  %s
      (the key_id that signs every evidence bundle; an auditor checks evidence against it)

The certificate is valid for: %s
Next: start the service (systemctl enable --now gaia), then gaia login -server https://<host>:<port> -user %s
`, *config, r.Config.DataDir, r.Config.Listen, r.Admin, r.CertSHA256, r.EvidenceKeyID, strings.Join(r.CertNames, ", "), r.Admin)
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

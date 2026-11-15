// Command gaia is Gaia's verification CLI (T7, ADR-002 #3–4):
//
//	gaia verify -config VM.json -image RESTORED.raw -key KEY.pem -evidence-dir DIR -ledger LEDGER [-restore-json R.json]
//	gaia evidence keygen -key KEY.pem -pub PUB.pem
//	gaia evidence verify -pubkey PUB.pem (-bundle DIR | -ledger LEDGER -evidence-dir DIR)
//
// `gaia verify` boots the image in an isolated libvirt sandbox on a copy-on-write
// overlay, runs the VM's checks from inside the sandbox network, and seals a signed
// evidence bundle into the ledger. `gaia evidence verify` detects any change to a
// bundle or the ledger. Every command prints a human-readable result, or JSON with
// -json. Exit codes: 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN.
//
// `gaia probe` is internal: verify runs it inside the sandbox's network namespace.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "verify":
		os.Exit(cmdVerify(ctx, os.Args[2:]))
	case "evidence":
		if len(os.Args) < 3 {
			usage()
		}
		switch os.Args[2] {
		case "keygen":
			os.Exit(cmdKeygen(os.Args[3:]))
		case "verify":
			os.Exit(cmdEvidenceVerify(os.Args[3:]))
		}
	case "probe":
		os.Exit(cmdProbe(ctx, os.Args[2:]))
	}
	usage()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gaia verify | evidence keygen | evidence verify [flags]  (-h for flags)")
	os.Exit(3)
}

func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// fail reports a command that could not run: UNKNOWN, exit 3.
func fail(asJSON bool, cmd string, err error) int {
	if asJSON {
		emit(map[string]any{"command": cmd, "verdict": verify.Unknown, "error": err.Error()})
	}
	fmt.Fprintf(os.Stderr, "UNKNOWN: %s: %v\n", cmd, err)
	return 3
}

func cmdVerify(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	config := fs.String("config", "", "verification config of the VM (JSON)")
	image := fs.String("image", "", "restored disk image (never written)")
	format := fs.String("format", "raw", "image format: raw or qcow2")
	restoreJSON := fs.String("restore-json", "", "gaia-backup restore -json output of this image (restore time, for Proven RTO)")
	keyPath := fs.String("key", "", "ed25519 private key (PEM) that signs the evidence")
	evDir := fs.String("evidence-dir", "", "directory for evidence bundles")
	ledger := fs.String("ledger", "", "append-only ledger file")
	workDir := fs.String("work-dir", "/var/lib/libvirt/images", "directory for the copy-on-write overlay (qemu must have access)")
	serialDir := fs.String("serial-dir", "/var/log/libvirt/qemu", "directory for the serial console log while the guest runs")
	sock := fs.String("libvirt-socket", "/run/libvirt/libvirt-sock", "libvirtd socket")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *config == "" || *image == "" || *keyPath == "" || *evDir == "" || *ledger == "" {
		return fail(*asJSON, "verify", errors.New("-config, -image, -key, -evidence-dir and -ledger are required"))
	}
	cfg, err := verify.LoadConfig(*config)
	if err != nil {
		return fail(*asJSON, "verify", err)
	}
	key, err := evidence.LoadPrivateKey(*keyPath)
	if err != nil {
		return fail(*asJSON, "verify", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail(*asJSON, "verify", err)
	}
	progress := os.Stderr
	res, err := verify.Run(ctx, verify.Options{Config: cfg, Image: *image, ImageFormat: *format, RestoreJSON: *restoreJSON,
		WorkDir: *workDir, SerialDir: *serialDir, EvidenceDir: *evDir, Ledger: *ledger, Key: key, Socket: *sock,
		Self: self, Log: progress})
	if err != nil {
		return fail(*asJSON, "verify", err)
	}
	if *asJSON {
		emit(res)
	} else {
		fmt.Printf("%s: %s  (bundle %s, ledger seq %d)\n", res.VM, res.Verdict, res.BundleDir, res.LedgerSeq)
		for _, c := range res.Checks {
			fmt.Printf("  %-16s %-9s %8.3f s  %s\n", c.Name, c.Verdict, c.Seconds, c.Detail)
		}
		for _, e := range res.Errors {
			fmt.Printf("  error: %s\n", e)
		}
		keys := []string{"restore", "boot", "ready", "proven_rto", "total"}
		var t []string
		for _, k := range keys {
			if v, ok := res.Timings[k]; ok {
				t = append(t, fmt.Sprintf("%s %.3f s", k, v))
			}
		}
		fmt.Printf("  timings: %s\n", strings.Join(t, ", "))
	}
	return verify.ExitCode(res.Verdict)
}

func cmdKeygen(args []string) int {
	fs := flag.NewFlagSet("evidence keygen", flag.ExitOnError)
	priv := fs.String("key", "", "private key file to create (mode 0600)")
	pub := fs.String("pub", "", "public key file to create")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *priv == "" || *pub == "" {
		return fail(*asJSON, "evidence keygen", errors.New("-key and -pub are required"))
	}
	fp, err := evidence.GenerateKey(*priv, *pub)
	if err != nil {
		return fail(*asJSON, "evidence keygen", err)
	}
	if *asJSON {
		emit(map[string]any{"command": "evidence keygen", "verdict": verify.OK, "key": *priv, "pub": *pub, "fingerprint": fp})
	} else {
		fmt.Printf("ed25519 key %s, public key %s, fingerprint %s\n", *priv, *pub, fp)
	}
	return 0
}

func cmdEvidenceVerify(args []string) int {
	fs := flag.NewFlagSet("evidence verify", flag.ExitOnError)
	pubPath := fs.String("pubkey", "", "ed25519 public key (PEM)")
	bundle := fs.String("bundle", "", "check one bundle directory")
	ledger := fs.String("ledger", "", "check the ledger and every bundle it lists")
	evDir := fs.String("evidence-dir", "", "the bundles' directory (with -ledger)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *pubPath == "" || (*bundle == "") == (*ledger == "") || (*ledger != "" && *evDir == "") {
		return fail(*asJSON, "evidence verify", errors.New("-pubkey and either -bundle or -ledger with -evidence-dir are required"))
	}
	pub, err := evidence.LoadPublicKey(*pubPath)
	if err != nil {
		return fail(*asJSON, "evidence verify", err)
	}
	var (
		problems []string
		report   any
	)
	if *bundle != "" {
		r := evidence.VerifyBundle(*bundle, pub)
		problems, report = r.Problems, r
	} else {
		r, err := evidence.VerifyLedger(*ledger, *evDir, pub)
		if err != nil {
			return fail(*asJSON, "evidence verify", err)
		}
		problems = append(problems, r.Problems...)
		for _, b := range r.Bundles {
			for _, p := range b.Problems {
				problems = append(problems, b.BundleID+": "+p)
			}
		}
		report = r
	}
	verdict := verify.OK
	if len(problems) > 0 {
		verdict = verify.Critical
	}
	if *asJSON {
		emit(map[string]any{"command": "evidence verify", "verdict": verdict, "key_fingerprint": evidence.Fingerprint(pub),
			"checked_at": time.Now().UTC(), "report": report})
	} else {
		fmt.Printf("evidence: %s (key %s)\n", verdict, evidence.Fingerprint(pub))
		for _, p := range problems {
			fmt.Printf("  %s\n", p)
		}
	}
	return verify.ExitCode(verdict)
}

func cmdProbe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	check := fs.String("check", "", "the check, as JSON")
	host := fs.String("host", "", "guest address")
	timeout := fs.Int("timeout", 120, "seconds to keep trying")
	fs.Parse(args)
	var c verify.Check
	if err := json.Unmarshal([]byte(*check), &c); err != nil || *host == "" {
		fmt.Fprintln(os.Stderr, "probe: -check JSON and -host are required")
		return 3
	}
	r := verify.Probe(ctx, c, *host, time.Duration(*timeout)*time.Second)
	json.NewEncoder(os.Stdout).Encode(r)
	if r.OK {
		return 0
	}
	return 2
}

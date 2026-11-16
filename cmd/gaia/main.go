// Command gaia is Gaia's server and CLI. `gaia server` runs the API (ADR-007, internal/server); the commands in
// cli.go are its client. The verification and evidence tools from T7 onwards are below:
//
//	gaia verify -config VM.json -image RESTORED.raw -key KEY.pem -evidence-dir DIR -ledger LEDGER [-restore-json R.json]
//	gaia evidence keygen -key KEY.pem -pub PUB.pem
//	gaia evidence verify -pubkey PUB.pem (-bundle DIR | -ledger LEDGER -evidence-dir DIR)
//	gaia migrate-check -url https://ESXI/sdk -password-file F -vm A,B -key KEY.pem -evidence-dir DIR -ledger LEDGER
//
// `gaia verify` boots the image in an isolated libvirt sandbox on a copy-on-write
// overlay, runs the VM's checks from inside the sandbox network, and seals a signed
// evidence bundle into the ledger. `gaia evidence verify` detects any change to a
// bundle or the ledger. Every command prints a human-readable result, or JSON with
// -json. Exit codes: 0 OK, 1 WARNING, 2 CRITICAL, 3 UNKNOWN.
//
// `gaia migrate-check` (ADR-003, stage 1) reads powered-off VMs from vSphere with
// read-only calls only, converts them with virt-v2v and verifies them like above,
// one signed readiness bundle per VM.
//
// `gaia probe` is internal: verify runs it inside the sandbox's network namespace.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/migrate"
	"github.com/Warewave-Technology/gaia/internal/verify"
	"github.com/Warewave-Technology/gaia/internal/vsphereread"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// "evidence" is the API resource (list, get, export) and the local tool (keygen, seal, verify, rotate).
	localEvidence := os.Args[1] == "evidence" && len(os.Args) > 2 && slices.Contains([]string{"keygen", "seal", "verify", "rotate"}, os.Args[2])
	if isCLICommand(os.Args[1]) && !localEvidence {
		os.Exit(runCLI(ctx, os.Args[1:]))
	}
	switch os.Args[1] {
	case "server":
		os.Exit(cmdServer(ctx, os.Args[2:]))
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
		case "rotate":
			os.Exit(cmdEvidenceRotate(os.Args[3:]))
		case "seal":
			os.Exit(cmdEvidenceSeal(os.Args[3:]))
		}
	case "migrate-check":
		os.Exit(cmdMigrateCheck(ctx, os.Args[2:]))
	case "probe":
		os.Exit(cmdProbe(ctx, os.Args[2:]))
	}
	usage()
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  gaia server init | run | migrate           the server (ADR-007)
  gaia login | logout | whoami | system | password
  gaia users | hosts | vms | repositories | policies | jobs | points | tests | evidence | audit | state-changes | state-backups VERB ...
  gaia api METHOD PATH [JSON]
  gaia verify | migrate-check | evidence keygen | seal | verify | rotate   (lab tools)
(-h on a server or lab command shows its flags)`)
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
		emit(map[string]any{"command": "evidence keygen", "verdict": verify.OK, "key": *priv, "pub": *pub, "key_id": fp, "fingerprint": fp})
	} else {
		fmt.Printf("ed25519 key %s, public key %s, key_id %s\n", *priv, *pub, fp)
	}
	return 0
}

// cmdEvidenceVerify checks a bundle or a ledger with its bundles (Restore Evidence Format v1, docs/spec/evidence-v1.md;
// v0 read-only). The verdict is the specification's: OK, CRITICAL, or UNKNOWN when something could not be checked
// (no trust anchor, an unsupported format).
func cmdEvidenceVerify(args []string) int {
	fs := flag.NewFlagSet("evidence verify", flag.ExitOnError)
	pubPath := fs.String("pubkey", "", "trust anchor: the ledger's genesis key, or the key that signed -bundle (PEM, or base64 of the raw 32 bytes); without it the verdict is at best UNKNOWN")
	bundle := fs.String("bundle", "", "check one bundle directory")
	ledger := fs.String("ledger", "", "check the ledger and every bundle it lists")
	evDir := fs.String("evidence-dir", "", "the bundles' directory (with -ledger)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if (*bundle == "") == (*ledger == "") || (*ledger != "" && *evDir == "") {
		return fail(*asJSON, "evidence verify", errors.New("either -bundle or -ledger with -evidence-dir is required"))
	}
	var pub ed25519.PublicKey
	if *pubPath != "" {
		var err error
		if pub, err = evidence.LoadPublicKey(*pubPath); err != nil {
			return fail(*asJSON, "evidence verify", err)
		}
	}
	var (
		verdict  string
		findings []evidence.Finding
		report   any
	)
	if *bundle != "" {
		r := evidence.VerifyBundle(*bundle, pub)
		verdict, findings, report = r.Verdict, r.Findings, r
	} else {
		r, err := evidence.VerifyLedger(*ledger, *evDir, pub)
		if err != nil {
			return fail(*asJSON, "evidence verify", err)
		}
		verdict, findings, report = r.Verdict, r.AllFindings(), r
	}
	anchor := ""
	if pub != nil {
		anchor = evidence.KeyID(pub)
	}
	if *asJSON {
		emit(map[string]any{"command": "evidence verify", "verdict": verdict, "anchor_key_id": anchor, "key_fingerprint": anchor,
			"checked_at": time.Now().UTC(), "findings": findings, "report": report})
	} else {
		fmt.Printf("evidence: %s (trust anchor %s)\n", verdict, orNone(anchor))
		for _, f := range findings {
			where := ""
			if f.Where != "" {
				where = f.Where + ": "
			}
			fmt.Printf("  %-9s %s%s: %s\n", f.Severity, where, f.Code, f.Detail)
		}
	}
	return verify.ExitCode(verdict)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// cmdEvidenceSeal seals a directory of artefacts as a bundle and appends it to a ledger (a new ledger starts
// with the signing key's genesis entry). The directory's name is the bundle id.
func cmdEvidenceSeal(args []string) int {
	fs := flag.NewFlagSet("evidence seal", flag.ExitOnError)
	dir := fs.String("dir", "", "the bundle directory (its name is the bundle id)")
	keyPath := fs.String("key", "", "the ledger's current private key (PEM)")
	ledger := fs.String("ledger", "", "the ledger to append to")
	subject := fs.String("subject", "", "what the evidence is about (e.g. a VM name)")
	verdict := fs.String("verdict", verify.Unknown, "the verdict the evidence supports")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *dir == "" || *keyPath == "" || *ledger == "" {
		return fail(*asJSON, "evidence seal", errors.New("-dir, -key and -ledger are required"))
	}
	key, err := evidence.LoadPrivateKey(*keyPath)
	if err != nil {
		return fail(*asJSON, "evidence seal", err)
	}
	id := filepath.Base(filepath.Clean(*dir))
	h, err := evidence.Seal(*dir, id, map[string]string{"subject": *subject, "verdict": *verdict}, key)
	if err != nil {
		return fail(*asJSON, "evidence seal", err)
	}
	e, err := evidence.Append(*ledger, evidence.Entry{BundleID: id, BundleSHA256: h, Subject: *subject, Verdict: *verdict}, key)
	if err != nil {
		return fail(*asJSON, "evidence seal", fmt.Errorf("bundle sealed but not in the ledger: %w", err))
	}
	if *asJSON {
		emit(map[string]any{"command": "evidence seal", "verdict": verify.OK, "bundle_id": id, "bundle_sha256": h, "ledger": *ledger,
			"seq": e.Seq, "key_id": e.KeyID})
	} else {
		fmt.Printf("bundle %s sealed (manifest sha256 %s), ledger %s entry %d, key %s\n", id, h, *ledger, e.Seq, e.KeyID)
	}
	return 0
}

// cmdEvidenceRotate hands a ledger over to a new signing key: the current key signs an entry that names the new one.
func cmdEvidenceRotate(args []string) int {
	fs := flag.NewFlagSet("evidence rotate", flag.ExitOnError)
	ledger := fs.String("ledger", "", "the ledger")
	keyPath := fs.String("key", "", "the ledger's current private key (PEM)")
	newPub := fs.String("new-pub", "", "the new public key (PEM, or base64 of the raw 32 bytes)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *ledger == "" || *keyPath == "" || *newPub == "" {
		return fail(*asJSON, "evidence rotate", errors.New("-ledger, -key and -new-pub are required"))
	}
	key, err := evidence.LoadPrivateKey(*keyPath)
	if err != nil {
		return fail(*asJSON, "evidence rotate", err)
	}
	next, err := evidence.LoadPublicKey(*newPub)
	if err != nil {
		return fail(*asJSON, "evidence rotate", err)
	}
	e, err := evidence.Rotate(*ledger, key, next)
	if err != nil {
		return fail(*asJSON, "evidence rotate", err)
	}
	old := evidence.KeyID(key.Public().(ed25519.PublicKey))
	if *asJSON {
		emit(map[string]any{"command": "evidence rotate", "verdict": verify.OK, "ledger": *ledger, "seq": e.Seq,
			"old_key_id": old, "new_key_id": evidence.KeyID(next), "entry_sha256": e.EntrySHA256})
	} else {
		fmt.Printf("ledger %s: entry %d hands over from key %s to %s\n", *ledger, e.Seq, old, evidence.KeyID(next))
	}
	return 0
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

func cmdMigrateCheck(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("migrate-check", flag.ExitOnError)
	rawURL := fs.String("url", os.Getenv("GAIA_VSPHERE_URL"), "vSphere SDK URL, e.g. https://esxi.lab/sdk")
	user := fs.String("user", envOr("GAIA_VSPHERE_USER", "root"), "vSphere user")
	pwFile := fs.String("password-file", "", "file holding the password (default: $GAIA_VSPHERE_PASSWORD); never printed")
	insecure := fs.Bool("insecure", false, "skip TLS verification (lab only)")
	vms := fs.String("vm", "", "comma-separated VM names (default: every powered-off VM)")
	checksPath := fs.String("checks", "", "per-VM checks (JSON: {\"vm\": {\"template\": ..., \"checks\": [...]}, \"*\": {...}})")
	workDir := fs.String("work-dir", "/var/lib/gaia/migrate", "downloads (one folder per VM)")
	imageDir := fs.String("image-dir", "/var/lib/libvirt/images/gaia-migrate", "converted images (qemu must have access)")
	calls := fs.String("calls", "", "write the read-only guard's call log here (JSON lines); default: <work-dir>/calls.jsonl")
	keyPath := fs.String("key", "", "ed25519 private key (PEM) that signs the evidence")
	evDir := fs.String("evidence-dir", "", "directory for evidence bundles")
	ledger := fs.String("ledger", "", "append-only ledger file")
	sandboxDir := fs.String("sandbox-dir", "/var/lib/libvirt/images", "directory for the sandbox's copy-on-write overlay")
	serialDir := fs.String("serial-dir", "/var/log/libvirt/qemu", "directory for the serial console log while the guest runs")
	v2vTimeout := fs.Duration("v2v-timeout", 30*time.Minute, "virt-v2v timeout per VM")
	virtioWin := fs.String("virtio-win", os.Getenv("VIRTIO_WIN"), "virtio-win ISO for Windows guests (virt-v2v's VIRTIO_WIN)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *rawURL == "" || *keyPath == "" || *evDir == "" || *ledger == "" {
		return fail(*asJSON, "migrate-check", errors.New("-url, -key, -evidence-dir and -ledger are required"))
	}
	pw := os.Getenv("GAIA_VSPHERE_PASSWORD")
	if *pwFile != "" {
		b, err := os.ReadFile(*pwFile)
		if err != nil {
			return fail(*asJSON, "migrate-check", err)
		}
		pw = strings.TrimRight(string(b), "\r\n")
	}
	if pw == "" {
		return fail(*asJSON, "migrate-check", errors.New("no password: -password-file or GAIA_VSPHERE_PASSWORD"))
	}
	var checks migrate.ChecksFile
	if *checksPath != "" {
		var err error
		if checks, err = migrate.LoadChecks(*checksPath); err != nil {
			return fail(*asJSON, "migrate-check", err)
		}
	}
	key, err := evidence.LoadPrivateKey(*keyPath)
	if err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	if err := os.MkdirAll(*workDir, 0o755); err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	if *calls == "" {
		*calls = filepath.Join(*workDir, "calls.jsonl")
	}
	callLog, err := os.OpenFile(*calls, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	defer callLog.Close()
	src, err := vsphereread.Connect(ctx, *rawURL, *user, pw, *insecure, callLog)
	if err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	defer src.Logout(context.Background())
	var names []string
	for _, n := range strings.Split(*vms, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	rep, err := migrate.Run(ctx, migrate.Options{Source: src, VMs: names, Checks: checks, WorkDir: *workDir, ImageDir: *imageDir,
		CallLog: *calls, V2VTimeout: *v2vTimeout, VirtioWin: *virtioWin, Log: os.Stderr,
		Verify: verify.Options{Key: key, EvidenceDir: *evDir, Ledger: *ledger, WorkDir: *sandboxDir, SerialDir: *serialDir,
			Socket: "/run/libvirt/libvirt-sock", Self: self, Log: os.Stderr}})
	if err != nil {
		return fail(*asJSON, "migrate-check", err)
	}
	verdict := rep.Verdict
	if rep.Blocked > 0 { // Gaia tried a write call: the guard stopped it, but that is a bug (ADR-003 §2)
		verdict = verify.Worst(verdict, verify.Critical)
	}
	if *asJSON {
		emit(rep)
	} else {
		fmt.Printf("migration check of %s (%s): %s\n", rep.Source, rep.Product, verdict)
		for _, r := range rep.VMs {
			fmt.Printf("  %-24s %-9s bundle %s (ledger seq %d)\n", r.VM, r.Readiness, r.BundleID, r.LedgerSeq)
			for _, c := range r.Checks {
				fmt.Printf("      %-16s %-9s %s\n", c.Name, c.Verdict, c.Detail)
			}
		}
		fmt.Printf("  vSphere calls: %d, blocked: %d (call log %s)\n", rep.Calls, rep.Blocked, *calls)
	}
	return verify.ExitCode(verdict)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

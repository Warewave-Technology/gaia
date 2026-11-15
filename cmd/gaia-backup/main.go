// Command gaia-backup is the T6 spike of Gaia's KVM data path (ADR-004 §4–6):
// a libvirt pull-mode backup (go-libvirt) creates a checkpoint, the disk and its
// dirty bitmap are read over NBD (internal/nbd), and only the changed extents are
// written into a Kopia repository through the Kopia Go library (internal/diskstore).
// Every point restores to a complete raw image on its own.
//
//	gaia-backup repo-create -repo DIR -config FILE [-cache-dir DIR -cache-limit-mb N]
//	gaia-backup backup  -config FILE -domain NAME [-disk vda] [-full] [-nbd tcp:127.0.0.1:10809] [-conns 4] [-scratch-dir DIR] [-resume]
//	gaia-backup restore -config FILE (-point ID | -domain NAME [-disk vda]) -out FILE [-workers 4]
//	gaia-backup points  -config FILE [-domain NAME] [-disk vda]
//
// The repository password comes from $GAIA_REPO_PASSWORD. Every command prints a
// human-readable result, or JSON with -json. Exit codes: 0 OK, 3 UNKNOWN (it failed:
// nothing was verified, so nothing can be called OK).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/libvirtbackup"
	"github.com/Warewave-Technology/gaia/internal/nbd"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var (
		res any
		err error
	)
	cmd, args := os.Args[1], os.Args[2:]
	asJSON := false
	switch cmd {
	case "repo-create":
		res, asJSON, err = cmdRepoCreate(ctx, args)
	case "backup":
		res, asJSON, err = cmdBackup(ctx, args)
	case "restore":
		res, asJSON, err = cmdRestore(ctx, args)
	case "points":
		res, asJSON, err = cmdPoints(ctx, args)
	default:
		usage()
	}
	if err != nil {
		if asJSON {
			emit(os.Stdout, map[string]any{"command": cmd, "verdict": "UNKNOWN", "error": err.Error()})
		}
		fmt.Fprintf(os.Stderr, "UNKNOWN: %s: %v\n", cmd, err)
		os.Exit(3)
	}
	if asJSON {
		emit(os.Stdout, res)
	} else {
		fmt.Print(human(res))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gaia-backup repo-create|backup|restore|points [flags]  (-h for flags)")
	os.Exit(3)
}

func emit(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func password() (string, error) {
	pw := os.Getenv("GAIA_REPO_PASSWORD")
	if pw == "" {
		return "", errors.New("GAIA_REPO_PASSWORD is not set")
	}
	return pw, nil
}

func cmdRepoCreate(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("repo-create", flag.ExitOnError)
	path := fs.String("repo", "", "repository directory (filesystem storage)")
	config := fs.String("config", "", "Kopia client config file to write")
	cacheDir := fs.String("cache-dir", "", "Kopia cache directory (default: Kopia's, next to the config)")
	limit := fs.Int64("cache-limit-mb", 0, "content cache hard limit in MB, 0 = Kopia's default (none); the soft target is half of it")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *path == "" || *config == "" {
		return nil, *asJSON, errors.New("-repo and -config are required")
	}
	pw, err := password()
	if err != nil {
		return nil, *asJSON, err
	}
	o := diskstore.RepoOptions{Path: *path, ConfigFile: *config, Password: pw, CacheDir: *cacheDir}
	if *limit > 0 {
		o.ContentCacheMB, o.ContentCacheLimitMB = *limit/2, *limit
		o.MetadataCacheMB, o.MetadataCacheLimitMB = *limit/4, *limit/2
	}
	if err := diskstore.Create(ctx, o); err != nil {
		return nil, *asJSON, err
	}
	return map[string]any{"command": "repo-create", "verdict": "OK", "repo": *path, "config": *config,
		"splitter": diskstore.Splitter, "compression": diskstore.Compression, "block_size": diskstore.BlockSize,
		"cache_dir": *cacheDir, "content_cache_limit_mb": o.ContentCacheLimitMB}, *asJSON, nil
}

// BackupReport is the JSON result of one backup.
type BackupReport struct {
	Command string `json:"command"`
	Verdict string `json:"verdict"`
	Libvirt string `json:"libvirt"`
	QEMU    string `json:"qemu"`
	Domain  string `json:"domain"`
	Disk    string `json:"disk"`
	Kind    string `json:"kind"`
	// What the source offered and what was read: for an incremental, changed_bytes is the size of
	// the dirty bitmap; nbd_requested_bytes what was asked for with NBD_CMD_READ; nbd_payload_bytes
	// what came back as data (holes in the export come back as hole chunks, without payload).
	DiskBytes         uint64             `json:"disk_bytes"`
	ChangedBytes      uint64             `json:"changed_bytes"`
	ChangedExtents    int                `json:"changed_extents"`
	NBDRequestedBytes uint64             `json:"nbd_requested_bytes"`
	NBDPayloadBytes   uint64             `json:"nbd_payload_bytes"`
	NBDReadCommands   uint64             `json:"nbd_read_commands"`
	NBDConnections    int                `json:"nbd_connections"`
	MetaContext       string             `json:"meta_context"`
	Export            string             `json:"export"`
	Point             *diskstore.Point   `json:"point"`
	Timings           map[string]float64 `json:"timings_s"`
	BackupXML         string             `json:"backup_xml"`
}

func cmdBackup(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	config := fs.String("config", "", "Kopia client config file")
	domain := fs.String("domain", "", "libvirt domain")
	disk := fs.String("disk", "vda", "disk target")
	full := fs.Bool("full", false, "full backup even if the repository has a point for this disk")
	server := fs.String("nbd", "tcp:127.0.0.1:10809", "where qemu's NBD server listens: tcp:HOST:PORT or unix:/path")
	conns := fs.Int("conns", 4, "parallel NBD connections (each reads one block at a time)")
	sock := fs.String("libvirt-socket", libvirtbackup.DefaultSocket, "libvirtd socket")
	scratch := fs.String("scratch-dir", "/var/lib/libvirt/images", "directory for qemu's copy-before-write scratch file (libvirt creates and removes it)")
	resume := fs.Bool("resume", false, "resume the domain once the job has started, if it is paused (pull mode keeps the export at the checkpoint)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *config == "" || *domain == "" || *conns < 1 {
		return nil, *asJSON, errors.New("-config and -domain are required, -conns >= 1")
	}
	srv, err := libvirtbackup.ParseServer(*server)
	if err != nil {
		return nil, *asJSON, err
	}
	pw, err := password()
	if err != nil {
		return nil, *asJSON, err
	}
	store, err := diskstore.Open(ctx, *config, pw)
	if err != nil {
		return nil, *asJSON, err
	}
	defer store.Close(ctx)

	var parent *diskstore.Point
	if !*full {
		pts, err := store.Points(ctx, *domain, *disk)
		if err != nil {
			return nil, *asJSON, err
		}
		if len(pts) > 0 {
			parent = pts[len(pts)-1]
		}
	}
	lv, err := libvirtbackup.Connect(*sock)
	if err != nil {
		return nil, *asJSON, err
	}
	defer lv.Close()
	rep := &BackupReport{Command: "backup", Verdict: "UNKNOWN", Domain: *domain, Disk: *disk, Kind: "full",
		NBDConnections: *conns, Timings: map[string]float64{}}
	if rep.Libvirt, rep.QEMU, err = lv.Versions(); err != nil {
		return nil, *asJSON, err
	}
	req := libvirtbackup.Request{Domain: *domain, Disk: *disk, Server: srv, ScratchDir: *scratch,
		Checkpoint: "gaia-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000Z"), ".", "")}
	if parent != nil {
		req.Incremental, rep.Kind = parent.Checkpoint, "incremental"
	}

	t0 := time.Now()
	job, err := lv.Begin(req)
	if err != nil {
		return nil, *asJSON, err
	}
	rep.Timings["begin"] = time.Since(t0).Seconds()
	rep.Export, rep.BackupXML = job.Export, job.XML
	err = func() error {
		if *resume {
			if paused, err := job.Paused(); err != nil {
				return err
			} else if paused {
				if err := job.Resume(); err != nil {
					return fmt.Errorf("resume: %w", err)
				}
				rep.Timings["resumed_after_begin"] = time.Since(t0).Seconds()
			}
		}
		return backupDisk(ctx, store, job, req, parent, *conns, rep)
	}()
	t1 := time.Now()
	if endErr := job.End(); endErr != nil {
		err = errors.Join(err, endErr)
	}
	rep.Timings["end"] = time.Since(t1).Seconds()
	rep.Timings["total"] = time.Since(t0).Seconds()
	if err != nil {
		return nil, *asJSON, err
	}
	rep.Verdict = "OK"
	return rep, *asJSON, nil
}

func backupDisk(ctx context.Context, store *diskstore.Store, job *libvirtbackup.Job, req libvirtbackup.Request,
	parent *diskstore.Point, conns int, rep *BackupReport) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second) // reads have no deadline
	defer cancel()
	network, addr := job.Server.Network, job.Server.Addr
	rep.MetaContext = nbd.ContextBaseAllocation
	if job.Bitmap != "" {
		rep.MetaContext = nbd.DirtyBitmapPrefix + job.Bitmap
	}
	t0 := time.Now()
	first, err := nbd.Dial(dialCtx, network, addr, nbd.Options{Export: job.Export, MetaContexts: []string{rep.MetaContext}})
	if err != nil {
		return fmt.Errorf("NBD %s %s export %q: %w", network, addr, job.Export, err)
	}
	clients := []*nbd.Client{first}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for len(clients) < conns {
		c, err := nbd.Dial(dialCtx, network, addr, nbd.Options{Export: job.Export})
		if err != nil {
			return fmt.Errorf("NBD connection %d: %w", len(clients)+1, err)
		}
		clients = append(clients, c)
	}
	rep.DiskBytes = first.Size()
	ext, err := first.Extents(rep.MetaContext, 0, first.Size())
	if err != nil {
		return err
	}
	var changed []diskstore.Range
	for _, e := range ext {
		// Full: everything that is neither a hole nor known zero. Incremental: the dirty extents.
		need := e.Flags&(nbd.StateHole|nbd.StateZero) == 0
		if job.Bitmap != "" {
			need = e.Flags&nbd.StateDirty != 0
		}
		if need {
			changed = append(changed, diskstore.Range{Offset: e.Offset, Length: e.Length})
			rep.ChangedBytes += e.Length
			rep.ChangedExtents++
		}
	}
	rep.Timings["extents"] = time.Since(t0).Seconds()

	sources := make([]diskstore.Source, len(clients))
	for i, c := range clients {
		sources[i] = c
	}
	t1 := time.Now()
	pt, err := store.Backup(ctx, diskstore.BackupRequest{Domain: req.Domain, Disk: req.Disk,
		Checkpoint: req.Checkpoint, Size: first.Size(), Changed: changed, Parent: parent, Sources: sources})
	if err != nil {
		return err
	}
	rep.Timings["data"] = time.Since(t1).Seconds()
	rep.Point = pt
	rep.NBDRequestedBytes = pt.Stats.SourceReadBytes
	for _, c := range clients {
		b, n := c.Stats()
		rep.NBDPayloadBytes += b
		rep.NBDReadCommands += n
	}
	return nil
}

func cmdRestore(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	config := fs.String("config", "", "Kopia client config file")
	point := fs.String("point", "", "point ID (default: the latest point of -domain/-disk)")
	domain := fs.String("domain", "", "libvirt domain")
	disk := fs.String("disk", "vda", "disk target")
	out := fs.String("out", "", "raw image to create (must not exist)")
	workers := fs.Int("workers", 4, "parallel block readers")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *config == "" || *out == "" || (*point == "" && *domain == "") {
		return nil, *asJSON, errors.New("-config, -out and -point or -domain are required")
	}
	pw, err := password()
	if err != nil {
		return nil, *asJSON, err
	}
	store, err := diskstore.Open(ctx, *config, pw)
	if err != nil {
		return nil, *asJSON, err
	}
	defer store.Close(ctx)
	var p *diskstore.Point
	if *point != "" {
		p, err = store.Point(ctx, *point)
	} else {
		var pts []*diskstore.Point
		if pts, err = store.Points(ctx, *domain, *disk); err == nil && len(pts) == 0 {
			err = fmt.Errorf("no point for %s/%s", *domain, *disk)
		} else if err == nil {
			p = pts[len(pts)-1]
		}
	}
	if err != nil {
		return nil, *asJSON, err
	}
	st, err := store.Restore(ctx, p, *out, *workers)
	if err != nil {
		os.Remove(*out)
		return nil, *asJSON, err
	}
	return map[string]any{"command": "restore", "verdict": "OK", "point": p, "out": *out, "format": "raw",
		"workers": *workers, "stats": st}, *asJSON, nil
}

func cmdPoints(ctx context.Context, args []string) (any, bool, error) {
	fs := flag.NewFlagSet("points", flag.ExitOnError)
	config := fs.String("config", "", "Kopia client config file")
	domain := fs.String("domain", "", "libvirt domain (default: all)")
	disk := fs.String("disk", "", "disk target (default: all)")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	if *config == "" {
		return nil, *asJSON, errors.New("-config is required")
	}
	pw, err := password()
	if err != nil {
		return nil, *asJSON, err
	}
	store, err := diskstore.Open(ctx, *config, pw)
	if err != nil {
		return nil, *asJSON, err
	}
	defer store.Close(ctx)
	pts, err := store.Points(ctx, *domain, *disk)
	if err != nil {
		return nil, *asJSON, err
	}
	return map[string]any{"command": "points", "verdict": "OK", "points": pts}, *asJSON, nil
}

func human(res any) string {
	var b strings.Builder
	switch r := res.(type) {
	case *BackupReport:
		p := r.Point
		fmt.Fprintf(&b, "%s backup of %s/%s: %s\n", r.Kind, r.Domain, r.Disk, r.Verdict)
		fmt.Fprintf(&b, "  point %s, checkpoint %s (parent %s)\n", p.ID, p.Checkpoint, orDash(p.ParentCheckpoint))
		fmt.Fprintf(&b, "  disk %d bytes; %s: %d bytes in %d extents\n", r.DiskBytes, r.MetaContext, r.ChangedBytes, r.ChangedExtents)
		fmt.Fprintf(&b, "  read over NBD: %d bytes requested, %d payload, %d commands on %d connections\n",
			r.NBDRequestedBytes, r.NBDPayloadBytes, r.NBDReadCommands, r.NBDConnections)
		fmt.Fprintf(&b, "  blocks: %d total, %d written, %d reused, %d zero; %d parent bytes read back\n",
			p.Stats.Blocks, p.Stats.BlocksWritten, p.Stats.BlocksReused, p.Stats.BlocksZero, p.Stats.RepoReadBytes)
		fmt.Fprintf(&b, "  time: %.3f s total, %.3f s data\n", r.Timings["total"], r.Timings["data"])
	default:
		j, _ := json.MarshalIndent(res, "", "  ")
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

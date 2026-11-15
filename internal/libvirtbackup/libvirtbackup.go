// Package libvirtbackup drives libvirt's pull-mode backup (ADR-004 §4) through
// the pure-Go RPC client github.com/digitalocean/go-libvirt (ADR-004 §5):
// it starts a backup job that creates a checkpoint (a persistent dirty bitmap in
// the qcow2) at the same instant, and reports where qemu's NBD server exports the
// point-in-time disk and, for an incremental backup, the dirty bitmap since the
// parent checkpoint. Ending the job removes the export.
package libvirtbackup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
)

// DefaultSocket is libvirtd's read-write socket on Debian.
const DefaultSocket = "/run/libvirt/libvirt-sock"

// Conn is a connection to the local libvirtd (qemu:///system).
type Conn struct {
	l *libvirt.Libvirt
}

// Connect dials libvirtd's unix socket.
func Connect(socket string) (*Conn, error) {
	l := libvirt.NewWithDialer(dialers.NewLocal(dialers.WithSocket(socket)))
	if err := l.ConnectToURI(libvirt.QEMUSystem); err != nil {
		return nil, fmt.Errorf("libvirt connect %s: %w", socket, err)
	}
	return &Conn{l: l}, nil
}

// Close disconnects.
func (c *Conn) Close() error { return c.l.Disconnect() }

// Versions returns libvirt's and the hypervisor's versions as "major.minor.micro".
func (c *Conn) Versions() (lib, hv string, err error) {
	lv, err := c.l.ConnectGetLibVersion()
	if err != nil {
		return "", "", err
	}
	hvv, err := c.l.ConnectGetVersion()
	if err != nil {
		return "", "", err
	}
	return version(lv), version(hvv), nil
}

func version(v uint64) string { return fmt.Sprintf("%d.%d.%d", v/1000000, v/1000%1000, v%1000) }

// Server is where qemu's NBD server listens: Network "tcp" (Addr "host:port") or "unix" (Addr a path).
type Server struct {
	Network, Addr string
}

// ParseServer parses "tcp:HOST:PORT" or "unix:/path".
func ParseServer(s string) (Server, error) {
	network, addr, ok := strings.Cut(s, ":")
	if !ok || addr == "" || (network != "tcp" && network != "unix") {
		return Server{}, fmt.Errorf("NBD server %q: want tcp:HOST:PORT or unix:/path", s)
	}
	return Server{network, addr}, nil
}

func (s Server) xml() (string, error) {
	if s.Network == "unix" {
		return fmt.Sprintf(`<server transport='unix' socket='%s'/>`, xmlEscape(s.Addr)), nil
	}
	host, port, ok := strings.Cut(s.Addr, ":")
	if _, err := strconv.Atoi(port); !ok || err != nil {
		return "", fmt.Errorf("NBD server address %q: want HOST:PORT", s.Addr)
	}
	return fmt.Sprintf(`<server transport='tcp' name='%s' port='%s'/>`, xmlEscape(host), port), nil
}

// Request describes one backup job for one disk of a running (or paused) domain.
type Request struct {
	Domain     string
	Disk       string // target name, e.g. "vda"
	Checkpoint string // name of the checkpoint the job creates
	// Incremental names the parent checkpoint; empty means a full backup.
	Incremental string
	Server      Server
	// ScratchDir holds the copy-before-write scratch file qemu needs in pull mode;
	// libvirt creates it at Begin and removes it at End.
	ScratchDir string
}

// Job is a running pull-mode backup job.
type Job struct {
	c      *Conn
	dom    libvirt.Domain
	Export string // NBD export name of the disk
	Bitmap string // NBD meta context bitmap name ("" for a full backup), without the qemu:dirty-bitmap: prefix
	Server Server
	XML    string // the job's backup XML as libvirt reports it
}

// Begin starts the job. A paused domain stays paused; the caller may resume it afterwards
// (pull mode keeps the export at the checkpoint state while the guest writes).
func (c *Conn) Begin(r Request) (*Job, error) {
	dom, err := c.l.DomainLookupByName(r.Domain)
	if err != nil {
		return nil, fmt.Errorf("domain %q: %w", r.Domain, err)
	}
	domXML, err := c.l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		return nil, err
	}
	disks, err := diskTargets(domXML)
	if err != nil {
		return nil, err
	}
	found := false
	var bdisks, cdisks strings.Builder
	for _, d := range disks {
		if d.Target == r.Disk {
			found = true
			scratch := path.Join(r.ScratchDir, fmt.Sprintf("gaia-scratch-%s-%s-%s.qcow2", r.Domain, d.Target, r.Checkpoint))
			fmt.Fprintf(&bdisks, `<disk name='%s' backup='yes' type='file'><scratch file='%s'/></disk>`, xmlEscape(d.Target), xmlEscape(scratch))
			fmt.Fprintf(&cdisks, `<disk name='%s' checkpoint='bitmap'/>`, xmlEscape(d.Target))
		} else {
			fmt.Fprintf(&bdisks, `<disk name='%s' backup='no'/>`, xmlEscape(d.Target))
			fmt.Fprintf(&cdisks, `<disk name='%s' checkpoint='no'/>`, xmlEscape(d.Target))
		}
	}
	if !found {
		return nil, fmt.Errorf("domain %q has no disk %q", r.Domain, r.Disk)
	}
	if !path.IsAbs(r.ScratchDir) {
		return nil, fmt.Errorf("scratch directory %q must be an absolute path", r.ScratchDir)
	}
	server, err := r.Server.xml()
	if err != nil {
		return nil, err
	}
	incr := ""
	if r.Incremental != "" {
		if _, err := c.l.DomainCheckpointLookupByName(dom, r.Incremental, 0); err != nil {
			return nil, fmt.Errorf("parent checkpoint %q: %w (a full backup is needed)", r.Incremental, err)
		}
		incr = fmt.Sprintf(`<incremental>%s</incremental>`, xmlEscape(r.Incremental))
	}
	backupXML := fmt.Sprintf(`<domainbackup mode='pull'>%s%s<disks>%s</disks></domainbackup>`, incr, server, bdisks.String())
	checkpointXML := fmt.Sprintf(`<domaincheckpoint><name>%s</name><disks>%s</disks></domaincheckpoint>`,
		xmlEscape(r.Checkpoint), cdisks.String())
	if err := c.l.DomainBackupBegin(dom, backupXML, libvirt.OptString{checkpointXML}, 0); err != nil {
		return nil, fmt.Errorf("backup begin: %w", err)
	}
	j := &Job{c: c, dom: dom, Server: r.Server}
	if err := j.describe(r.Disk, r.Incremental != ""); err != nil {
		return nil, errors.Join(err, j.End())
	}
	return j, nil
}

func (j *Job) describe(disk string, incremental bool) error {
	x, err := j.c.l.DomainBackupGetXMLDesc(j.dom, 0)
	if err != nil {
		return fmt.Errorf("backup XML: %w", err)
	}
	j.XML = x
	var b struct {
		Disks []struct {
			Name         string `xml:"name,attr"`
			Backup       string `xml:"backup,attr"`
			ExportName   string `xml:"exportname,attr"`
			ExportBitmap string `xml:"exportbitmap,attr"`
		} `xml:"disks>disk"`
	}
	if err := xml.Unmarshal([]byte(x), &b); err != nil {
		return fmt.Errorf("backup XML: %w", err)
	}
	for _, d := range b.Disks {
		if d.Name != disk {
			continue
		}
		j.Export = d.ExportName
		if j.Export == "" {
			j.Export = disk // libvirt's default export name is the target
		}
		if incremental {
			j.Bitmap = d.ExportBitmap
			if j.Bitmap == "" {
				j.Bitmap = "backup-" + disk
			}
		}
		return nil
	}
	return fmt.Errorf("backup XML has no disk %q:\n%s", disk, x)
}

// Paused reports whether the domain is paused.
func (j *Job) Paused() (bool, error) {
	st, _, err := j.c.l.DomainGetState(j.dom, 0)
	return libvirt.DomainState(st) == libvirt.DomainPaused, err
}

// Resume resumes the paused domain.
func (j *Job) Resume() error { return j.c.l.DomainResume(j.dom) }

// End ends the pull-mode job (virDomainAbortJob); the checkpoint stays.
func (j *Job) End() error {
	if err := j.c.l.DomainAbortJob(j.dom); err != nil {
		return fmt.Errorf("end backup job: %w", err)
	}
	return nil
}

type diskTarget struct {
	Device, Target string
}

func diskTargets(domXML string) ([]diskTarget, error) {
	var d struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"devices>disk"`
	}
	if err := xml.Unmarshal([]byte(domXML), &d); err != nil {
		return nil, fmt.Errorf("domain XML: %w", err)
	}
	var out []diskTarget
	for _, x := range d.Disks {
		out = append(out, diskTarget{x.Device, x.Target.Dev})
	}
	return out, nil
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s)) // also escapes ' and ", so the result fits in either attribute quote
	return b.String()
}

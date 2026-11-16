package libvirtbackup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"path"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// ConnectDialer connects to libvirtd through dial, e.g. a unix socket forwarded over SSH (ADR-007 §3).
func ConnectDialer(dial func() (net.Conn, error)) (*Conn, error) {
	l := libvirt.NewWithDialer(dialerFunc(dial))
	if err := l.ConnectToURI(libvirt.QEMUSystem); err != nil {
		return nil, fmt.Errorf("libvirt connect: %w", err)
	}
	return &Conn{l: l}, nil
}

type dialerFunc func() (net.Conn, error)

func (f dialerFunc) Dial() (net.Conn, error) { return f() }

// Domains lists the names of all domains.
func (c *Conn) Domains() ([]string, error) {
	ds, _, err := c.l.ConnectListAllDomains(1, 0)
	if err != nil {
		return nil, err
	}
	out := []string{} // none is an empty list, not null, in the API
	for _, d := range ds {
		out = append(out, d.Name)
	}
	slices.Sort(out)
	return out, nil
}

// Disk is a disk of a domain that can be backed up.
type Disk struct {
	Target   string `json:"target"`
	Source   string `json:"source"`
	Format   string `json:"format"`
	Capacity uint64 `json:"capacity"`
}

// Disks returns the domain's disks that pull-mode backup can read: device "disk", a file source, not read-only.
// CD-ROMs, read-only and network disks are left out.
func (c *Conn) Disks(domain string) ([]Disk, string, error) {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return nil, "", fmt.Errorf("domain %q: %w", domain, err)
	}
	x, err := c.l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		return nil, "", err
	}
	var d struct {
		UUID  string `xml:"uuid"`
		Disks []struct {
			Type   string `xml:"type,attr"`
			Device string `xml:"device,attr"`
			Driver struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
			ReadOnly *struct{} `xml:"readonly"`
		} `xml:"devices>disk"`
	}
	if err := xml.Unmarshal([]byte(x), &d); err != nil {
		return nil, "", fmt.Errorf("domain XML: %w", err)
	}
	var out []Disk
	for _, k := range d.Disks {
		if k.Device != "disk" || k.Type != "file" || k.ReadOnly != nil || k.Source.File == "" {
			continue
		}
		_, capacity, _, err := c.l.DomainGetBlockInfo(dom, k.Target.Dev, 0)
		if err != nil {
			return nil, "", fmt.Errorf("disk %s: block info: %w", k.Target.Dev, err)
		}
		out = append(out, Disk{Target: k.Target.Dev, Source: k.Source.File, Format: k.Driver.Type, Capacity: capacity})
	}
	return out, d.UUID, nil
}

// Machine is a domain's virtual hardware, as far as a restore test must copy it (JSON as in verify.Machine).
type Machine struct {
	Arch      string `json:"arch"`
	Firmware  string `json:"firmware"` // bios or efi
	MemoryMiB int    `json:"memory_mib"`
	VCPUs     int    `json:"vcpus"`
	DiskBus   string `json:"disk_bus"`  // the first backed-up disk's bus
	NICModel  string `json:"nic_model"` // the first interface's; "" without one
	MAC       string `json:"mac"`       // the first interface's; "" without one
}

// Machine reads the domain's virtual hardware from its XML.
func (c *Conn) Machine(domain string) (Machine, error) {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return Machine{}, fmt.Errorf("domain %q: %w", domain, err)
	}
	x, err := c.l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		return Machine{}, err
	}
	return parseMachine(x)
}

func parseMachine(x string) (Machine, error) {
	var d struct {
		Memory struct {
			Unit  string `xml:"unit,attr"`
			Value uint64 `xml:",chardata"`
		} `xml:"memory"`
		VCPU int `xml:"vcpu"`
		OS   struct {
			Firmware string `xml:"firmware,attr"`
			Type     struct {
				Arch string `xml:"arch,attr"`
			} `xml:"type"`
			Loader *struct {
				Type string `xml:"type,attr"`
			} `xml:"loader"`
		} `xml:"os"`
		Disks []struct {
			Type     string    `xml:"type,attr"`
			Device   string    `xml:"device,attr"`
			ReadOnly *struct{} `xml:"readonly"`
			Target   struct {
				Bus string `xml:"bus,attr"`
			} `xml:"target"`
		} `xml:"devices>disk"`
		Interfaces []struct {
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(x), &d); err != nil {
		return Machine{}, fmt.Errorf("domain XML: %w", err)
	}
	m := Machine{Arch: d.OS.Type.Arch, Firmware: "bios", VCPUs: d.VCPU}
	if d.OS.Firmware == "efi" || d.OS.Loader != nil && d.OS.Loader.Type == "pflash" {
		m.Firmware = "efi"
	}
	kib := d.Memory.Value // libvirt reports KiB unless it says otherwise
	switch d.Memory.Unit {
	case "", "KiB", "k":
	case "MiB", "M":
		kib *= 1024
	case "GiB", "G":
		kib *= 1024 * 1024
	case "b", "bytes":
		kib /= 1024
	default:
		return Machine{}, fmt.Errorf("domain XML: memory unit %q", d.Memory.Unit)
	}
	m.MemoryMiB = int(kib / 1024)
	for _, k := range d.Disks {
		if k.Device == "disk" && k.Type == "file" && k.ReadOnly == nil {
			m.DiskBus = k.Target.Bus
			break
		}
	}
	if len(d.Interfaces) > 0 {
		m.MAC, m.NICModel = d.Interfaces[0].MAC.Address, d.Interfaces[0].Model.Type
	}
	return m, nil
}

// Checkpoints lists the domain's checkpoint names.
func (c *Conn) Checkpoints(domain string) ([]string, error) {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return nil, fmt.Errorf("domain %q: %w", domain, err)
	}
	cps, _, err := c.l.DomainListAllCheckpoints(dom, 1, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, cp := range cps {
		out = append(out, cp.Name)
	}
	slices.Sort(out)
	return out, nil
}

// HasCheckpoint reports whether the domain has a checkpoint of that name.
func (c *Conn) HasCheckpoint(domain, name string) (bool, error) {
	cps, err := c.Checkpoints(domain)
	if err != nil {
		return false, err
	}
	return slices.Contains(cps, name), nil
}

// DeleteCheckpoint removes a checkpoint; libvirt merges its bitmap into its parent's, so later incrementals
// from older checkpoints stay correct.
func (c *Conn) DeleteCheckpoint(domain, name string) error {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return fmt.Errorf("domain %q: %w", domain, err)
	}
	cp, err := c.l.DomainCheckpointLookupByName(dom, name, 0)
	if err != nil {
		return fmt.Errorf("checkpoint %s: %w", name, err)
	}
	return c.l.DomainCheckpointDelete(cp, 0)
}

// DomainDir is a running domain's private directory under libvirt's QEMU state directory (base, normally
// /var/lib/libvirt/qemu): domain-<id>-<the first 20 characters of the name>, as libvirt names it
// (virDomainDefGetShortName). It is the place libvirt's per-domain AppArmor profile lets QEMU create files, so a
// pull-mode backup's NBD unix socket goes there; libvirt 9.0 does not add the socket to that profile itself.
func (c *Conn) DomainDir(domain, base string) (string, error) {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return "", fmt.Errorf("domain %q: %w", domain, err)
	}
	if dom.ID <= 0 {
		return "", fmt.Errorf("domain %q is not running (a pull-mode backup needs a running domain)", domain)
	}
	return path.Join(base, fmt.Sprintf("domain-%d-%s", dom.ID, shortName(domain))), nil
}

// shortName is libvirt's short domain name: the first 20 characters (bytes for ASCII names).
func shortName(name string) string {
	r := []rune(name)
	if len(r) > 20 {
		r = r[:20]
	}
	return string(r)
}

// AbortStaleJob ends a pull-mode backup job left on the domain whose NBD socket path starts with socketPrefix
// (a job of this program that its process did not end); it reports whether there was one. Jobs of others are
// left alone and reported as an error.
func (c *Conn) AbortStaleJob(domain, socketPrefix string) (bool, error) {
	dom, err := c.l.DomainLookupByName(domain)
	if err != nil {
		return false, fmt.Errorf("domain %q: %w", domain, err)
	}
	x, err := c.l.DomainBackupGetXMLDesc(dom, 0)
	if err != nil {
		return false, nil // no backup job
	}
	if !strings.Contains(x, "socket='"+socketPrefix) && !strings.Contains(x, `socket="`+socketPrefix) {
		return false, fmt.Errorf("domain %q has a backup job that is not Gaia's:\n%s", domain, x)
	}
	if err := c.l.DomainAbortJob(dom); err != nil {
		return true, fmt.Errorf("end the stale backup job: %w", err)
	}
	return true, nil
}

// MultiRequest is one pull-mode backup job over several disks of a domain, with one checkpoint for all of them.
type MultiRequest struct {
	Domain      string
	Disks       []string // targets; every other disk of the domain is left out
	Checkpoint  string
	Incremental string // parent checkpoint; "" for a full backup
	Server      Server
	ScratchDir  string
}

// Export is where one disk of a running job is exported.
type Export struct {
	Disk   string `json:"disk"`
	Export string `json:"export"`
	Bitmap string `json:"bitmap"` // "" for a full backup
}

// MultiJob is a running multi-disk pull-mode job.
type MultiJob struct {
	c       *Conn
	dom     libvirt.Domain
	Exports []Export
	Server  Server
	XML     string
}

// BeginMulti starts the job: one checkpoint over the given disks, each exported over NBD.
func (c *Conn) BeginMulti(r MultiRequest) (*MultiJob, error) {
	if len(r.Disks) == 0 {
		return nil, errors.New("no disks to back up")
	}
	if !path.IsAbs(r.ScratchDir) {
		return nil, fmt.Errorf("scratch directory %q must be an absolute path", r.ScratchDir)
	}
	dom, err := c.l.DomainLookupByName(r.Domain)
	if err != nil {
		return nil, fmt.Errorf("domain %q: %w", r.Domain, err)
	}
	domXML, err := c.l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		return nil, err
	}
	all, err := diskTargets(domXML)
	if err != nil {
		return nil, err
	}
	var bdisks, cdisks strings.Builder
	for _, d := range all {
		if slices.Contains(r.Disks, d.Target) {
			scratch := path.Join(r.ScratchDir, fmt.Sprintf("gaia-scratch-%s-%s-%s.qcow2", r.Domain, d.Target, r.Checkpoint))
			fmt.Fprintf(&bdisks, `<disk name='%s' backup='yes' type='file'><scratch file='%s'/></disk>`, xmlEscape(d.Target), xmlEscape(scratch))
			fmt.Fprintf(&cdisks, `<disk name='%s' checkpoint='bitmap'/>`, xmlEscape(d.Target))
		} else {
			fmt.Fprintf(&bdisks, `<disk name='%s' backup='no'/>`, xmlEscape(d.Target))
			fmt.Fprintf(&cdisks, `<disk name='%s' checkpoint='no'/>`, xmlEscape(d.Target))
		}
	}
	for _, want := range r.Disks {
		if !slices.ContainsFunc(all, func(d diskTarget) bool { return d.Target == want }) {
			return nil, fmt.Errorf("domain %q has no disk %q", r.Domain, want)
		}
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
	checkpointXML := fmt.Sprintf(`<domaincheckpoint><name>%s</name><disks>%s</disks></domaincheckpoint>`, xmlEscape(r.Checkpoint), cdisks.String())
	if err := c.l.DomainBackupBegin(dom, backupXML, libvirt.OptString{checkpointXML}, 0); err != nil {
		return nil, fmt.Errorf("backup begin: %w", err)
	}
	j := &MultiJob{c: c, dom: dom, Server: r.Server}
	x, err := c.l.DomainBackupGetXMLDesc(dom, 0)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("backup XML: %w", err), j.End())
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
		return nil, errors.Join(fmt.Errorf("backup XML: %w", err), j.End())
	}
	for _, want := range r.Disks {
		e := Export{Disk: want, Export: want}
		if r.Incremental != "" {
			e.Bitmap = "backup-" + want
		}
		for _, d := range b.Disks {
			if d.Name == want {
				if d.ExportName != "" {
					e.Export = d.ExportName
				}
				if r.Incremental != "" && d.ExportBitmap != "" {
					e.Bitmap = d.ExportBitmap
				}
			}
		}
		j.Exports = append(j.Exports, e)
	}
	return j, nil
}

// Paused reports whether the domain is paused.
func (j *MultiJob) Paused() (bool, error) {
	st, _, err := j.c.l.DomainGetState(j.dom, 0)
	return libvirt.DomainState(st) == libvirt.DomainPaused, err
}

// End ends the job; the checkpoint stays.
func (j *MultiJob) End() error {
	if err := j.c.l.DomainAbortJob(j.dom); err != nil {
		return fmt.Errorf("end backup job: %w", err)
	}
	return nil
}

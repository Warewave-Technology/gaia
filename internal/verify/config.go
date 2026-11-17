// Package verify is Gaia's verification engine v0 (T7): it boots a restored disk
// image in an isolated libvirt sandbox and runs health checks against it.
//
//   - The restored image is never written: the guest runs on a qcow2 copy-on-write
//     overlay, and the image's sha256 is taken before and after.
//   - The sandbox network is a libvirt bridge with no uplink and no host address.
//     A network namespace on that bridge serves DHCP and runs the probes, so
//     the probes run from inside the sandbox network (ADR-002 #4), and the guest
//     cannot reach the host or anything else.
//   - Checks are declared per VM in JSON, on top of a built-in template.
//   - Evidence (ADR-002 #3): console screenshots, the serial log, check results and
//     timings, sealed and signed by package evidence.
package verify

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// Check types.
const (
	CheckBootSerial = "boot_serial" // the serial console shows Pattern: the guest booted
	CheckTCP        = "tcp"         // a TCP connect to Port succeeds
	CheckHTTP       = "http"        // GET http://guest:Port/Path answers ExpectStatus (and contains ExpectBody)
	CheckRDP        = "rdp"         // an RDP server on Port answers the X.224 Connection Request (MS-RDPBCGR 1.3.1.1)
	CheckWinRM      = "winrm"       // WinRM on Port answers a WS-Management Identify with an IdentifyResponse (ADR-005 §3)
)

// Check is one declared health check.
type Check struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Pattern      string `json:"pattern,omitempty"` // boot_serial: regular expression (RE2), multi-line
	Port         int    `json:"port,omitempty"`
	Path         string `json:"path,omitempty"`
	ExpectStatus int    `json:"expect_status,omitempty"`
	ExpectBody   string `json:"expect_body,omitempty"`
	// Boot makes a tcp, http, rdp or winrm check the boot check, for guests without a serial console
	// (Windows): it runs from the moment the guest takes a DHCP lease, within the boot timeout.
	Boot bool `json:"boot,omitempty"`
}

// IsBoot reports whether k is the check that decides that the guest booted.
func (k Check) IsBoot() bool { return k.Type == CheckBootSerial || k.Boot }

// Machine is the virtual hardware of the sandbox guest. It should match the source VM,
// MAC address included: guests often bind their network configuration to it.
type Machine struct {
	Arch      string `json:"arch"`     // x86_64 or aarch64
	Firmware  string `json:"firmware"` // bios or efi
	MemoryMiB int    `json:"memory_mib"`
	VCPUs     int    `json:"vcpus"`
	DiskBus   string `json:"disk_bus"` // virtio, sata, scsi
	NICModel  string `json:"nic_model"`
	MAC       string `json:"mac"`
}

// Config declares how one VM is verified.
type Config struct {
	VM               string  `json:"vm"`
	Template         string  `json:"template"`
	Machine          Machine `json:"machine"`
	BootTimeoutS     int     `json:"boot_timeout_s"`
	ProbeTimeoutS    int     `json:"probe_timeout_s"`
	ShutdownTimeoutS int     `json:"shutdown_timeout_s"`
	Checks           []Check `json:"checks"`
}

// Templates are the built-in check sets (ADR-002 #4: templates first, user probes on top).
var Templates = map[string][]Check{
	// A Linux guest that reaches a login prompt on the serial console and answers on SSH.
	"linux-basic": {
		{Name: "boot", Type: CheckBootSerial, Pattern: `(?m)login: *$`},
		{Name: "ssh", Type: CheckTCP, Port: 22},
	},
	// Only that the guest boots to a serial login prompt.
	"linux-boot": {
		{Name: "boot", Type: CheckBootSerial, Pattern: `(?m)login: *$`},
	},
	// A Windows guest has no serial login prompt: it booted when WinRM, on by default in Windows Server, answers a
	// WS-Management Identify on the address the guest leased (ADR-005 §3). Remote Desktop is off by default.
	"windows-basic": {
		{Name: "boot", Type: CheckWinRM, Port: 5985, Boot: true},
	},
	// windows-basic, and Remote Desktop answers the RDP handshake: for guests where RDP is enabled.
	"windows-rdp": {
		{Name: "boot", Type: CheckWinRM, Port: 5985, Boot: true},
		{Name: "rdp", Type: CheckRDP, Port: 3389},
	},
}

// LoadConfig reads a JSON config, applies defaults and the template, and validates it.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate applies defaults and the template to a config as declared, and checks it (what LoadConfig does after
// reading the file). Call it once: the template's checks are added to Checks.
func (c *Config) Validate() error { return c.resolve() }

func (c *Config) resolve() error {
	m := &c.Machine
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	defInt := func(n *int, v int) {
		if *n == 0 {
			*n = v
		}
	}
	def(&m.Arch, "x86_64")
	def(&m.Firmware, "bios")
	def(&m.DiskBus, "virtio")
	def(&m.NICModel, "virtio")
	defInt(&m.MemoryMiB, 1024)
	defInt(&m.VCPUs, 2)
	defInt(&c.BootTimeoutS, 300)
	defInt(&c.ProbeTimeoutS, 120)
	defInt(&c.ShutdownTimeoutS, 60)
	if c.VM == "" {
		return fmt.Errorf("vm is required")
	}
	if m.Arch != "x86_64" && m.Arch != "aarch64" {
		return fmt.Errorf("machine.arch %q", m.Arch)
	}
	if m.Firmware != "bios" && m.Firmware != "efi" {
		return fmt.Errorf("machine.firmware %q", m.Firmware)
	}
	if m.MAC == "" {
		return fmt.Errorf("machine.mac is required (use the source VM's)")
	}
	if _, err := net.ParseMAC(m.MAC); err != nil {
		return fmt.Errorf("machine.mac: %w", err)
	}
	var checks []Check
	if c.Template != "" {
		t, ok := Templates[c.Template]
		if !ok {
			return fmt.Errorf("unknown template %q", c.Template)
		}
		checks = append(checks, t...)
	}
	checks = append(checks, c.Checks...)
	c.Checks = checks
	seen := map[string]bool{}
	boots := 0
	for i := range c.Checks {
		k := &c.Checks[i]
		if k.Name == "" || seen[k.Name] {
			return fmt.Errorf("check %d: name %q missing or duplicate", i, k.Name)
		}
		seen[k.Name] = true
		if k.IsBoot() {
			boots++
		}
		switch k.Type {
		case CheckBootSerial:
			if _, err := regexp.Compile(k.Pattern); err != nil || k.Pattern == "" {
				return fmt.Errorf("check %s: pattern: %v", k.Name, err)
			}
		case CheckTCP, CheckHTTP, CheckRDP, CheckWinRM:
			if k.Port < 1 || k.Port > 65535 {
				return fmt.Errorf("check %s: port %d", k.Name, k.Port)
			}
			if k.Type == CheckHTTP {
				if k.Path == "" {
					k.Path = "/"
				}
				if k.ExpectStatus == 0 {
					k.ExpectStatus = 200
				}
			}
		default:
			return fmt.Errorf("check %s: unknown type %q", k.Name, k.Type)
		}
	}
	if boots != 1 {
		return fmt.Errorf("exactly one boot check (%s, or a probe with boot: true) is required, have %d", CheckBootSerial, boots)
	}
	return nil
}

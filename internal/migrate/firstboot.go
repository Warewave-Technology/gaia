package migrate

import (
	"fmt"
	"slices"
	"strings"
)

// DriverLoop is a driver that virt-v2v's first-boot service failed to install on every boot it tried, each time
// ending with "retrying on next boot": the guest restarts for it for ever.
type DriverLoop struct {
	Driver   string `json:"driver"`   // its .inf name without .inf (smbus)
	Failures int    `json:"failures"` // boots on which its install failed
	Boots    int    `json:"boots"`    // boots the service started on, in the log
	Why      string `json:"why"`      // pnputil's own words, from the latest failure
}

// DriverInstallLoops reads virt-v2v's first-boot log (C:\Program Files\Guestfs\Firstboot\log.txt) and returns the
// drivers stuck in a loop. The service writes, on every boot,
//
//	starting firstboot service
//	running "...\2000-0001-pnputil-install-drivers.bat"
//	Installing: smbus.inf.
//	Adding the driver package failed : A certificate chain processed, but terminated in a root certificate ...
//	Failed to install smbus.inf.
//	Script finished with exit code 249, retrying on next boot
//
// A driver is in a loop when its install failed on at least two boots that each ended in a retry, and on the latest
// boot that installed drivers (a driver that failed once and then went in is not). A log with fewer boots says
// nothing (the guest may have restarted for another reason).
func DriverInstallLoops(log string) []DriverLoop {
	type boot struct {
		failed map[string]string // driver -> why
		retry  bool
		ran    bool // the driver script ran on this boot
	}
	var boots []*boot
	var cur *boot
	installing, why := "", ""
	for _, l := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "starting firstboot service":
			cur = &boot{failed: map[string]string{}}
			boots = append(boots, cur)
			installing, why = "", ""
		case cur == nil:
		case strings.HasPrefix(l, "running ") && strings.Contains(l, "pnputil-install-drivers"):
			cur.ran = true
		case strings.HasPrefix(l, "Installing: ") && strings.HasSuffix(l, ".inf."):
			installing, why = strings.TrimSuffix(strings.TrimPrefix(l, "Installing: "), ".inf."), ""
		case strings.HasPrefix(l, "Adding the driver package failed") || strings.HasPrefix(l, "Failed to install the driver"):
			if why == "" || strings.HasPrefix(l, "Adding") { // the package's reason says more than the device's
				why = l
			}
		case strings.HasPrefix(l, "Failed to install ") && strings.HasSuffix(l, ".inf."):
			d := strings.TrimSuffix(strings.TrimPrefix(l, "Failed to install "), ".inf.")
			if d == installing || installing == "" {
				cur.failed[d] = why
			}
		case strings.HasPrefix(l, "Script finished with exit code") && strings.Contains(l, "retrying on next boot"):
			cur.retry = true
		}
	}
	var last *boot
	for _, b := range boots {
		if b.ran {
			last = b
		}
	}
	if last == nil {
		return nil
	}
	var out []DriverLoop
	seen := map[string]bool{}
	for _, b := range boots {
		for d := range b.failed {
			if seen[d] {
				continue
			}
			seen[d] = true
			n := 0
			for _, c := range boots {
				if _, ok := c.failed[d]; ok && c.retry {
					n++
				}
			}
			if _, now := last.failed[d]; n >= 2 && now && last.retry {
				out = append(out, DriverLoop{Driver: d, Failures: n, Boots: len(boots), Why: last.failed[d]})
			}
		}
	}
	// in the order the service installs them (alphabetical, as pnputil reads the folder)
	slices.SortFunc(out, func(a, b DriverLoop) int { return strings.Compare(a.Driver, b.Driver) })
	return out
}

// DriverLoopText says what the loop is and what to do, in one line.
func DriverLoopText(loops []DriverLoop) string {
	var parts []string
	names := make([]string, len(loops))
	for i, l := range loops {
		names[i] = l.Driver
		why := l.Why
		if why == "" {
			why = "no reason in the log"
		}
		parts = append(parts, fmt.Sprintf("%s failed to install on %d of %d boots (%s)", l.Driver, l.Failures, l.Boots, why))
	}
	return fmt.Sprintf("Windows restarts on every boot: virt-v2v's first-boot service cannot install virtio-win's %s driver and "+
		"retries it on the next boot: %s. Check again with exclude_drivers %q if the VM does not need it",
		strings.Join(names, ", "), strings.Join(parts, "; "), names)
}

// Package buildinfo is what a build of gaia says about itself: its release version and its source revision.
package buildinfo

import "runtime/debug"

// Version is the release version (for example 0.1.0), set by the package build (scripts/release.sh) with
// -ldflags "-X github.com/Warewave-Technology/gaia/internal/buildinfo.Version=..."; empty in other builds.
var Version string

// Revision is the source revision the binary was built from (12 hex digits, "+modified" for a tree with changes),
// "devel" without version control information, or "unknown".
func Revision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, mod := "", ""
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			mod = kv.Value
		}
	}
	if rev == "" {
		return "devel"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if mod == "true" {
		rev += "+modified"
	}
	return rev
}

// String is the version gaia reports: "0.1.0 (a1b2c3d4e5f6)" for a package build, the revision alone otherwise.
func String() string {
	if Version == "" {
		return Revision()
	}
	return Version + " (" + Revision() + ")"
}

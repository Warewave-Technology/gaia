// Package version holds the build version of the gaia binaries.
package version

// Version is set at build time:
//
//	-ldflags "-X github.com/Warewave-Technology/gaia/internal/version.Version=v0.1.0"
var Version = "dev"

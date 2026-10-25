// Package access holds Gaia's own Ansible roles: distributing access
// (design 14.4) and joining the domain (design 14.6). Runs add them to the
// project they hand ansible-runner.
package access

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed roles
var roles embed.FS

// Group is the group of the accounts Gaia manages on a server.
const Group = "gaia-users"

// Role is the access role's name in a playbook.
const Role = "gaia_access"

// JoinRole joins a server to Active Directory with a one-time password.
const JoinRole = "gaia_domain_join"

// User is one account to give access.
type User struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
	Sudo bool     `json:"sudo"` // every command
	// Commands are the only commands they may run as root, when Sudo is
	// not set; empty without sudo.
	Commands []string `json:"commands"`
}

// WriteRoles writes the role into dir/roles.
func WriteRoles(dir string) error {
	return fs.WalkDir(roles, "roles", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, err := roles.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
}

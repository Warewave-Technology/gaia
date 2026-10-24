// Package access holds Gaia's own Ansible role for distributing access
// (design 14.4); runs add it to the project they hand ansible-runner.
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

// Role is the role's name in a playbook.
const Role = "gaia_access"

// User is one account to give access.
type User struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
	Sudo bool     `json:"sudo"`
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

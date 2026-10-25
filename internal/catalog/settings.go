package catalog

import (
	"path"
	"sort"
	"strings"
)

// settingKinds are Gaia's settings (design 17.1): kept in Gaia's settings
// store, not in Git, once the store is in use. Git keeps the contracts
// and what is installed on servers.
var settingKinds = map[string]bool{
	KindSite: true, KindNetwork: true, KindImage: true, KindSize: true,
	KindGroup: true, KindTeam: true, KindPolicy: true, KindPlatform: true,
	KindAuthConfig: true, KindProvider: true,
	KindTeamSource: true, KindNetworkSrc: true, KindRoleSource: true,
}

// IsSetting reports whether objects of a kind are Gaia's settings.
func IsSetting(kind string) bool { return settingKinds[kind] }

// SettingKinds lists the settings kinds in a stable order.
func SettingKinds() []string {
	out := make([]string, 0, len(settingKinds))
	for k := range settingKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SettingsPrefix starts the path of every file of the settings store, so
// that messages and pages say where an object is.
const SettingsPrefix = "settings:"

// SettingsPath is where a settings object is kept: one file per object,
// and one per patch of a generated object.
func SettingsPath(kind, name string, patch bool) string {
	if patch {
		return SettingsPrefix + kind + "/" + name + ".patch.yaml"
	}
	return SettingsPrefix + kind + "/" + name + ".yaml"
}

// FromSettings reports whether a file is one of the settings store's.
func FromSettings(p string) bool { return strings.HasPrefix(p, SettingsPrefix) }

// SettingsFileKind is the kind a settings file is under.
func SettingsFileKind(p string) string {
	return path.Dir(strings.TrimPrefix(p, SettingsPrefix))
}

// Ignored is an object of a settings kind found in Git while the settings
// store is in use: it is left out of the desired state.
type Ignored struct {
	Key    Key
	Source Source
}

package catalog

import (
	"reflect"
	"strings"
)

// Field describes one field of a kind's spec, for forms and tables: its
// YAML key, its type, whether it is required, and the values it takes.
type Field struct {
	Key      string   `json:"key,omitempty"`
	Type     string   `json:"type"` // string | int | float | bool | object | list | map | any
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	// Ref is the kind a string field names an object of.
	Ref string `json:"ref,omitempty"`
	// Fields are an object's; Items is a list's element or a map's value.
	Fields []Field `json:"fields,omitempty"`
	Items  *Field  `json:"items,omitempty"`
}

// enums are the closed sets of values some fields take, by kind and path.
var enums = map[string][]string{
	"Provider.type":                   ProviderTypes,
	"AuthConfig.require":              {"mfa", "passkey"},
	"AuthConfig.providers[].type":     {"ldap", "local"},
	"Image.os":                        {"linux", "windows"},
	"Image.connection":                {"ssh", "winrm"},
	"Baseline.os":                     {"linux", "windows"},
	"Policy.environments{}.approval":  {"required", "none"},
	"Team.notifications[].triggers[]": Triggers,
}

// refs are string fields that name another object.
var refs = map[string]string{
	"Network.site": KindSite, "Network.ipam.provider": KindProvider, "Network.dns.provider": KindProvider, "Network.domain.provider": KindProvider,
	"Site.compute.provider": KindProvider, "Image.templates[].provider": KindProvider,
	"Group.owner": KindTeam, "Group.teams[]": KindTeam, "Group.groups[]": KindGroup,
	"AuthConfig.providers[].directory.provider": KindProvider,
	"TeamSource.provider":                       KindProvider, "NetworkSource.provider": KindProvider, "AnsibleRoleSource.provider": KindProvider,
	"Platform.secrets": KindProvider, "Platform.runArchive": KindProvider, "Platform.runLogs": KindProvider,
	"Agent.ansibleRole": KindAnsibleRole, "App.requires[]": KindAnsibleRole,
	"Platform.monitoring.provider": KindProvider, "Platform.bastion.provider": KindProvider, "Platform.contracts[].provider": KindProvider,
	"Platform.notifications.email": KindProvider, "Platform.engines.configuration.source": KindProvider,
	"Team.adGroup.provider": KindProvider, "Team.approversGroup.provider": KindProvider, "Team.notifications[].provider": KindProvider,
}

// Schema is a kind's spec as fields; nil for an unknown kind.
func Schema(kind string) *Field {
	mk, ok := newSpec[kind]
	if !ok {
		return nil
	}
	f := fieldOf(reflect.TypeOf(mk()), kind, map[reflect.Type]bool{})
	return &f
}

func fieldOf(t reflect.Type, path string, seen map[reflect.Type]bool) Field {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Name() {
	case "TemplateRef": // a name or a Proxmox VM ID
		return Field{Type: "object", Fields: []Field{{Key: "name", Type: "string"}, {Key: "vmid", Type: "int"}}}
	case "Remediation", "Sudo", "RoleRef", "Param", "Node":
		return Field{Type: "any"}
	}
	f := Field{Enum: enums[path], Ref: refs[path]}
	switch t.Kind() {
	case reflect.String:
		f.Type = "string"
	case reflect.Bool:
		f.Type = "bool"
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
		f.Type = "int"
	case reflect.Float32, reflect.Float64:
		f.Type = "float"
	case reflect.Slice:
		f.Type = "list"
		it := fieldOf(t.Elem(), path+"[]", seen)
		f.Items = &it
		f.Enum, f.Ref = nil, ""
	case reflect.Map:
		f.Type = "map"
		it := fieldOf(t.Elem(), path+"{}", seen)
		f.Items = &it
	case reflect.Struct:
		if seen[t] {
			return Field{Type: "any"}
		}
		seen[t] = true
		defer delete(seen, t)
		f.Type = "object"
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			key, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
			if key == "-" || key == "" {
				continue
			}
			child := fieldOf(sf.Type, path+"."+key, seen)
			child.Key = key
			child.Required = strings.Contains(sf.Tag.Get("gaia"), "required")
			f.Fields = append(f.Fields, child)
		}
	default:
		f.Type = "any"
	}
	return f
}

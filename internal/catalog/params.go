package catalog

import (
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Formats a param may require (design 6.2).
var paramFormats = map[string]func(string) bool{
	"semver": regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`).MatchString,
	"ipv4": func(s string) bool {
		ip := net.ParseIP(s)
		return ip != nil && ip.To4() != nil && !strings.Contains(s, ":")
	},
	"cidr":     func(s string) bool { _, _, err := net.ParseCIDR(s); return err == nil },
	"hostname": hostnameRe.MatchString,
	"fqdn":     isFQDN,
	"duration": func(s string) bool { _, err := time.ParseDuration(s); return err == nil },
}

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func isFQDN(s string) bool {
	s = strings.TrimSuffix(strings.ToLower(s), ".")
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(s) > 253 {
		return false
	}
	for _, p := range parts {
		if !hostnameRe.MatchString(p) {
			return false
		}
	}
	return true
}

// checkParamSchema checks what can be checked without a value: the
// pattern compiles, the format is known, min <= max, and the default fits.
func checkParamSchema(path string, p Param) []string {
	var out []string
	if g := p.Gaia; g != nil {
		if g.Pattern != "" {
			if _, err := regexp.Compile(g.Pattern); err != nil {
				out = append(out, fmt.Sprintf("%s.gaia.pattern: %v", path, err))
			}
		}
		if g.Format != "" && paramFormats[g.Format] == nil {
			out = append(out, fmt.Sprintf("%s.gaia.format: %q is unknown (one of %s)", path, g.Format, strings.Join(sortedKeys(paramFormats), ", ")))
		}
		if g.Min != nil && g.Max != nil && *g.Min > *g.Max {
			out = append(out, path+".gaia: min is greater than max")
		}
		if g.Ref != "" && newSpec[g.Ref] == nil {
			out = append(out, fmt.Sprintf("%s.gaia.ref: unknown kind %q", path, g.Ref))
		}
	}
	if p.Default != nil && len(out) == 0 && (p.Gaia == nil || !p.Gaia.Secret) {
		out = append(out, checkValue(path+".default", p, p.Default)...)
	}
	return out
}

// checkValue checks one value against its param: type, elements and
// options, choices and Gaia's extensions.
func checkValue(path string, p Param, v any) []string {
	t := p.Type
	if t == "" {
		t = "str"
	}
	g := p.Gaia
	if g == nil {
		g = &ParamExt{}
	}
	var out []string
	fail := func(format string, a ...any) { out = append(out, path+": "+fmt.Sprintf(format, a...)) }

	switch t {
	case "str", "path":
		s, ok := v.(string)
		if !ok {
			fail("want a string, got %s", describe(v))
			return out
		}
		if g.Secret && !strings.HasPrefix(s, "bao://") {
			fail("a secret must be a bao:// reference, not a value")
			return out
		}
		checkRange(fail, g, float64(len([]rune(s))), "length")
		if g.Pattern != "" {
			if re, err := regexp.Compile(g.Pattern); err == nil && !re.MatchString(s) {
				fail("%q does not match %s", s, g.Pattern)
			}
		}
		if f := paramFormats[g.Format]; f != nil && !f(s) {
			fail("%q is not a valid %s", s, g.Format)
		}
	case "int":
		n, ok := v.(int)
		if !ok {
			fail("want an int, got %s", describe(v))
			return out
		}
		checkRange(fail, g, float64(n), "value")
	case "float":
		var f float64
		switch x := v.(type) {
		case int:
			f = float64(x)
		case float64:
			f = x
		default:
			fail("want a number, got %s", describe(v))
			return out
		}
		checkRange(fail, g, f, "value")
	case "bool":
		if _, ok := v.(bool); !ok {
			fail("want a bool, got %s", describe(v))
		}
	case "list":
		items, ok := v.([]any)
		if !ok {
			fail("want a list, got %s", describe(v))
			return out
		}
		checkRange(fail, g, float64(len(items)), "length")
		if p.Elements != "" {
			elem := Param{Type: p.Elements, Options: p.Options}
			for i, item := range items {
				out = append(out, checkValue(fmt.Sprintf("%s[%d]", path, i), elem, item)...)
			}
		}
	case "dict":
		m, ok := v.(map[string]any)
		if !ok {
			fail("want a mapping, got %s", describe(v))
			return out
		}
		if len(p.Options) > 0 {
			out = append(out, checkOptions(path, p.Options, m)...)
		}
	}

	if len(p.Choices) > 0 && len(out) == 0 && !inChoices(v, p.Choices) {
		fail("%v is not one of %v", v, p.Choices)
	}
	return out
}

// checkOptions checks a mapping against named params: no unknown keys,
// required ones present, each value valid.
func checkOptions(path string, opts map[string]Param, m map[string]any) []string {
	var out []string
	for _, k := range sortedKeys(m) {
		o, ok := opts[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s: unknown key %q%s", path, k, nearest(k, sortedKeys(opts))))
			continue
		}
		out = append(out, checkValue(path+"."+k, o, m[k])...)
	}
	for _, k := range sortedKeys(opts) {
		if _, given := m[k]; opts[k].Required && opts[k].Default == nil && !given {
			out = append(out, fmt.Sprintf("%s: missing required key %q", path, k))
		}
	}
	return out
}

func checkRange(fail func(string, ...any), g *ParamExt, n float64, what string) {
	if g.Min != nil && n < *g.Min {
		fail("%s %v is less than %v", what, n, *g.Min)
	}
	if g.Max != nil && n > *g.Max {
		fail("%s %v is greater than %v", what, n, *g.Max)
	}
}

func inChoices(v any, choices []any) bool {
	for _, c := range choices {
		if reflect.DeepEqual(v, c) {
			return true
		}
	}
	return false
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case int, float64:
		return "a number"
	case bool:
		return "a bool"
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return fmt.Sprintf("%T", v)
}

// effectiveParams is given over defaults, as the role will see them.
func effectiveParams(schema map[string]Param, given map[string]any) map[string]any {
	out := map[string]any{}
	for k, p := range schema {
		if p.Default != nil {
			out[k] = p.Default
		}
	}
	for k, v := range given {
		out[k] = v
	}
	return out
}

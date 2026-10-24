package catalog

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// posError is a problem at a position in the document.
type posError struct {
	Line, Column int
	Msg          string
}

type posErrors []posError

func (e posErrors) Error() string {
	parts := make([]string, len(e))
	for i, p := range e {
		parts[i] = fmt.Sprintf("line %d: %s", p.Line, p.Msg)
	}
	return strings.Join(parts, "; ")
}

var (
	unmarshalerType = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()
	nodeType        = reflect.TypeOf(yaml.Node{})
	typeErrLine     = regexp.MustCompile(`^line (\d+): (.*)$`)
)

// decodeStrict decodes n into v and rejects unknown fields and missing
// required ones (gaia:"required"), reporting each with its position.
// yaml.v3 can be strict only through a Decoder, which loses the positions
// of nested documents; walking the node keeps them.
func decodeStrict(n *yaml.Node, v any) error { return decodeStrictAt(n, v, "") }

// decodeStrictAt is decodeStrict with messages prefixed by path.
func decodeStrictAt(n *yaml.Node, v any, path string) error {
	var errs posErrors
	checkNode(n, reflect.TypeOf(v), path, &errs)
	if len(errs) > 0 {
		return errs
	}
	if err := n.Decode(v); err != nil {
		return toPosErrors(err, n)
	}
	return nil
}

func checkNode(n *yaml.Node, t reflect.Type, path string, errs *posErrors) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// Types with their own UnmarshalYAML check themselves; yaml.Node is
	// decoded later.
	if t == nodeType || t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
		return
	}
	isNull := n.Kind == yaml.ScalarNode && n.Tag == "!!null"
	add := func(at *yaml.Node, format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		if path != "" {
			msg = path + ": " + msg
		}
		*errs = append(*errs, posError{Line: at.Line, Column: at.Column, Msg: msg})
	}

	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			if !isNull {
				add(n, "expected a mapping")
			}
			return
		}
		fields, required := structFields(t)
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			f, ok := fields[key.Value]
			if !ok {
				add(key, "unknown field %q", key.Value)
				continue
			}
			seen[key.Value] = true
			checkNode(val, f.Type, join(path, key.Value), errs)
		}
		for _, name := range required {
			if !seen[name] {
				add(n, "missing required field %q", name)
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			if !isNull {
				add(n, "expected a mapping")
			}
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			checkNode(n.Content[i+1], t.Elem(), join(path, n.Content[i].Value), errs)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			if !isNull {
				add(n, "expected a list")
			}
			return
		}
		for i, item := range n.Content {
			checkNode(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i), errs)
		}
	}
	// Scalars and `any` are left to Decode, which reports type errors with
	// their line.
}

// structFields maps yaml names to fields and lists the required ones.
func structFields(t reflect.Type) (map[string]reflect.StructField, []string) {
	fields := map[string]reflect.StructField{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f
		if f.Tag.Get("gaia") == "required" {
			required = append(required, name)
		}
	}
	return fields, required
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// toPosErrors keeps the line numbers of yaml.v3's type errors.
func toPosErrors(err error, n *yaml.Node) posErrors {
	switch e := err.(type) {
	case posErrors:
		return e
	case *yaml.TypeError:
		var out posErrors
		for _, msg := range e.Errors {
			if m := typeErrLine.FindStringSubmatch(msg); m != nil {
				line, _ := strconv.Atoi(m[1])
				out = append(out, posError{Line: line, Msg: m[2]})
				continue
			}
			out = append(out, posError{Line: n.Line, Msg: msg})
		}
		return out
	}
	return posErrors{{Line: n.Line, Column: n.Column, Msg: err.Error()}}
}

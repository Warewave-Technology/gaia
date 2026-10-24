package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// File is one file of a commit.
type File struct {
	Path string
	Data []byte
}

// Error is one problem found while loading, with its position.
type Error struct {
	File   string
	Line   int
	Column int
	Object string // Kind/name, when known
	Msg    string
}

func (e Error) Error() string {
	var b strings.Builder
	if e.File != "" {
		b.WriteString(e.File)
		if e.Line > 0 {
			fmt.Fprintf(&b, ":%d", e.Line)
			if e.Column > 0 {
				fmt.Fprintf(&b, ":%d", e.Column)
			}
		}
		b.WriteString(": ")
	}
	if e.Object != "" {
		b.WriteString(e.Object + ": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors are all problems of one load, sorted by position.
type Errors []Error

func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, err := range e {
		lines[i] = err.Error()
	}
	return strings.Join(lines, "\n")
}

var (
	// Names are DNS-label-like; dots are allowed for AnsibleRole names
	// such as web.nginx.
	nameRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$`)
	labelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9._/]{0,61}[a-z0-9])?$`)
)

type envelope struct {
	APIVersion string    `yaml:"apiVersion" gaia:"required"`
	Kind       string    `yaml:"kind" gaia:"required"`
	Metadata   Metadata  `yaml:"metadata" gaia:"required"`
	Spec       yaml.Node `yaml:"spec" gaia:"required"`
}

type validator interface{ validate() []string }

// Load reads every .yaml and .yml file (outside hidden directories) as
// Gaia objects. Each file may hold several documents. It reports every
// problem at once; when the returned error is non-nil the catalog must
// not be used to apply anything.
func Load(commit string, files []File) (*Catalog, error) {
	cat := newCatalog(commit)
	var errs Errors
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, f := range sorted {
		if !isCatalogFile(f.Path) {
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(f.Data))
		for {
			var doc yaml.Node
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				errs = append(errs, parseError(f.Path, err))
				break
			}
			root := documentRoot(&doc)
			if root == nil {
				continue
			}
			obj, objErrs := decodeObject(f.Path, root)
			errs = append(errs, objErrs...)
			if obj == nil {
				continue
			}
			if prev, dup := cat.objects[obj.Key()]; dup {
				errs = append(errs, Error{File: f.Path, Line: obj.Source.Line, Object: obj.Key().String(),
					Msg: "defined more than once (also at " + prev.Source.String() + ")"})
				continue
			}
			cat.objects[obj.Key()] = obj
		}
	}

	if len(errs) > 0 {
		sortErrors(errs)
		return cat, errs
	}
	return cat, nil
}

func isCatalogFile(p string) bool {
	for _, part := range strings.Split(path.Dir(p), "/") {
		if strings.HasPrefix(part, ".") && part != "." {
			return false
		}
	}
	ext := path.Ext(p)
	return ext == ".yaml" || ext == ".yml"
}

// documentRoot returns the document's content, or nil for an empty one.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return nil
	}
	return root
}

func decodeObject(file string, root *yaml.Node) (*Object, Errors) {
	var env envelope
	if err := decodeStrict(root, &env); err != nil {
		return nil, fileErrors(file, "", toPosErrors(err, root))
	}
	obj := &Object{
		APIVersion: env.APIVersion,
		Kind:       env.Kind,
		Metadata:   env.Metadata,
		Source:     Source{File: file, Line: root.Line},
		node:       root,
	}
	id := env.Kind + "/" + env.Metadata.Name
	at := func(line int, format string, a ...any) Error {
		return Error{File: file, Line: line, Object: id, Msg: fmt.Sprintf(format, a...)}
	}

	var errs Errors
	if env.APIVersion != APIVersion {
		errs = append(errs, at(root.Line, "apiVersion %q is not supported (%s)", env.APIVersion, APIVersion))
	}
	mk, known := newSpec[env.Kind]
	if !known {
		errs = append(errs, at(root.Line, "unknown kind %q (known: %s)", env.Kind, strings.Join(Kinds(), ", ")))
	}
	if !nameRe.MatchString(env.Metadata.Name) {
		errs = append(errs, at(root.Line, "metadata.name %q: lowercase letters, digits, '-' and '.', at most 63 characters", env.Metadata.Name))
	}
	for _, k := range sortedKeys(env.Metadata.Labels) {
		if !labelRe.MatchString(k) {
			errs = append(errs, at(root.Line, "metadata.labels: invalid key %q", k))
		}
	}
	if env.Metadata.Annotations["gaia/mode"] == "patch" {
		errs = append(errs, at(root.Line, "patch objects apply to generated objects, which this version does not load yet"))
	}
	if !known {
		return nil, errs
	}

	spec := mk()
	if err := decodeStrictAt(&env.Spec, spec, "spec"); err != nil {
		errs = append(errs, fileErrors(file, id, toPosErrors(err, &env.Spec))...)
		return nil, errs
	}
	if v, ok := spec.(validator); ok {
		for _, msg := range v.validate() {
			errs = append(errs, at(env.Spec.Line, "spec.%s", msg))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	obj.Spec = spec
	return obj, nil
}

func fileErrors(file, object string, errs posErrors) Errors {
	out := make(Errors, len(errs))
	for i, e := range errs {
		out[i] = Error{File: file, Line: e.Line, Column: e.Column, Object: object, Msg: e.Msg}
	}
	return out
}

var parseLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

func parseError(file string, err error) Error {
	if m := parseLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ := strconv.Atoi(m[1])
		return Error{File: file, Line: line, Msg: m[2]}
	}
	return Error{File: file, Msg: err.Error()}
}

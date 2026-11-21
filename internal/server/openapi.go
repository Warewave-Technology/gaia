package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// OpenAPI builds the OpenAPI 3.1 description of the routes. It is deterministic: the committed openapi.json is
// this function's output (TestOpenAPIFile), and GET /api/v1/openapi.json serves it.
func OpenAPI(routes []route, _ string) ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}}
	errSchema := g.schema(reflect.TypeOf(apiError{}))
	errResp := func(desc string) map[string]any {
		return map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": errSchema}}}
	}
	paths := map[string]map[string]any{}
	for _, rt := range routes {
		op := map[string]any{
			"operationId":  operationID(rt.method, rt.path),
			"summary":      rt.summary,
			"tags":         []string{tag(rt.path)},
			"x-gaia-roles": rolesOf(rt),
		}
		if rt.example != "" {
			op["x-gaia-example"] = rt.example // a body the role × endpoint checks send
		}
		switch rt.stepUp { // ADR-008 §4: the request needs X-Gaia-Step-Up (stepup.go)
		case stepAlways:
			op["x-gaia-step-up"] = "always"
		case stepIfLoose:
			op["x-gaia-step-up"] = "when_it_weakens_protection"
		}
		if rt.roles == nil {
			op["security"] = []any{}
		}
		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(rt.path, -1) {
			schema := map[string]any{"type": "integer", "minimum": 1} // {id}: a row's ID
			if m[1] != "id" {
				schema = map[string]any{"type": "string"} // {name} of a file, {sid} of a step-up
			}
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": schema})
		}
		for _, q := range rt.query {
			params = append(params, map[string]any{"name": q.name, "in": "query", "required": false, "description": q.doc,
				"schema": map[string]any{"type": q.typ}})
		}
		if params != nil {
			op["parameters"] = params
		}
		if rt.in != nil {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": g.schema(reflect.TypeOf(rt.in))}}}
		}
		status := rt.status
		if status == 0 {
			status = http.StatusOK
		}
		ok := map[string]any{"description": http.StatusText(status)}
		switch {
		case rt.path == "/api/v1/evidence/{id}/export":
			ok["content"] = map[string]any{"application/gzip": map[string]any{"schema": map[string]any{"type": "string", "contentMediaType": "application/gzip"}}}
		case rt.out != nil:
			ok["content"] = map[string]any{"application/json": map[string]any{"schema": g.schema(reflect.TypeOf(rt.out))}}
		}
		resp := map[string]any{fmt.Sprint(status): ok, "400": errResp("Invalid request"), "500": errResp("Internal error")}
		if rt.roles != nil {
			resp["401"] = errResp("Not logged in, or the session is not valid")
			resp["403"] = errResp("The user's role may not do this")
			if rt.audited() {
				resp["503"] = errResp("The audit log cannot be written, so the action was not done")
			}
		} else if rt.path == "/api/v1/login" {
			resp["401"] = errResp("Wrong username or password, or the user is disabled")
		}
		if strings.Contains(rt.path, "{") {
			resp["404"] = errResp("No such record")
		}
		if rt.method != http.MethodGet {
			resp["409"] = errResp("Conflicts with existing records")
		}
		if rt.path == "/api/v1/repositories" && rt.method == http.MethodPost {
			resp["501"] = errResp("Not implemented in this version (S3)")
		}
		op["responses"] = resp
		if paths[rt.path] == nil {
			paths[rt.path] = map[string]any{}
		}
		paths[rt.path][strings.ToLower(rt.method)] = op
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Gaia API",
			"version":     "1",
			"description": "The REST/JSON API of gaia server (ADR-007 §6). Every operation lists the roles allowed on it in x-gaia-roles; an empty list means no login is needed. x-gaia-step-up marks the destructive and security actions that need a fresh second factor in the request (ADR-008 §4). Generated from internal/server/routes.go.",
			"license":     map[string]any{"name": "AGPL-3.0-or-later", "identifier": "AGPL-3.0-or-later"},
		},
		"security":   []any{map[string]any{"bearer": []any{}}},
		"paths":      paths,
		"components": map[string]any{"securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}}, "schemas": g.defs},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func rolesOf(rt route) []string {
	if rt.roles == nil {
		return []string{}
	}
	return rt.roles
}

func operationID(method, path string) string {
	p := strings.TrimPrefix(path, "/api/v1/")
	p = pathParam.ReplaceAllString(p, "by_$1")
	p = strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(p)
	return strings.ToLower(method) + "_" + p
}

func tag(path string) string {
	seg := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")[0]
	switch seg {
	case "login", "logout", "me":
		return "session"
	case "health", "openapi.json", "system", "state-backups":
		return "system"
	}
	return seg
}

type schemaGen struct{ defs map[string]any }

var rawJSON = reflect.TypeOf(json.RawMessage{})

func (g *schemaGen) schema(t reflect.Type) map[string]any {
	if t == rawJSON {
		return map[string]any{"description": "any JSON value"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		inner := g.schema(t.Elem())
		if typ, ok := inner["type"].(string); ok {
			out := map[string]any{}
			for k, v := range inner {
				out[k] = v
			}
			out["type"] = []string{typ, "null"}
			return out
		}
		return map[string]any{"oneOf": []any{inner, map[string]any{"type": "null"}}}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Int32:
		return map[string]any{"type": "integer"}
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		return map[string]any{"type": "integer", "minimum": 0}
	case reflect.Float64, reflect.Float32:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Map:
		if t.Elem().Kind() == reflect.Interface {
			return map[string]any{"type": "object"}
		}
		return map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		name := t.Name()
		if t.String() == "time.Time" {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		ref := map[string]any{"$ref": "#/components/schemas/" + name}
		if _, done := g.defs[name]; done {
			return ref
		}
		g.defs[name] = map[string]any{} // placeholder against recursion
		props := map[string]any{}
		var required []string
		var fields func(t reflect.Type)
		fields = func(t reflect.Type) {
			for i := range t.NumField() {
				f := t.Field(i)
				tagv := f.Tag.Get("json")
				if tagv == "-" {
					continue
				}
				jname, opts, _ := strings.Cut(tagv, ",")
				if f.Anonymous && jname == "" && f.Type.Kind() == reflect.Struct {
					fields(f.Type) // embedded: its fields are the outer object's, as encoding/json does
					continue
				}
				if !f.IsExported() {
					continue
				}
				if jname == "" {
					jname = f.Name
				}
				props[jname] = g.schema(f.Type)
				if !strings.Contains(opts, "omitempty") {
					required = append(required, jname)
				}
			}
		}
		fields(t)
		slices.Sort(required)
		def := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			def["required"] = required
		}
		g.defs[name] = def
		return ref
	}
	panic("openapi: no schema for " + t.String())
}

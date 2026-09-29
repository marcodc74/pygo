package interp

// OpenAPI: http.openapi builds an OpenAPI 3.1 document (JSON) from http.Route
// values. Path and query parameters come from the route itself; request and
// response schemas come from the struct/enum types declared on the route.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
)

// specBuilder accumulates JSON Schemas into components/schemas, so a type used
// in several places is emitted once and referenced by name.
type specBuilder struct {
	th         *Thread
	components map[string]any // component name -> schema
	names      map[any]string // *StructType/*EnumType -> component name
	used       map[string]bool
	building   map[any]bool // guards against recursive types
}

func newSpecBuilder(th *Thread) *specBuilder {
	return &specBuilder{
		th:         th,
		components: map[string]any{},
		names:      map[any]string{},
		used:       map[string]bool{},
		building:   map[any]bool{},
	}
}

// httpOpenAPI builds the document. Invalid routes, query types or schemas are
// programmer errors, so they are panics (like http.dispatch), not failures.
func (th *Thread) httpOpenAPI(routes *List, title, version, description, server string) (Value, error) {
	if title == "" {
		return nil, perr(PArgs, `pass title: "My API"`, "http.openapi: title must not be empty")
	}
	if version == "" {
		return nil, perr(PArgs, `pass version: "1.0.0"`, "http.openapi: version must not be empty")
	}
	b := newSpecBuilder(th)
	routeType := th.in.stdModule("http").Types["Route"]
	paths := map[string]any{}
	seenRoute := map[string]bool{}
	seenTemplate := map[string]string{}
	seenOp := map[string]bool{}
	for _, rv := range routes.Snapshot() {
		route, ok := rv.(*Struct)
		if !ok || route.T != routeType {
			return nil, perr(PArgs, "", "http.openapi: routes must be http.Route values, got %s", TypeName(rv))
		}
		method := Str(field(route, "method"))
		path := Str(field(route, "path"))
		pat, err := compileRoute(method, path)
		if err != nil {
			return nil, perr(PArgs, `write http.Route{method: "GET", path: "/items/{id}", handler: f}`, "http.openapi: %v", err)
		}
		rkey := strings.ToLower(method) + " " + path
		if seenRoute[rkey] {
			return nil, perr(PArgs, "remove the duplicate route", "http.openapi: duplicate route %s %s", method, path)
		}
		seenRoute[rkey] = true
		// Two routes may share a path item only if it is literally the same
		// path (then several methods are fine). Paths that differ only in a
		// parameter name, or a rest parameter that collapses to a normal one,
		// are the same OpenAPI path and would be invalid or silently dropped.
		if prev, ok := seenTemplate[normalizedPath(pat)]; ok && prev != path {
			return nil, perr(PArgs, "give the routes distinct paths", "http.openapi: routes %q and %q are the same OpenAPI path", prev, path)
		}
		seenTemplate[normalizedPath(pat)] = path
		id := Str(field(route, "operation_id"))
		if id == "" {
			id = defaultOperationID(method, path)
		}
		if seenOp[id] {
			return nil, perr(PArgs, "give the route an operation_id", "http.openapi: duplicate operationId %q", id)
		}
		seenOp[id] = true
		op, err := b.operation(route, pat, id)
		if err != nil {
			return nil, err
		}
		key := openapiPath(path)
		item, _ := paths[key].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[key] = item
		}
		item[strings.ToLower(method)] = op
	}
	info := map[string]any{"title": title, "version": version}
	if description != "" {
		info["description"] = description
	}
	doc := map[string]any{"openapi": "3.1.0", "info": info, "paths": paths}
	if server != "" {
		doc["servers"] = []any{map[string]any{"url": server}}
	}
	if len(b.components) > 0 {
		doc["components"] = map[string]any{"schemas": b.components}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, th.fail("E_OPENAPI", "%v", err)
	}
	return string(out) + "\n", nil
}

func (b *specBuilder) operation(route *Struct, pat *routePat, id string) (map[string]any, error) {
	op := map[string]any{}
	if s := Str(field(route, "summary")); s != "" {
		op["summary"] = s
	}
	op["operationId"] = id
	if tags, ok := field(route, "tags").(*List); ok && tags.Len() > 0 {
		op["tags"] = stringList(tags)
	}
	var params []any
	for _, name := range pat.names {
		if name != "" {
			params = append(params, pathParameter(name))
		}
	}
	if pat.rest != "" {
		params = append(params, pathParameter(pat.rest))
	}
	if q, ok := field(route, "query").(*Map); ok && q.Len() > 0 {
		ks, vs := q.Items()
		type kv struct{ name, typ string }
		qs := make([]kv, len(ks))
		for i := range ks {
			qs[i] = kv{Str(ks[i]), Str(vs[i])}
		}
		sort.Slice(qs, func(i, j int) bool { return qs[i].name < qs[j].name })
		for _, p := range qs {
			schema := querySchema(p.typ)
			if schema == nil {
				return nil, perr(PArgs, `query types are "Int", "Float", "Str" or "Bool"`,
					"http.openapi: query parameter %q has unknown type %q", p.name, p.typ)
			}
			params = append(params, map[string]any{"name": p.name, "in": "query", "required": false, "schema": schema})
		}
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	if body := field(route, "body"); body != nil {
		schema, err := b.schemaOfValue(body)
		if err != nil {
			return nil, err
		}
		op["requestBody"] = map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
		}
	}
	responses := map[string]any{
		"200": map[string]any{"description": "OK"},
		"404": map[string]any{"description": "Not Found"},
		"405": map[string]any{"description": "Method Not Allowed"},
		"500": map[string]any{"description": "Internal Server Error"},
	}
	if resp := field(route, "response"); resp != nil {
		schema, err := b.schemaOfValue(resp)
		if err != nil {
			return nil, err
		}
		responses["200"] = map[string]any{
			"description": "OK",
			"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
		}
	}
	op["responses"] = responses
	return op, nil
}

// ---------- JSON Schema from Pygo types ----------

func (b *specBuilder) schemaOfValue(v Value) (any, error) {
	switch v.(type) {
	case *StructType, *EnumType:
		return b.refOf(v)
	}
	return nil, perr(PArgs, "pass a struct or enum type", "http.openapi: schema must be a struct or enum type, got %s", TypeName(v))
}

func (b *specBuilder) schemaOfType(te *ast.TypeExpr, mod *Module, depth int) (any, error) {
	if te == nil || depth > 32 {
		return map[string]any{}, nil
	}
	if te.Optional {
		inner, err := b.schemaOfType(&ast.TypeExpr{Name: te.Name, Args: te.Args}, mod, depth)
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}, nil
	}
	switch te.Name {
	case "Any":
		return map[string]any{}, nil
	case "Int":
		return map[string]any{"type": "integer", "format": "int64"}, nil
	case "Float":
		return map[string]any{"type": "number", "format": "double"}, nil
	case "Str", "Html", "Sql":
		return map[string]any{"type": "string"}, nil
	case "Bool":
		return map[string]any{"type": "boolean"}, nil
	case "Nil":
		return map[string]any{"type": "null"}, nil
	case "Range", "Chan", "Task", "fn", "Type", "Error":
		// not JSON data; a free-form schema is the honest answer
		return map[string]any{}, nil
	case "List":
		items := any(map[string]any{})
		if len(te.Args) >= 1 {
			s, err := b.schemaOfType(te.Args[0], mod, depth+1)
			if err != nil {
				return nil, err
			}
			items = s
		}
		return map[string]any{"type": "array", "items": items}, nil
	case "Map":
		if len(te.Args) >= 2 {
			s, err := b.schemaOfType(te.Args[1], mod, depth+1)
			if err != nil {
				return nil, err
			}
			return map[string]any{"type": "object", "additionalProperties": s}, nil
		}
		return map[string]any{"type": "object"}, nil
	}
	t := resolveSchemaType(te, mod)
	switch t.(type) {
	case *StructType, *EnumType:
		return b.refOf(t)
	case nil:
		return nil, perr(PArgs, "", "http.openapi: unknown type %s", te.Name)
	}
	return map[string]any{}, nil
}

// refOf reserves a component name and returns a $ref to it, emitting the schema
// once. Recursive types see the reserved ref instead of recursing forever.
func (b *specBuilder) refOf(t any) (any, error) {
	name := b.nameFor(t)
	ref := map[string]any{"$ref": "#/components/schemas/" + name}
	if b.building[t] {
		return ref, nil
	}
	b.building[t] = true
	defer delete(b.building, t)
	switch x := t.(type) {
	case *StructType:
		props := map[string]any{}
		var required []string
		for _, f := range x.Fields {
			if f.Type == nil {
				props[f.Name] = map[string]any{}
				continue
			}
			s, err := b.schemaOfType(f.Type, x.Module, 0)
			if err != nil {
				return nil, err
			}
			props[f.Name] = s
			if f.Default == nil && !f.Type.Optional {
				required = append(required, f.Name)
			}
		}
		sch := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			sort.Strings(required)
			sch["required"] = toAny(required)
		}
		b.components[name] = sch
	case *EnumType:
		var oneOf []any
		for _, v := range x.Variants {
			props := map[string]any{"variant": map[string]any{"const": v.Name}}
			required := []string{"variant"}
			for _, f := range v.Fields {
				if f.Name == "variant" {
					return nil, perr(PArgs, `rename the field: "variant" is the enum tag`,
						"http.openapi: variant %s.%s has a field named 'variant'", x.Name, v.Name)
				}
				if f.Type == nil {
					props[f.Name] = map[string]any{}
					continue
				}
				s, err := b.schemaOfType(f.Type, x.Module, 0)
				if err != nil {
					return nil, err
				}
				props[f.Name] = s
				if !f.Type.Optional {
					required = append(required, f.Name)
				}
			}
			sort.Strings(required)
			oneOf = append(oneOf, map[string]any{"type": "object", "properties": props, "required": toAny(required)})
		}
		b.components[name] = map[string]any{"oneOf": oneOf}
	}
	return ref, nil
}

func (b *specBuilder) nameFor(t any) string {
	if n, ok := b.names[t]; ok {
		return n
	}
	base := "Type"
	switch x := t.(type) {
	case *StructType:
		base = x.Name
	case *EnumType:
		base = x.Name
	}
	base = sanitizeSchemaName(base)
	name := base
	for i := 2; b.used[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	b.used[name] = true
	b.names[t] = name
	return name
}

// resolveSchemaType resolves a (possibly qualified) type name in mod.
func resolveSchemaType(te *ast.TypeExpr, mod *Module) any {
	if mod == nil {
		return nil
	}
	if i := strings.IndexByte(te.Name, '.'); i >= 0 {
		if im := mod.Imports[te.Name[:i]]; im != nil {
			return im.Types[te.Name[i+1:]]
		}
		return nil
	}
	return mod.Types[te.Name]
}

// ---------- helpers ----------

func openapiPath(path string) string {
	return strings.ReplaceAll(path, "...}", "}")
}

// normalizedPath renders a compiled route as an OpenAPI path template with the
// parameter names erased, so /a/{x} and /a/{y} (and /a/{x...}) are recognized
// as the same path, which OpenAPI forbids to declare twice.
func normalizedPath(p *routePat) string {
	var b strings.Builder
	for i, seg := range p.segs {
		b.WriteByte('/')
		if p.names[i] == "" {
			b.WriteString(seg)
		} else {
			b.WriteString("{}")
		}
	}
	if p.rest != "" {
		b.WriteString("/{}")
	}
	if b.Len() == 0 {
		b.WriteByte('/')
	}
	return b.String()
}

func pathParameter(name string) map[string]any {
	return map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
}

func querySchema(typ string) map[string]any {
	switch typ {
	case "Int":
		return map[string]any{"type": "integer", "format": "int64"}
	case "Float":
		return map[string]any{"type": "number", "format": "double"}
	case "Str":
		return map[string]any{"type": "string"}
	case "Bool":
		return map[string]any{"type": "boolean"}
	}
	return nil
}

// defaultOperationID is method + path segments, e.g. GET /items/{id} -> getItemsId.
func defaultOperationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
		name = strings.TrimSuffix(name, "...")
		name = strings.ReplaceAll(name, "_", " ")
		name = strings.ReplaceAll(name, "-", " ")
		for _, part := range strings.Fields(name) {
			b.WriteString(capitalize(part))
		}
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func sanitizeSchemaName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "Type"
	}
	return b.String()
}

func stringList(l *List) []any {
	items := l.Snapshot()
	out := make([]any, len(items))
	for i, v := range items {
		out[i] = Str(v)
	}
	return out
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, s := range xs {
		out[i] = s
	}
	return out
}

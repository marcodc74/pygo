package interp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

const openapiDemoSrc = `
import "http"

struct Item { id: Int, name: Str, price: Float, tags: List[Str] = [], note: Str? = nil }
struct ItemPage { items: List[Item], total: Int }
enum Status { Active, Archived(reason: Str) }

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn routes() -> List[http.Route] {
    return [
        http.Route{method: "GET", path: "/items", handler: h, summary: "List items", tags: ["items"], query: {"limit": "Int", "q": "Str"}, response: ItemPage},
        http.Route{method: "GET", path: "/items/{id}", handler: h, operation_id: "getItem", response: Item},
        http.Route{method: "POST", path: "/items", handler: h, body: Item, response: Item},
        http.Route{method: "GET", path: "/status", handler: h, response: Status},
        http.Route{method: "GET", path: "/files/{path...}", handler: h},
    ]
}

fn main() {
    print(http.openapi(routes(), title: "Items API", version: "2.0.0", description: "Demo", server: "https://api.example.com"))
}
`

func TestOpenAPI(t *testing.T) {
	out, res := runSrc(t, openapiDemoSrc)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi version %v", doc["openapi"])
	}
	for _, want := range []string{
		`"title": "Items API"`,
		`"version": "2.0.0"`,
		`"description": "Demo"`,
		`"summary": "List items"`,
		`"operationId": "getItem"`,
		`"operationId": "getItems"`,
		`"operationId": "getFilesPath"`,
		`"#/components/schemas/Item"`,
		`"#/components/schemas/ItemPage"`,
		`"#/components/schemas/Status"`,
		`"const": "Archived"`,
		`"in": "path"`,
		`"in": "query"`,
		`"url": "https://api.example.com"`,
		`"anyOf"`,
		`"required"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("spec lacks %q", want)
		}
	}
}

func TestOpenAPIEmpty(t *testing.T) {
	out, res := runSrc(t, `
import "http"

fn main() { print(http.openapi([], title: "Empty")) }
`)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) != 0 {
		t.Fatalf("paths = %v, want an empty object", doc["paths"])
	}
}

// A self-referential type must terminate and reference itself instead of
// recursing forever.
func TestOpenAPIRecursive(t *testing.T) {
	out, res := runSrc(t, `
import "http"

struct Node { value: Int, next: Node? }

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    print(http.openapi([http.Route{method: "GET", path: "/n", handler: h, response: Node}], title: "N"))
}
`)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	if !strings.Contains(out, `"#/components/schemas/Node"`) || !strings.Contains(out, `"anyOf"`) {
		t.Fatalf("recursive schema not referenced:\n%s", out)
	}
}

func TestOpenAPIErrors(t *testing.T) {
	head := `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")
`
	cases := []struct{ name, body string }{
		{"path without a slash", `print(http.openapi([http.Route{method: "GET", path: "items", handler: h}], title: "T"))`},
		{"lowercase method", `print(http.openapi([http.Route{method: "get", path: "/items", handler: h}], title: "T"))`},
		{"unknown query type", `print(http.openapi([http.Route{method: "GET", path: "/items", handler: h, query: {"x": "Nope"}}], title: "T"))`},
		{"empty title", `print(http.openapi([http.Route{method: "GET", path: "/items", handler: h}], title: ""))`},
		{"empty version", `print(http.openapi([http.Route{method: "GET", path: "/items", handler: h}], title: "T", version: ""))`},
		{"duplicate route", `print(http.openapi([http.Route{method: "GET", path: "/x", handler: h}, http.Route{method: "GET", path: "/x", handler: h}], title: "T"))`},
		{"duplicate operation id", `print(http.openapi([http.Route{method: "GET", path: "/x", handler: h, operation_id: "op"}, http.Route{method: "POST", path: "/y", handler: h, operation_id: "op"}], title: "T"))`},
		{"equivalent paths, different names", `print(http.openapi([http.Route{method: "GET", path: "/a/{x}", handler: h}, http.Route{method: "GET", path: "/a/{y}", handler: h}], title: "T"))`},
		{"rest collapses to a parameter", `print(http.openapi([http.Route{method: "GET", path: "/a/{x}", handler: h}, http.Route{method: "GET", path: "/a/{x...}", handler: h}], title: "T"))`},
		{"not a route", `
    let bad: Any = "nope"
    print(http.openapi([bad], title: "T"))`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, res := runSrc(t, head+"\nfn main() {\n"+c.body+"\n}\n")
			if res.Status != "panic" || res.Panic == nil || res.Panic.Code != PArgs {
				t.Fatalf("got %s, want a %s panic", res.Describe(), PArgs)
			}
		})
	}
}

// The same literal path with several methods is one path item, not a duplicate.
func TestOpenAPISamePathDifferentMethods(t *testing.T) {
	out, res := runSrc(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    print(http.openapi([http.Route{method: "GET", path: "/x", handler: h}, http.Route{method: "POST", path: "/x", handler: h}], title: "T"))
}
`)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s", res.Status, res.Describe())
	}
	if !strings.Contains(out, `"get"`) || !strings.Contains(out, `"post"`) {
		t.Fatalf("both methods should share the path item:\n%s", out)
	}
}

// An enum variant field named "variant" would collide with the discriminator, so
// openapi refuses it instead of emitting a wrong schema.
func TestOpenAPIEnumVariantField(t *testing.T) {
	_, res := runSrc(t, `
import "http"

enum E { A(variant: Str) }

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    print(http.openapi([http.Route{method: "GET", path: "/e", handler: h, response: E}], title: "T"))
}
`)
	if res.Status != "panic" || res.Panic == nil || res.Panic.Code != PArgs {
		t.Fatalf("got %s, want a %s panic", res.Describe(), PArgs)
	}
}

// A struct field that is not JSON data (a function) becomes a free-form schema
// instead of an error.
func TestOpenAPIFunctionField(t *testing.T) {
	out, res := runSrc(t, `
import "http"

struct Handler { name: Str, f: fn(Int) -> Int }

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    print(http.openapi([http.Route{method: "GET", path: "/h", handler: h, response: Handler}], title: "T"))
}
`)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s", res.Status, res.Describe())
	}
	if !strings.Contains(out, `"name"`) || !strings.Contains(out, `"Handler"`) {
		t.Fatalf("unexpected schema:\n%s", out)
	}
}

// Two types with the same name in different modules must get distinct component
// names.
func TestOpenAPINameCollision(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.pg"), []byte("struct Item { id: Int }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.pg"), []byte("struct Item { name: Str }\n"), 0o644)
	main := `
import "./a"
import "./b"
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    print(http.openapi([
        http.Route{method: "GET", path: "/a", handler: h, response: a.Item},
        http.Route{method: "GET", path: "/b", handler: h, response: b.Item},
    ], title: "T"))
}
`
	mainPath := filepath.Join(dir, "main.pg")
	os.WriteFile(mainPath, []byte(main), 0o644)
	prog, ds := loader.Load(mainPath, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("load: %v", ds)
	}
	out, res := runBoth(t, prog, Options{})
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\n%s", res.Status, res.Describe(), out)
	}
	if !strings.Contains(out, `"#/components/schemas/Item"`) || !strings.Contains(out, `"#/components/schemas/Item_2"`) {
		t.Fatalf("collision not disambiguated:\n%s", out)
	}
}

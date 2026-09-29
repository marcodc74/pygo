package check

import "testing"

func TestWebApi(t *testing.T) {
	expectCodes(t, `
import "http"

fn assets(req: http.Request) -> http.Response uses fs => http.static(req, dir: "public")

fn create(req: http.Request) -> http.Response {
    let fields = http.form(req) catch e {
        return http.text(400, body: e.message)
    }
    let title = fields.get("title") ?? ""
    return http.Response{status: 303, headers: {"location": "/"}, cookies: [http.Cookie{name: "last", value: title}]}
}

fn routes() -> List[http.Route] uses fs {
    return [
        http.Route{method: "GET", path: "/static/{path...}", handler: assets},
        http.Route{method: "POST", path: "/todos", handler: create},
        http.Route{method: "GET", path: "/", handler: fn(req) => http.redirect("/todos")},
    ]
}

fn main() -> ! uses net, fs {
    try http.serve(":8080", handler: fn(req) => http.dispatch(req, routes: routes()), max_body: 65536)
}
`)
	// static reads files: the fs effect must be declared
	expectCodes(t, `
import "http"

fn assets(req: http.Request) -> http.Response => http.static(req, dir: "public")
`, "E0501")
}

// Middleware is typed: dispatch takes a List[Middleware]. A struct literal with
// an apply function is accepted; a list of plain handlers is a mismatch.
func TestMiddlewareTypes(t *testing.T) {
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn pass() -> http.Middleware {
    return http.Middleware{name: "pass", apply: fn(next) => fn(req) { return next(req) }}
}

fn main() {
    let r = http.dispatch(http.Request{method: "GET", path: "/", query: {}, headers: {}, body: ""}, routes: [http.Route{method: "GET", path: "/", handler: h}], middleware: [pass()])
    print(r.status)
}
`)
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    let r = http.dispatch(http.Request{method: "GET", path: "/", query: {}, headers: {}, body: ""}, routes: [], middleware: [h])
    print(r.status)
}
`, "E0301")
}

// serve accepts an optional http.Tls: a wrong type or a missing field is caught.
func TestServeTLSTypes(t *testing.T) {
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() -> ! uses net {
    try http.serve(":8443", handler: h, tls: http.Tls{cert: "cert.pem", key: "key.pem"})
}
`)
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() -> ! uses net {
    try http.serve(":8443", handler: h, tls: http.Tls{cert: "cert.pem"})
}
`, "E0602")
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() -> ! uses net {
    try http.serve(":8443", handler: h, tls: "cert.pem")
}
`, "E0301")
}

// openapi takes the route list and optional metadata; body and response must be
// a struct or enum type (Type[Any]).
func TestOpenAPITypes(t *testing.T) {
	expectCodes(t, `
import "http"

struct Item { id: Int, name: Str }

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    let doc = http.openapi([http.Route{method: "GET", path: "/items", handler: h, body: Item, response: Item, query: {"limit": "Int"}}], title: "T")
    print(doc.len())
}
`)
	expectCodes(t, `
import "http"

fn h(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    let doc = http.openapi([http.Route{method: "GET", path: "/items", handler: h, body: "no"}], title: "T")
    print(doc.len())
}
`, "E0301")
}

// A named function used as a value brings its effects to the function
// that takes it: routes() can hand assets to anyone who calls it.
func TestFunctionValueEffects(t *testing.T) {
	ds := expectCodes(t, `
import "fs"

fn read(path: Str) -> Str uses fs => fs.read(path) catch e { "" }

fn readers() -> List[fn(Str) -> Str] {
    return [read]
}

fn apply(paths: List[Str]) -> List[Str] {
    return paths.map(read)
}
`, "E0501", "E0501")
	if ds[0].Hint != "declare it: uses fs" {
		t.Fatalf("hint %q", ds[0].Hint)
	}
}

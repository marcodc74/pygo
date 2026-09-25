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

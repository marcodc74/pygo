package interp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

func TestDispatch(t *testing.T) {
	expectOut(t, `
import "http"

fn show(req: http.Request) -> http.Response => http.text(200, body: "item ${req.params["id"]}")
fn create(req: http.Request) -> http.Response => http.text(201, body: "created")
fn file(req: http.Request) -> http.Response => http.text(200, body: "file '${req.params["path"]}'")
fn fixed(req: http.Request) -> http.Response => http.text(200, body: "new form")

fn routes() -> List[http.Route] {
    return [
        http.Route{method: "GET", path: "/items/new", handler: fixed},
        http.Route{method: "GET", path: "/items/{id}", handler: show},
        http.Route{method: "POST", path: "/items", handler: create},
        http.Route{method: "GET", path: "/files/{path...}", handler: file},
    ]
}

fn call(method: Str, path: Str) {
    let r = http.dispatch(http.Request{method: method, path: path, query: {}, headers: {}, body: ""}, routes: routes())
    print(r.status, r.body, r.headers.get("allow") ?? "-")
}

fn main() {
    call("GET", path: "/items/42")
    call("HEAD", path: "/items/7")
    call("GET", path: "/items/new")
    call("POST", path: "/items")
    call("DELETE", path: "/items/42")
    call("GET", path: "/items")
    call("GET", path: "/items/")
    call("GET", path: "/files/css/site.css")
    call("GET", path: "/files/")
    call("GET", path: "/files")
    call("GET", path: "/nope")
}
`, `200 item 42 -
200 item 7 -
200 new form -
201 created -
405 method not allowed GET, HEAD
405 method not allowed POST
404 not found -
200 file 'css/site.css' -
200 file '' -
404 not found -
404 not found -`)
}

func TestDispatchInvalidRoute(t *testing.T) {
	for _, path := range []string{"items", "/a/{id", "/a/{x}/{x}", "/a/{rest...}/b", "/a/{1x}"} {
		out, res := runSrc(t, `
import "http"

fn ok(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    let req = http.Request{method: "GET", path: "/a", query: {}, headers: {}, body: ""}
    print(http.dispatch(req, routes: [http.Route{method: "GET", path: "`+path+`", handler: ok}]).status)
}
`)
		if res.Status != "panic" || res.Panic.Code != PArgs || !strings.Contains(res.Panic.Message, "http.dispatch") {
			t.Fatalf("%s: got %s: %s\n%s", path, res.Status, res.Describe(), out)
		}
	}
	_, res := runSrc(t, `
import "http"

fn ok(req: http.Request) -> http.Response => http.text(200, body: "ok")

fn main() {
    let req = http.Request{method: "GET", path: "/a", query: {}, headers: {}, body: ""}
    print(http.dispatch(req, routes: [http.Route{method: "get", path: "/a", handler: ok}]).status)
}
`)
	if res.Status != "panic" || !strings.Contains(res.Panic.Message, "invalid route method") {
		t.Fatalf("got %s", res.Describe())
	}
}

func TestFormAndRedirect(t *testing.T) {
	expectOut(t, `
import "http"

fn main() {
    let req = http.Request{method: "POST", path: "/todos", query: {}, headers: {"content-type": "application/x-www-form-urlencoded"}, body: "title=Buy+milk&done=on&title=ignored"}
    let f = try http.form(req)
    print(f)
    let bad = http.Request{method: "POST", path: "/todos", query: {}, headers: {"content-type": "application/json"}, body: "{}"}
    let _x = http.form(bad) catch e {
        print(e.code)
        {}
    }
    let r = http.redirect("/todos")
    print(r.status, r.headers["location"], r.body == "")
    print(http.redirect("/new", status: 308).status)
}
`, `{"done": "on", "title": "Buy milk"}
E_FORM
303 /todos true
308`)
	_, res := runSrc(t, `
import "http"

fn main() {
    print(http.redirect("/x", status: 200).status)
}
`)
	if res.Status != "panic" || res.Panic.Code != PArgs {
		t.Fatalf("got %s", res.Describe())
	}
}

func TestStaticFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "css"), 0o755)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o644)
	os.WriteFile(filepath.Join(dir, "css", "site.css"), []byte("body{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>home</h1>"), 0o644)
	os.WriteFile(filepath.Join(dir, "data.bin"), []byte{0, 1, 2}, 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("no"), 0o644)
	src := `
import "http"

fn serve_file(req: http.Request) -> http.Response uses fs => http.static(req, dir: "` + filepath.ToSlash(dir) + `")

fn get(path: Str) uses fs {
    let routes = [http.Route{method: "GET", path: "/static/{path...}", handler: serve_file}]
    let r = http.dispatch(http.Request{method: "GET", path: path, query: {}, headers: {}, body: ""}, routes: routes)
    print(r.status, r.headers.get("content-type") ?? "-", r.body.len())
}

fn main() uses fs {
    get("/static/app.js")
    get("/static/css/site.css")
    get("/static/")
    get("/static/data.bin")
    get("/static/.env")
    get("/static/../outside.txt")
    get("/static/css/../app.js")
    get("/static/missing.png")
}
`
	out, res := runSrc(t, src, "fs")
	if res.Status != "ok" {
		t.Fatalf("%s\n%s", res.Describe(), out)
	}
	want := `200 text/javascript; charset=utf-8 14
200 text/css; charset=utf-8 6
200 text/html; charset=utf-8 13
200 application/octet-stream 3
404 text/plain; charset=utf-8 9
404 text/plain; charset=utf-8 9
404 text/plain; charset=utf-8 9
404 text/plain; charset=utf-8 9`
	if strings.TrimSpace(out) != want {
		t.Fatalf("got:\n%s", out)
	}
	// without the fs capability the call is refused
	_, res = runSrc(t, src)
	if res.ExitCode != ExitPermission {
		t.Fatalf("expected a capability error, got %s", res.Describe())
	}
}

// webHandler loads a program and returns the net/http handler serving its
// handle function, on the given engine.
func webHandler(t *testing.T, src string, maxBody int64, engine string) (http.Handler, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	var out bytes.Buffer
	in := New(prog, Options{Stdout: &out, Stderr: &out, Engine: engine})
	m, err := in.Load(prog.Main)
	if err != nil {
		t.Fatal(err)
	}
	return in.newThread(m).httpHandler(m.Members["handle"], maxBody), &out
}

func TestServeHandler(t *testing.T) {
	src := `
import "http"

fn handle(req: http.Request) -> http.Response {
    if req.path == "/bad-cookie" {
        return http.Response{status: 200, cookies: [http.Cookie{name: "x", value: "1", secure: false, same_site: "None"}]}
    }
    let seen = req.cookies.get("session") ?? "none"
    return http.Response{
        status: 200,
        body: "session=${seen} body=${req.body.len()}",
        cookies: [http.Cookie{name: "session", value: "abc"}, http.Cookie{name: "theme", value: "", max_age: -1, http_only: false, same_site: "Strict"}],
    }
}
`
	for _, engine := range []string{"tree", "vm"} {
		h, log := webHandler(t, src, 16, engine)

		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader("hello"))
		r.Header.Set("Cookie", "session=s1; other=2")
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != "session=s1 body=5" {
			t.Fatalf("%s: %d %q", engine, w.Code, w.Body.String())
		}
		got := strings.Join(w.Result().Header.Values("Set-Cookie"), " | ")
		want := "session=abc; Path=/; HttpOnly; Secure; SameSite=Lax | theme=; Path=/; Max-Age=0; Secure; SameSite=Strict"
		if got != want {
			t.Fatalf("%s: set-cookie\n got %s\nwant %s", engine, got, want)
		}

		// body over max_body: 413, handler not called
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 17))))
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: got %d", engine, w.Code)
		}
		// same without Content-Length (streamed body)
		w = httptest.NewRecorder()
		r = httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 17)))
		r.ContentLength = -1
		h.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: streamed body: got %d", engine, w.Code)
		}

		// an invalid cookie is a 500 and a log line
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/bad-cookie", nil))
		if w.Code != 500 || !strings.Contains(log.String(), `same_site \"None\" requires secure: true`) {
			t.Fatalf("%s: got %d, log %s", engine, w.Code, log.String())
		}
	}
}

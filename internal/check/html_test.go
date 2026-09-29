package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

func TestHtmlLiteralsValid(t *testing.T) {
	expectCodes(t, `
import "html"
import "http"

struct Item { name: Str, price: Float, tags: List[Str] }

fn row(it: Item) -> Html {
    let tags = it.tags.map(fn(tag) => html"<span>${tag}</span>")
    return html"""
<tr data-name="${it.name}"><td><a href="/items?name=${it.name}">${it.name}</a></td>
<td>${it.price:.2}</td><td>${tags}</td></tr>"""
}

fn page(items: List[Item]) -> http.Response {
    let body = html"<table>${items.map(row)}</table>${html.raw("<hr>")}"
    return http.html(200, body: body)
}

fn query() -> Sql {
    return sql"SELECT name FROM items WHERE price > ?"
}
`)
}

func TestHtmlInterpolationTypes(t *testing.T) {
	ds := expectCodes(t, `
struct P { x: Int }

fn f(m: Map[Str, Int], names: List[Str], opt: Str?, h: Html, p: P) -> Html {
    return html"${m} ${names} ${opt} ${h:.2} ${p}"
}
`, "E0311", "E0311", "E0311", "E0311", "E0311")
	if !strings.Contains(ds[1].Hint, "xs.map") || !strings.Contains(ds[2].Hint, "??") {
		t.Fatalf("hints: %q / %q", ds[1].Hint, ds[2].Hint)
	}
}

func TestHtmlContextErrors(t *testing.T) {
	ds := expectCodes(t, `
fn f(x: Str) -> List[Html] {
    return [html"<p title='${x}", html"<script>let a = 1"]
}
`, "E0312", "E0312")
	if !strings.Contains(ds[0].Message, "ends inside") {
		t.Fatalf("message: %q", ds[0].Message)
	}
}

func TestTrustedTextTypes(t *testing.T) {
	ds := expectCodes(t, `
fn run(q: Sql) {}

fn page(name: Str) -> Html {
    run("DELETE FROM t")
    let _a = html"<b>" + html"</b>"
    let _s: Str = html"<p>"
    return "<p>hi</p>"
}

struct Html {}
`, "E0301", "E0302", "E0301", "E0301", "E0207")
	if !strings.Contains(ds[0].Hint, `sql"..."`) || !strings.Contains(ds[1].Hint, `html"${a}${b}"`) || !strings.Contains(ds[3].Hint, `html"..."`) {
		t.Fatalf("hints: %q / %q / %q", ds[0].Hint, ds[1].Hint, ds[3].Hint)
	}
}

// A Str literal where Html or Sql is expected gets a safe fix that adds
// the prefix (only without interpolation: then escaping changes nothing).
func TestTrustedTextFixes(t *testing.T) {
	src := `fn run(q: Sql) {}

fn page(name: Str) -> Html {
    run("SELECT 1")
    let greeting: Html = "<h1>Hi ${name}</h1>"
    print(greeting)
    return "<p>hi</p>"
}
`
	ds := checkSrc(t, src)
	var safe, unsafe []diag.Fix
	for _, d := range ds {
		if d.Fix != nil {
			if d.Fix.Safe {
				safe = append(safe, *d.Fix)
			} else {
				unsafe = append(unsafe, *d.Fix)
			}
		}
	}
	if len(safe) != 2 || len(unsafe) != 1 {
		t.Fatalf("fixes: %d safe, %d unsafe: %v", len(safe), len(unsafe), ds)
	}
	out, _ := diag.ApplyFixes(src, append(safe, unsafe...))
	want := `fn run(q: Sql) {}

fn page(name: Str) -> Html {
    run(sql"SELECT 1")
    let greeting: Html = html"<h1>Hi ${name}</h1>"
    print(greeting)
    return html"<p>hi</p>"
}
`
	if out != want {
		t.Fatalf("got:\n%s", out)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(out), 0o644)
	prog, _ := loader.Load(p, nil)
	if ds := Check(prog); diag.HasErrors(ds) {
		t.Fatalf("fixed program still has errors: %v", ds)
	}
}

func TestSqlLiteralCannotInterpolate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte("fn f(id: Int) -> Sql {\n    return sql\"SELECT * FROM t WHERE id = ${id}\"\n}\n"), 0o644)
	_, ds := loader.Load(p, nil)
	if codes(ds) != "E0121" || !strings.Contains(ds[0].Hint, "args:") {
		t.Fatalf("got %v", ds)
	}
}

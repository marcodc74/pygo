package interp

import (
	"strings"
	"testing"
)

// Every value interpolated in an html literal is escaped for its context.
func TestHtmlEscaping(t *testing.T) {
	expectOut(t, `
import "html"

struct Todo { title: Str, done: Bool }

fn item(t: Todo) -> Html {
    let state = if t.done { "done" } else { "open" }
    return html"<li class='${state}'>${t.title}</li>"
}

fn main() {
    let todos = [Todo{title: "<script>alert(1)</script>", done: false}, Todo{title: "Tom & Jerry", done: true}]
    print(html"<ul>${todos.map(item)}</ul>")
    print(html"<a href=\"${"javascript:alert(1)"}\">x</a> <a href=\"/items?q=${"a b&c"}\">y</a>")
    print(html"<script>let n = ${3.0}; let i = ${2}; let s = ${"</script>"};</script>")
    print(html"<p title=\"${"say \"hi\""}\">${html.raw("<b>trusted</b>")} ${1.5:.2} ${true}</p>")
    print(html"no values")
}
`, `<ul><li class='open'>&lt;script&gt;alert(1)&lt;/script&gt;</li><li class='done'>Tom &amp; Jerry</li></ul>
<a href="#ZgotmplZ">x</a> <a href="/items?q=a%20b%26c">y</a>
<script>let n =  3.0 ; let i =  2 ; let s = "\u003c/script\u003e";</script>
<p title="say &#34;hi&#34;"><b>trusted</b> 1.50 true</p>
no values`)
}

// Html and Sql values: printing, repr, equality, JSON, str.
func TestTrustedTextValues(t *testing.T) {
	expectOut(t, `
import "json"

fn main() {
    let h = html"<b>${"x"}</b>"
    let q = sql"SELECT id FROM t WHERE id = ?"
    print(h, q)
    print([h], [q])
    print(h == html"<b>x</b>", str(h).len())
    print(json.encode({"h": h, "q": q}))
    print("${h}")
}
`, `<b>x</b> SELECT id FROM t WHERE id = ?
[html"<b>x</b>"] [sql"SELECT id FROM t WHERE id = ?"]
true 8
{"h":"<b>x</b>","q":"SELECT id FROM t WHERE id = ?"}
<b>x</b>`)
}

// Trusted text never comes from data.
func TestHtmlCannotBeDecoded(t *testing.T) {
	expectOut(t, `
import "json"

struct Page { body: Html }

fn main() {
    let p = json.decode_as(r"""{"body": "<script>x</script>"}""", schema: Page) catch e {
        print(e.code, e.message)
        return
    }
    print(p.body)
}
`, `E_SCHEMA $.body: Html cannot come from data; build it with a html"..." literal`)
}

// A list hidden behind Any is still checked item by item: a Str never
// becomes markup. Both engines report the same panic.
func TestHtmlListItemsMustBeHtml(t *testing.T) {
	out, res := runSrc(t, `
fn main() {
    let items: List[Any] = ["<script>"]
    let xs: List[Html] = items
    print(html"<ul>${xs}</ul>")
}
`)
	if res.Status != "panic" || res.Panic.Code != PType || !strings.Contains(res.Panic.Message, "list item is Str, not Html") {
		t.Fatalf("got %s: %s\n%s", res.Status, res.Describe(), out)
	}
}

func TestHttpHtmlResponse(t *testing.T) {
	expectOut(t, `
import "http"

fn main() {
    let r = http.html(200, body: html"<h1>${"<Hi>"}</h1>")
    print(r.status, r.headers["content-type"], r.body)
}
`, `200 text/html; charset=utf-8 <h1>&lt;Hi&gt;</h1>`)
}

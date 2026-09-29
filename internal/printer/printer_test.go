package printer

import (
	"testing"

	"github.com/marcodc74/pygo/internal/parser"
)

// Strings with newlines print in the """...""" form, trusted literals keep
// their prefix, and printing is stable (fmt of fmt is the same).
func TestStringLiterals(t *testing.T) {
	src := "fn f(name: Str) -> Html {\n" +
		"    let a = \"one\\ntwo \\\"q\\\" ${name}\"\n" +
		"    let b = \"end quote\\\"\"\n" +
		"    let c = \"line\\nends with quote\\\"\"\n" +
		"    let d = \"x\\n\\\"\\\"\\\" y\"\n" +
		"    let e = sql\"SELECT 1\"\n" +
		"    print(a, b, c, d, e)\n" +
		"    return html\"\"\"\n<p>${name}</p>\n<p>\\\"</p>\"\"\"\n" +
		"}\n"
	want := "fn f(name: Str) -> Html {\n" +
		"    let a = \"\"\"\none\ntwo \"q\" ${name}\"\"\"\n" +
		"    let b = \"end quote\\\"\"\n" +
		"    let c = \"\"\"\nline\nends with quote\\\"\"\"\"\n" +
		"    let d = \"\"\"\nx\n\\\"\\\"\" y\"\"\"\n" +
		"    let e = sql\"SELECT 1\"\n" +
		"    print(a, b, c, d, e)\n" +
		"    return html\"\"\"\n<p>${name}</p>\n<p>\"</p>\"\"\"\n" +
		"}\n"
	f, ds := parser.ParseFile("t.pg", src)
	if len(ds) > 0 {
		t.Fatalf("parse: %v", ds)
	}
	got := File(f)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	f2, ds := parser.ParseFile("t.pg", got)
	if len(ds) > 0 {
		t.Fatalf("reparse: %v", ds)
	}
	if again := File(f2); again != got {
		t.Fatalf("not stable:\n%s", again)
	}
}

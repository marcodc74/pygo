package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

func checkSrc(t *testing.T, src string) []diag.Diagnostic {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	return Check(prog)
}

func codes(ds []diag.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return strings.Join(out, ",")
}

// expectCodes asserts that exactly the given diagnostic codes are reported.
func expectCodes(t *testing.T, src string, want ...string) []diag.Diagnostic {
	t.Helper()
	ds := checkSrc(t, src)
	if got := codes(ds); got != strings.Join(want, ",") {
		var lines []string
		for _, d := range ds {
			lines = append(lines, d.String())
		}
		t.Fatalf("codes = %q, want %q\n%s", got, strings.Join(want, ","), strings.Join(lines, "\n"))
	}
	return ds
}

func TestValidProgram(t *testing.T) {
	expectCodes(t, `
import "json"
import "fs"

/// A user.
struct User {
    name: Str
    age: Int = 0
    email: Str?
}

enum Shape {
    Circle(r: Float)
    Rect(w: Float, h: Float)
    Empty
}

impl User {
    fn greet(self) -> Str => "hi ${self.name}"
    fn domain(self) -> Str {
        if self.email == nil {
            return ""
        }
        let parts = self.email.split("@")
        return parts[1]
    }
}

fn area(s: Shape) -> Float {
    return match s {
        Shape.Circle(r) => 3.14 * r * r
        Shape.Rect(w, h) => w * h
        Shape.Empty => 0.0
    }
}

fn load(path: Str) -> !List[User] uses fs {
    let text = try fs.read(path)
    let data = try json.decode(text)
    var out: List[User] = []
    out.push(User{name: str(data)})
    return out
}

fn first_word(s: Str) -> Str? => s.split(" ").first()

fn main() -> ! uses fs {
    let users = load("u.json") catch e {
        print("error:", e.message)
        []
    }
    let names = users.map(fn(u) => u.name)
    let total = [1, 2, 3].reduce(0, f: fn(acc, x) => acc + x)
    let w = first_word("a b") ?? "none"
    let counts: Map[Str, Int] = {}
    counts["a"] = (counts.get("a") ?? 0) + 1
    for k, v in counts { print(k, v) }
    print(names, total, w.upper(), area(Shape.Rect(1.0, h: 2.0)))
    let t = spawn area(Shape.Empty)
    print(try t.wait())
}

test "area" {
    assert area(Shape.Circle(r: 1.0)) > 3.0
}
`)
}

func TestNames(t *testing.T) {
	ds := expectCodes(t, `
fn main() {
    let count = 1
    print(cont)
}
`, "W0201", "E0201")
	if !strings.Contains(ds[1].Hint, "count") {
		t.Fatalf("hint should suggest 'count': %q", ds[0].Hint)
	}
	expectCodes(t, `
fn main() {
    let s = json.encode(1)
    print(s)
}
`, "E0208")
}

func TestImmutable(t *testing.T) {
	expectCodes(t, `
fn f(n: Int) {
    let x = 1
    x = 2
    n = 3
}
`, "W0201", "E0202", "E0202")
}

func TestShadowing(t *testing.T) {
	expectCodes(t, `
fn main() {
    let x = 1
    if x > 0 {
        let x = 2
        print(x)
    }
    let len = 3
    print(len)
}
`, "E0206", "E0206")
}

func TestTypeErrors(t *testing.T) {
	expectCodes(t, `
fn add(a: Int, b: Int) -> Int => a + b
fn main() {
    let a = add(1, b: "x")
    let b = 1 + 2.5
    let c: Str = 5
    if 1 { print(a, b, c) }
}
`, "E0301", "E0302", "E0301", "E0303")
}

func TestNamedArgsRule(t *testing.T) {
	ds := expectCodes(t, `
fn transfer(amount: Int, from: Str, to: Str) {}
fn main() {
    transfer(10, "alice", "bob")
    transfer(10, from: "alice", to: "bob")
    transfer(10, to: "bob")
}
`, "E0306", "E0306", "E0305")
	if !strings.Contains(ds[0].Hint, `from: "alice"`) {
		t.Fatalf("hint should show the fix: %q", ds[0].Hint)
	}
}

func TestFallibility(t *testing.T) {
	expectCodes(t, `
fn parse(s: Str) -> !Int => try parse_int(s)
fn a() -> Int {
    return parse("1")
}
fn b() -> Int {
    return try parse("1")
}
fn c() -> Int {
    fail "no"
}
fn d() -> Int {
    return parse("1") catch e { 0 }
}
fn e2() -> Int {
    return try 1 + 1
}
`, "E0401", "E0402", "E0403", "E0402", "W0404")
}

func TestEffects(t *testing.T) {
	ds := expectCodes(t, `
import "fs"
import "time"
fn read_it() -> !Str {
    let now = time.now()
    return try fs.read("x")
}
fn unused() uses net {}
`, "W0201", "E0501", "E0501", "W0503")
	if !strings.Contains(ds[1].Hint, "uses clock, fs") {
		t.Fatalf("hint: %q", ds[0].Hint)
	}
}

func TestStructs(t *testing.T) {
	expectCodes(t, `
struct P { x: Int, y: Int, label: Str = "" }
fn main() {
    let p = P{x: 1, z: 2}
    print(p.xx)
}
`, "E0602", "E0601", "E0204")
}

func TestExhaustiveness(t *testing.T) {
	ds := expectCodes(t, `
enum Color { Red, Green, Blue }
fn name(c: Color) -> Str {
    return match c {
        Color.Red => "red"
        Green => "green"
    }
}
fn num(n: Int) -> Str {
    return match n {
        1 => "one"
    }
}
fn ok(c: Color) -> Str {
    return match c {
        Color.Red => "r"
        _ => "other"
    }
}
`, "E0701", "E0701")
	if !strings.Contains(ds[0].Message, "Color.Blue") {
		t.Fatalf("message should list the missing variant: %q", ds[0].Message)
	}
}

func TestNilSafety(t *testing.T) {
	expectCodes(t, `
fn f(m: Map[Str, Str]) -> Int {
    let v = m.get("k")
    let a = v.len()
    if v != nil {
        print(v.len())
    }
    let w = m.get("z")
    if w == nil {
        return 0
    }
    return w.len() + a
}
`, "E0310")
}

func TestMissingReturn(t *testing.T) {
	expectCodes(t, `
fn f(x: Int) -> Int {
    if x > 0 {
        return 1
    }
}
fn g(x: Int) -> Int {
    if x > 0 {
        return 1
    } else {
        return 2
    }
}
`, "E0307")
}

func TestUnknownMethod(t *testing.T) {
	ds := expectCodes(t, `
fn main() {
    let s = "abc"
    print(s.lenght(), [1].psuh(2))
}
`, "E0204", "E0204")
	if !strings.Contains(ds[0].Hint, "'len'") || !strings.Contains(ds[1].Hint, "'push'") {
		t.Fatalf("hints: %q %q", ds[0].Hint, ds[1].Hint)
	}
}

func TestLambdaInference(t *testing.T) {
	expectCodes(t, `
fn main() {
    let xs = [1, 2, 3]
    let ys = xs.map(fn(x) => x * 2.0)
    let zs: List[Str] = xs.map(fn(x) => x + 1)
    print(ys, zs)
}
`, "E0302", "E0301")
}

func TestFixes(t *testing.T) {
	src := `fn transfer(amount: Int, from: Str, to: Str) {}
fn main() {
    let n = 1
    n = 2
    transfer(10, "a", "b")
    let s = json.encode(n)
    print(s)
}
`
	ds := checkSrc(t, src)
	var fixes []diag.Fix
	for _, d := range ds {
		if d.Fix != nil && d.Fix.Safe {
			fixes = append(fixes, *d.Fix)
		}
	}
	out, n := diag.ApplyFixes(src, fixes)
	if n != 4 {
		t.Fatalf("applied %d fixes, want 4:\n%s", n, out)
	}
	want := `import "json"
fn transfer(amount: Int, from: Str, to: Str) {}
fn main() {
    var n = 1
    n = 2
    transfer(10, from: "a", to: "b")
    let s = json.encode(n)
    print(s)
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

func TestExternPython(t *testing.T) {
	expectCodes(t, `
extern python "statistics" {
    fn mean(data: List[Float]) -> !Float
}
extern python "pandas" as pd {
    type DataFrame {
        fn head(self, n: Int = 5) -> !DataFrame
    }
    fn read_csv(path: Str) -> !DataFrame
}

fn avg(xs: List[Float]) -> !Float uses python => try statistics.mean(xs)

fn main() -> ! uses python {
    let df = try pd.read_csv("x.csv")
    let top: pd.DataFrame = try df.head(n: 3)
    print(top, try avg([1.0]))
}
`)
	expectCodes(t, `
extern python "math" {
    fn sqrt(x: Float) -> Float
    type Thing {
        fn f(x: Int) -> !Int
    }
}
fn main() {
    let t = math.Thing{}
    print(math.sqrt(2.0), t)
}
`, "E0610", "E0612", "E0611", "E0501")
	ds := expectCodes(t, `
extern python "pandas" as pd {
    type DataFrame {
        fn describe(self) -> !DataFrame
    }
    fn read_csv(path: Str) -> !DataFrame
}
fn load() -> !pd.DataFrame {
    let df = try pd.read_csv("x.csv")
    return try df.descrbe()
}
extern python "unused_mod" {
    fn f() -> !Int
}
`, "E0501", "E0204", "W0202")
	if !strings.Contains(ds[0].Hint, "uses python") || !strings.Contains(ds[1].Hint, "'describe'") {
		t.Fatalf("hints: %q / %q", ds[0].Hint, ds[1].Hint)
	}
}

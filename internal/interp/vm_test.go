package interp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

// Edge cases for the bytecode VM. Every program runs on both engines
// (runSrc -> runBoth), which must agree on output, result, panic details,
// traces and step counts.

func TestVMLoopsBreakContinueInsideRegions(t *testing.T) {
	expectOut(t, `
fn check(n: Int) -> !Int {
    if n == 3 {
        fail error("three", code: "E3")
    }
    return n
}

fn main() {
    var out: List[Int] = []
    for i in 0..10 {
        let v = check(i) catch e {
            continue
        }
        if v == 6 {
            break
        }
        out.push(v)
    }
    var k = 0
    while true {
        k += 1
        let r = (if k > 4 { break } else { k }) catch e { 0 }
        out.push(r * 100)
    }
    for x in [1, 2, 3] {
        for y in [10, 20, 30] {
            if y == 20 { continue }
            if x == 2 { break }
            out.push(x + y)
        }
    }
    print(out)
}
`, "[0, 1, 2, 4, 5, 100, 200, 300, 400, 11, 31, 13, 33]")
}

func TestVMBreakInsideCallArgument(t *testing.T) {
	expectOut(t, `
fn add(a: Int, b: Int) -> Int => a + b

fn main() {
    var total = 0
    for i in 0..10 {
        total = add(total, b: if i == 5 { break } else { i })
    }
    print(total)
    let words = ["a", "bb", "ccc"]
    var n = 0
    for w in words {
        n += add(w.len(), b: match w.len() {
            2 => { continue }
            _ => 0
        })
    }
    print(n)
}
`, "10\n4")
}

func TestVMClosures(t *testing.T) {
	expectOut(t, `
fn apply(f: fn(Int) -> Int, x: Int) -> Int => f(x)

fn main() {
    var fns: List[fn() -> Int] = []
    for i in 0..3 {
        fns.push(fn() => i * 10)
    }
    print(fns.map(fn(f: fn() -> Int) => f()))

    var shared = 0
    let inc = fn(by: Int) {
        shared += by
    }
    inc(2)
    inc(3)
    print(shared)

    let fact = fn(n: Int) -> Int {
        if n <= 1 { return 1 }
        return n * fact(n - 1)
    }
    print(fact(10))

    let base = 7
    let adder = fn(a: Int) => fn(b: Int) => a + b + base
    print(apply(adder(1), x: 2))

    let r = match 5 {
        x if x > 3 => (fn() => x * 2)()
        _ => 0
    }
    print(r)
}
`, "[0, 10, 20]\n5\n3628800\n10\n10")
}

func TestVMSpawnSharedCell(t *testing.T) {
	expectOut(t, `
fn main() -> ! {
    var count = 0
    let bump = fn(k: Int) -> Int {
        for i in 0..k {
            count += 1
        }
        return count
    }
    let t = spawn bump(50)
    let seen = try t.wait()
    var tasks: List[Task[Int]] = []
    for i in 1..=5 {
        tasks.push(spawn (fn(k: Int) => k * k + seen)(i))
    }
    print(seen, count, try wait_all(tasks))
}
`, "50 50 [51, 54, 59, 66, 75]")
}

func TestVMMatch(t *testing.T) {
	expectOut(t, `
enum Shape {
    Circle(r: Int)
    Rect(w: Int, h: Int)
    Empty
}

fn describe(s: Shape) -> Str => match s {
    Shape.Circle(r) if r > 10 => "big circle ${r}"
    Shape.Circle(r) => "circle ${r}"
    Rect(w, h) | Rect(h, w) if w == h => "square ${w}"
    Rect(w, h) => "rect ${w}x${h}"
    Empty => "empty"
}

fn grade(n: Int) -> Str => match n {
    0 => "zero"
    1..=3 | 7 => "low or seven"
    -1 => "minus one"
    _ => "other"
}

fn main() {
    for s in [Shape.Circle(r: 20), Shape.Circle(r: 2), Shape.Rect(w: 3, h: 3), Shape.Rect(w: 2, h: 5), Shape.Empty] {
        print(describe(s))
    }
    print(grade(0), grade(2), grade(7), grade(-1), grade(9))
}
`, "big circle 20\ncircle 2\nsquare 3\nrect 2x5\nempty\nzero low or seven low or seven minus one other")
}

func TestVMDeferAndFailure(t *testing.T) {
	expectOut(t, `
fn risky(n: Int) -> !Int {
    defer print("defer 1 for ${n}")
    defer print("defer 2 for ${n}")
    if n > 1 {
        fail "too big"
    }
    return n
}

fn early() -> Int {
    defer print("early defer")
    for i in 0..10 {
        let v = (try risky(i)) catch e {
            return i * 100
        }
        print("got", v)
    }
    return -1
}

fn main() {
    print(risky(5) catch e { e.message })
    print(early())
}
`, "defer 2 for 5\ndefer 1 for 5\ntoo big\ndefer 2 for 0\ndefer 1 for 0\ngot 0\ndefer 2 for 1\ndefer 1 for 1\ngot 1\ndefer 2 for 2\ndefer 1 for 2\nearly defer\n200")
}

func TestVMNestedTryCatchInLambdas(t *testing.T) {
	expectOut(t, `
fn parse_all(items: List[Str]) -> !List[Int] {
    return try items.map(fn(s: Str) -> !Int => try parse_int(s))
}

fn main() {
    let a = parse_all(["1", "2"]) catch e { [] }
    let b = parse_all(["1", "x"]) catch e { [-1] }
    let safe = ["4", "y", "6"].map(fn(s: Str) => parse_int(s) catch e { 0 })
    let nested = (try (try parse_int("z"))) catch outer { 42 }
    print(a, b, safe, nested)
}
`, "[1, 2] [-1] [4, 0, 6] 42")
}

func TestVMStringsMapsStructs(t *testing.T) {
	expectOut(t, `
struct Point { x: Int, y: Int = 5, label: Str? }

impl Point {
    fn sum(self) -> Int => self.x + self.y
    fn moved(self, dx: Int) -> Point => Point{x: self.x + dx, y: self.y}
}

fn main() {
    var p = Point{x: 1}
    p.x += 10
    print(p, p.sum(), p.moved(dx: 2).sum())
    var m = {"a": 1, "b": 2}
    m["a"] += 5
    m["c"] = 9
    var xs = [1, 2, 3]
    xs[1] *= 7
    print(m, xs, xs[1..3], "x=${p.x:>5}|${3.14159:.2f}|${255:x}")
    var s = ""
    for k, v in m { s = s + "${k}${v}" }
    for c in "héllo" { s += c }
    for i, v in [7, 8] { s += "${i}:${v}" }
    print(s, nil ?? "dflt", m.get("zz") ?? 0, true and not false, false or true)
}
`, `Point{x: 11, y: 5, label: nil} 16 18
{"a": 6, "b": 2, "c": 9} [1, 14, 3] [14, 3] x=   11|3.14|ff
a6b2c9héllo0:71:8 dflt 0 true true`)
}

// Programs that panic: both engines must report the same panic (code,
// message, position, values, trace) and the same step count.
func TestVMPanicsAgree(t *testing.T) {
	progs := []string{
		`fn main() { let x = [1][5] }`,
		`fn main() { var x = 9223372036854775807
 x += 1 }`,
		`fn main() { let x = -(-9223372036854775807 - 1) }`,
		`fn main() { let a = 3
 a = 4 }`,
		`fn main() { let m = {[1]: 2} }`,
		`fn main() { let s = "${1:zz}" }`,
		`fn main() { let s = "${1:zz} ${[1][3]}" }`,
		`fn main() { let m = {[1]: [0][9]} }`,
		`fn main() { while 1 {} }`,
		`fn main() { for x in 5 {} }`,
		`fn main() { let x = 3
 assert x > 4, "x small" }`,
		`fn main() { let xs = [1, 2]
 let n = 3
 assert xs.contains(n) and n > 0 }`,
		`fn main() { assert 5 }`,
		`struct P { x: Int }
fn main() { let p = P{x: "a"} }`,
		`struct P { x: Int }
fn main() { let p = P{y: 1} }`,
		`struct P { x: Int }
fn main() { let p = P{} }`,
		`fn main() { let x = match 3 { 1 => 0 } }`,
		"fn main() { let x = match 3 {\n n if n => 0\n _ => 1\n } }",
		`fn f() -> !Int { fail "boom" }
fn main() { let x = f() }`,
		`fn f() -> Int { fail "boom" }
fn main() { let x = f() }`,
		`fn f(n: Int) -> Int
    requires n > 0
    ensures result < 10
{ return n * 5 }
fn main() { print(f(1))
 print(f(3)) }`,
		`fn f(n: Int) -> Int requires n > 0 { return n }
fn main() { f(0) }`,
		`fn f() -> Int { }
fn main() { f() }`,
		`fn deep(n: Int) -> Int { if n == 0 { return [1][2] }
 return deep(n - 1) }
fn main() { deep(5) }`,
		`fn main() { var i = 0
 while true { i += 1 } }`,
		`fn main() { let f = fn() { break }
 for i in 0..3 { f() } }`,
		`fn main() { let s: Int = "x" }`,
		`fn main() { let r = "a"..3 }`,
		`fn main() { print(undefined_name) }`,
		`fn main() { nope = 3 }`,
		`fn main() { let x = 1.5 + 1 }`,
		`fn main() { let x = 1 < "a" }`,
		`fn main() { let x = 5 / 0 }`,
		`fn main() { let b = 1 and true }`,
		`fn main() { let b = true and 1 }`,
		`fn main() { let x = not 3 }`,
		`fn main() { let x = [1].nope }`,
		`fn main() { let x = nil.foo }`,
		`fn main() -> ! { fail error("bad", code: "E_BAD") }`,
		`fn main() -> ! { fail 3 }`,
	}
	for _, src := range progs {
		_, res := runSrc(t, src)
		if res.Status == "ok" {
			t.Errorf("expected a panic or failure:\n%s", src)
		}
	}
}

func TestVMStepBudgetAgrees(t *testing.T) {
	src := `
fn f(n: Int) -> Int {
    var t = 0
    for i in 0..n {
        t += if i % 2 == 0 { i } else { 0 }
    }
    return t
}
fn main() {
    var i = 0
    while i < 1000 {
        print(f(i))
        i += 1
    }
}
`
	for _, max := range []int64{1, 7, 50, 333, 5000} {
		dir := t.TempDir()
		p := filepath.Join(dir, "main.pg")
		os.WriteFile(p, []byte(src), 0o644)
		prog, ds := loader.Load(p, nil)
		if diag.HasErrors(ds) {
			t.Fatal(ds)
		}
		_, res := runBoth(t, prog, Options{MaxSteps: max})
		if res.Panic == nil || res.Panic.Code != PBudget {
			t.Fatalf("max %d: want budget panic, got %+v", max, res)
		}
	}
}

func TestVMTestBlocksAgree(t *testing.T) {
	src := `
fn double(n: Int) -> Int => n * 2
fn must(n: Int) -> !Int {
    if n < 0 { fail "negative" }
    return n
}

test "passes" {
    assert double(2) == 4
    let f = fn(x: Int) => x + 1
    assert f(1) == 2
}

test "fails on assert" {
    let x = 3
    assert double(x) == 7
}

test "try propagates" {
    let v = try must(-1)
}

test "returns early" {
    for i in 0..3 {
        if i == 1 { return }
    }
    assert false
}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatal(ds)
	}
	var results [2]string
	for i, engine := range []string{"tree", "vm"} {
		var out bytes.Buffer
		in := New(prog, Options{Stdout: &out, Stderr: &out, Engine: engine})
		trs := in.RunTests(nil, "")
		for j := range trs {
			trs[j].Millis = 0
		}
		b, _ := json.MarshalIndent(trs, "", "  ")
		results[i] = string(b)
		if fb := in.VMFallbacks(); len(fb) > 0 {
			t.Errorf("not compiled: %v", fb)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("engines differ\n--- tree ---\n%s\n--- vm ---\n%s", results[0], results[1])
	}
	if !strings.Contains(results[0], `"passed": true`) || strings.Count(results[0], `"passed": false`) != 2 {
		t.Fatalf("unexpected results: %s", results[0])
	}
}

package interp

import (
	"strings"
	"testing"
)

func TestErrorsTryCatch(t *testing.T) {
	expectOut(t, `
fn half(n: Int) -> !Int {
    if n % 2 != 0 {
        fail error("odd number", code: "E_ODD", data: n)
    }
    return n / 2
}

fn quarter(n: Int) -> !Int {
    let h = try half(n)
    return try half(h)
}

fn main() {
    print(quarter(8) catch e { -1 })
    let r = quarter(6) catch e {
        print("caught", e.message, e.code, e.data)
        0
    }
    print(r)
    let n = parse_int("12x") catch e { 99 }
    print(n)
}
`, "2\ncaught odd number E_ODD 3\n0\n99")
}

func TestUnhandledFailureIsPanic(t *testing.T) {
	_, res := runSrc(t, `
fn f() -> !Int { fail "boom" }
fn main() { let x = f() }
`)
	if res.Status != "panic" || res.Panic.Code != PUnhandled {
		t.Fatalf("want unhandled panic, got %s %+v", res.Status, res.Panic)
	}
	if !strings.Contains(res.Panic.Hint, "try f()") {
		t.Fatalf("hint should propose try: %q", res.Panic.Hint)
	}
}

func TestMainFailureExitCode(t *testing.T) {
	_, res := runSrc(t, `
fn main() -> ! { fail error("bad config", code: "E_CFG") }
`)
	if res.ExitCode != ExitFailure || res.Error.Code != "E_CFG" {
		t.Fatalf("got %+v", res)
	}
}

func TestEnumMatch(t *testing.T) {
	expectOut(t, `
enum Shape {
    Circle(r: Float)
    Rect(w: Float, h: Float)
    Empty
}

fn area(s: Shape) -> Float {
    return match s {
        Shape.Circle(r) => 3.0 * r * r
        Shape.Rect(w, h) => w * h
        Shape.Empty => 0.0
    }
}

fn grade(n: Int) -> Str {
    return match n {
        90..=100 => "A"
        60..90 => "B"
        0 | 1 => "zero-or-one"
        x if x < 0 => "negative ${x}"
        _ => "F"
    }
}

fn main() {
    let shapes = [Shape.Circle(r: 1.0), Shape.Rect(2.0, h: 3.0), Shape.Empty]
    for s in shapes { print(area(s)) }
    print(grade(95), grade(70), grade(1), grade(-3), grade(10))
    print(shapes[1])
}
`, "3.0\n6.0\n0.0\nA B zero-or-one negative -3 F\nShape.Rect(w: 2.0, h: 3.0)")
}

func TestStructImpl(t *testing.T) {
	expectOut(t, `
struct User {
    name: Str
    age: Int = 18
    email: Str?
}

impl User {
    fn new(name: Str) -> User => User{name: name}
    fn greet(self) -> Str => "hi ${self.name} (${self.age})"
    fn birthday(self) { self.age += 1 }
}

fn main() {
    let u = User.new("ada")
    u.birthday()
    print(u.greet(), u.email == nil)
    print(u)
}
`, "hi ada (19) true\nUser{name: \"ada\", age: 19, email: nil}")
}

func TestContracts(t *testing.T) {
	_, res := runSrc(t, `
fn sqrt_int(n: Int) -> Int
    requires n >= 0
    ensures result * result <= n
{
    var r = 0
    while (r + 1) * (r + 1) <= n { r += 1 }
    return r
}
fn main() {
    print(sqrt_int(10))
    print(sqrt_int(-4))
}
`)
	if res.Status != "panic" || res.Panic.Code != PContract {
		t.Fatalf("want contract panic, got %+v", res)
	}
	if res.Panic.Values["n"] != "-4" {
		t.Fatalf("contract panic should report n = -4, got %v", res.Panic.Values)
	}
}

func TestAssertValues(t *testing.T) {
	_, res := runSrc(t, `
fn main() {
    let xs = [1, 2, 3]
    assert xs.len() == 4, "size"
}
`)
	if res.Panic == nil || res.Panic.Code != PAssert || res.Panic.Values["xs.len()"] != "3" {
		t.Fatalf("got %+v", res.Panic)
	}
}

func TestCollections(t *testing.T) {
	expectOut(t, `
fn main() {
    var xs = [5, 3, 8, 1]
    xs.push(4)
    print(xs.sorted(), xs.sorted(desc: true), xs.map(fn(x) => x * 2), xs.filter(fn(x) => x > 3))
    print(xs.reduce(0, f: fn(acc, x) => acc + x), xs.sum(), xs.min(), xs.max(), xs[1..3], xs.len())
    let m = {"b": 2, "a": 1}
    m["c"] = 3
    for k, v in m { print(k, v) }
    print(m.get("zz") ?? 0, m.has("a"), m.keys())
    let words = "the quick brown the".split(" ")
    var counts: Map[Str, Int] = {}
    for w in words { counts[w] = (counts.get(w) ?? 0) + 1 }
    print(counts)
    print("héllo"[1], "héllo".len(), "a,b".upper(), 7 / 2, 7 % 3, -7 / 2)
    print("${3.14159:.2}|${42:>5}|${"x":<3}|${255:x}|${0.5:%}")
    for i, x in ["a", "b"] { print(i, x) }
    for i in 0..3 { print(i) }
}
`, `[1, 3, 4, 5, 8] [8, 5, 4, 3, 1] [10, 6, 16, 2, 8] [5, 8, 4]
21 21 1 8 [3, 8] 5
b 2
a 1
c 3
0 true ["b", "a", "c"]
{"the": 2, "quick": 1, "brown": 1}
é 5 A,B 3 1 -3
3.14|   42|x  |ff|50.000000%
0 a
1 b
0
1
2`)
}

func TestRuntimeErrors(t *testing.T) {
	cases := map[string]string{
		`fn main() { let x = [1][5] }`:  PIndex,
		`fn main() { let x = 1 + 1.5 }`: PType,
		`fn main() { let m = {"a": 1}
 let x = m["b"] }`: PKey,
		`fn main() { let x = 1 / 0 }`:                   PDivZero,
		`fn main() { let x = 9223372036854775807 + 1 }`: POverflow,
		`fn main() { if 1 { print(1) } }`:               PType,
		`fn f(x: Int) -> Int => x
fn main() { f("a") }`: PType,
		`fn main() { while true {} }`: PBudget,
	}
	for src, code := range cases {
		_, res := runSrc(t, src)
		if res.Panic == nil || res.Panic.Code != code {
			t.Errorf("%s\n want %s, got %+v", src, code, res.Panic)
		}
	}
}

func TestConcurrency(t *testing.T) {
	expectOut(t, `
fn square(n: Int) -> Int => n * n

fn producer(ch: Chan[Int], n: Int) {
    for i in 0..n { ch.send(i) }
    ch.close()
}

fn main() -> ! {
    var tasks = []
    for i in 1..=5 { tasks.push(spawn square(i)) }
    print(try wait_all(tasks))
    let ch = chan(2)
    spawn producer(ch, n: 4)
    var total = 0
    for v in ch { total += v }
    print(total)
    let t = spawn square(9)
    print(try t.wait())
}
`, "[1, 4, 9, 16, 25]\n6\n81")
}

func TestJSON(t *testing.T) {
	expectOut(t, `
import "json"

struct Item { id: Int, tags: List[Str], price: Float, note: Str? }

fn main() -> ! {
    let it = try json.decode_as("{\"id\": 1, \"tags\": [\"a\"], \"price\": 2}", schema: Item)
    print(it)
    print(json.encode(it))
    let bad = json.decode_as("{\"id\": \"x\"}", schema: Item) catch e { e.message }
    print(bad)
    let any = try json.decode("[1, 2.5, {\"b\": null}]")
    print(any)
}
`, `Item{id: 1, tags: ["a"], price: 2.0, note: nil}
{"id":1,"tags":["a"],"price":2.0,"note":null}
$.id: expected Int, got string
[1, 2.5, {"b": nil}]`)
}

func TestPermissionDenied(t *testing.T) {
	_, res := runSrc(t, `
import "fs"
fn main() -> ! uses fs { let s = try fs.read("/etc/hostname") }
`)
	if res.ExitCode != ExitPermission || !strings.Contains(res.Panic.Hint, "--allow fs") {
		t.Fatalf("got %+v", res.Panic)
	}
}

func TestDeferAndClosures(t *testing.T) {
	expectOut(t, `
fn make_counter() -> fn() -> Int {
    var n = 0
    return fn() {
        n += 1
        return n
    }
}

fn work() {
    defer print("cleanup")
    print("working")
}

fn main() {
    let c = make_counter()
    c()
    print(c())
    work()
}
`, "2\nworking\ncleanup")
}

package interp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

// These tests run a real Python 3 (python3 on PATH, or PYGO_PYTHON).
// CI installs it on every OS; they are never skipped.

func runPy(t *testing.T, src string, opt Options) (string, *Result) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	if opt.Allow == nil {
		opt.Allow = map[string]bool{"python": true}
	}
	return runBoth(t, prog, opt)
}

func expectPy(t *testing.T, src, want string) {
	t.Helper()
	out, res := runPy(t, src, Options{})
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(want) {
		t.Fatalf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestPythonCalls(t *testing.T) {
	expectPy(t, `
extern python "statistics" {
    fn mean(data: List[Float]) -> !Float
}
extern python "math" {
    fn pow(x: Float, y: Float) -> !Float
    fn factorial(n: Int) -> !Int
}
extern python "json" as pyjson {
    fn dumps(obj: Any, sort_keys: Bool = false, indent: Int? = nil) -> !Str
    fn loads(s: Str) -> !Any
}
extern python "builtins" as py {
    fn sorted(items: List[Str], key: Any = nil, reverse: Bool = false) -> !List[Str]
    fn divmod(a: Int, b: Int) -> !List[Int]
}

fn main() -> ! uses python {
    print(try statistics.mean([1.0, 2.0, 4.5]))
    print(try math.pow(2.0, y: 0.5) > 1.41, try math.factorial(20))
    print(try pyjson.dumps({"b": 1, "a": [1.5, true, nil]}, sort_keys: true))
    let back = try pyjson.loads("{\"x\": [1, 2.0, \"s\"]}")
    print(back)
    print(try py.sorted(["b", "C", "a"], reverse: true), try py.divmod(17, b: 5))
}
`, `2.5
true 2432902008176640000
{"a": [1.5, true, null], "b": 1}
{"x": [1, 2.0, "s"]}
["b", "a", "C"] [3, 2]`)
}

func TestPythonHandles(t *testing.T) {
	expectPy(t, `
extern python "collections" {
    type Counter {
        fn most_common(self, n: Int? = nil) -> !List[List[Any]]
        fn total(self) -> !Int
    }
    fn Counter(items: List[Str]) -> !Counter
}

fn main() -> ! uses python {
    let c = try collections.Counter(["a", "b", "a", "c", "a", "b"])
    print(type_of(c), try c.total())
    print(try c.most_common(n: 2))
}
`, `collections.Counter 6
[["a", 3], ["b", 2]]`)
}

func TestPythonErrors(t *testing.T) {
	expectPy(t, `
extern python "math" {
    fn sqrt(x: Float) -> !Float
}
extern python "pygo_surely_missing_module" as missing {
    fn f() -> !Int
}
extern python "builtins" as py {
    fn str(x: Any) -> !Int
    fn pow(base: Int, exp: Int) -> !Int
}

fn main() -> ! uses python {
    let a = math.sqrt(-1.0) catch e {
        print(e.code, e.message)
        0.0
    }
    let b = missing.f() catch e {
        print(e.code, e.message.contains("pip install pygo_surely_missing_module"))
        0
    }
    let c = py.str(5) catch e {
        print(e.code, e.message)
        0
    }
    let d = py.pow(10, exp: 30) catch e {
        print(e.code)
        0
    }
    print(a, b, c, d)
}
`, `E_PYTHON ValueError: math domain error
E_PYTHON_IMPORT true
E_PYTHON_TYPE str returned a value that does not match Int: result: expected Int, got string
E_PYTHON_TYPE
0.0 0 0 0`)
}

func TestPythonUnhandledFailureShowsTraceback(t *testing.T) {
	_, res := runPy(t, `
extern python "math" {
    fn sqrt(x: Float) -> !Float
}
fn main() -> ! uses python {
    print(try math.sqrt(-1.0))
}
`, Options{})
	if res.Status != "failure" || res.Error.Code != "E_PYTHON" {
		t.Fatalf("got %+v", res)
	}
}

func TestPythonCapabilityDenied(t *testing.T) {
	_, res := runPy(t, `
extern python "math" {
    fn sqrt(x: Float) -> !Float
}
fn main() -> ! uses python {
    print(try math.sqrt(4.0))
}
`, Options{Allow: map[string]bool{}})
	if res.ExitCode != ExitPermission {
		t.Fatalf("want exit %d, got %+v", ExitPermission, res.Panic)
	}
}

func TestPythonTimeoutKillsWorker(t *testing.T) {
	start := time.Now()
	_, res := runPy(t, `
extern python "time" as pytime {
    fn sleep(seconds: Float) -> !Any
}
fn main() -> ! uses python {
    try pytime.sleep(30.0)
}
`, Options{Timeout: 500 * time.Millisecond})
	if res.Panic == nil || res.Panic.Code != PTimeout {
		t.Fatalf("want timeout panic, got %+v", res)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

func TestPythonConcurrentCalls(t *testing.T) {
	expectPy(t, `
extern python "math" {
    fn factorial(n: Int) -> !Int
}
fn fact(n: Int) -> !Int uses python => try math.factorial(n)

fn main() -> ! uses python {
    var tasks: List[Task[Int]] = []
    for i in 0..10 {
        tasks.push(spawn fact(i))
    }
    print(try wait_all(tasks))
}
`, `[1, 1, 2, 6, 24, 120, 720, 5040, 40320, 362880]`)
}

func TestPythonStdoutDoesNotCorruptProtocol(t *testing.T) {
	out, res := runPy(t, `
extern python "builtins" as py {
    fn print(values: ...Str) -> !Any
    fn len(x: Str) -> !Int
}
fn main() -> ! uses python {
    try py.print("printed by python")
    print(try py.len("four"))
}
`, Options{})
	if res.Status != "ok" || !strings.Contains(out, "printed by python") || !strings.Contains(out, "4") {
		t.Fatalf("status %s, output %q", res.Status, out)
	}
}

func TestPythonWrongParameterName(t *testing.T) {
	expectPy(t, `
extern python "math" {
    fn isclose(a: Float, b: Float, tolerance: Float = 0.1) -> !Bool
}
fn main() -> ! uses python {
    let r = math.isclose(1.0, b: 1.05, tolerance: 0.1) catch e {
        print(e.message.contains("has no parameter 'tolerance'"), e.message.contains("rel_tol"))
        false
    }
    print(r)
}
`, "true true\nfalse")
}

func TestPythonUnicode(t *testing.T) {
	expectPy(t, `
extern python "unicodedata" {
    fn name(chr: Str) -> !Str
}
extern python "builtins" as py {
    fn len(x: Str) -> !Int
    fn repr(x: Any) -> !Str
}
fn main() -> ! uses python {
    let s = "héllo 世界 🙂"
    print(try py.len(s), try unicodedata.name("é"))
    print(try py.repr({"k": s}))
}
`, "10 LATIN SMALL LETTER E WITH ACUTE\n{'k': 'héllo 世界 🙂'}")
}

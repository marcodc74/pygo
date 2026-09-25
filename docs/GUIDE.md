# Pygo quick reference (for AI agents)

Pygo is statically checked, strict and unambiguous. There is ONE way to write each thing.
Workflow: write file.pg → `pygo check --json file.pg` → `pygo fix file.pg` (safe fixes) → `pygo test file.pg` → `pygo run --allow <caps> file.pg`.

## Syntax
- Blocks use `{}`; one statement per line; no `;`. Newlines inside `()`/`[]` are ignored; a line starting with `.m()` continues a call chain.
- Comments `//`; doc comments `///` before a declaration.
- `let x = 1` (immutable), `var n = 0` (mutable), `let xs: List[Int] = []` (annotate empty/nil values).
- No shadowing: a name is unique within a function and cannot hide module-level or builtin names (`len`, `str`, ...).
- `if c { } else if d { } else { }` — `else` on the same line as `}`. `if`/`match`/blocks are expressions (value = last line).
- `for x in xs { }`, `for i, x in xs { }`, `for k, v in m { }`, `for i in 0..n { }` (`0..=n` inclusive), `for v in ch { }`, `while c { }`, `break`, `continue`.
- Operators: `+ - * / %` (Int/Int → Int, truncating), `== != < <= > >=`, `and or not`, `in`, `??` (nil default), `a..b`. Mixing `and`/`or`, or `??` with other operators, needs parentheses. No chained comparisons.
- Strings: `"a ${expr} b"`, format `"${x:.2}" "${n:>5}" "${n:05}" "${n:x}" "${r:%}"`; `{` `}` are plain characters; `\${` is a literal `${`. Raw: `r"C:\dir"`, multi-line: `"""..."""`, raw multi-line `r"""{"json": true}"""`.
- Literals: `42 1_000 0xff 3.14 1e-9 true false nil [1, 2] {"k": 1}`; empty map `{}`.

## Types
`Int` (64-bit, overflow = panic) `Float` `Str` (runes) `Bool` `List[T]` `Map[K, V]` (insertion ordered; keys Int/Str/Bool/Float) `T?` (optional, the only place nil exists) `fn(A, B) -> R` `fn(A) -> !R` `Chan[T]` `Task[T]` `Range` `Error` `Any`.
- No implicit conversions: `float(n)`, `int(x)` (truncates), `str(x)`, `try parse_int(s)`, `try parse_float(s)`.
- No truthiness: conditions are Bool (`not xs.is_empty()`, `x != nil`).
- Nil safety: using a `T?` value requires a check. `if x != nil { x.len() }`, `if x == nil { return }` then x is non-nil, or `x ?? default`.

## Functions
```
/// Doc comment.
fn area(w: Float, h: Float = 1.0) -> Float {
    return w * h
}
fn double(n: Int) -> Int => n * 2
fn first[T](xs: List[T]) -> T? => xs.first()
```
- Parameter types are mandatory (lambdas may omit them): `fn(x) => x * 2`, `fn(a: Int) -> Int { return a }`.
- CALL RULE: only the FIRST argument is positional, all others are named: `area(2.0, h: 3.0)`, `transfer(10, from: a, to: b)`. Variadic `print(a, b, c)` is the exception.
- A function with a result type must `return` on every path. `main` is `fn main()` or `fn main() -> !` (use `os.args()`, `os.exit(n)`).
- Contracts: `requires <cond>` / `ensures <cond using result>` between the signature and `{`.

## Errors
- `-> !T` = may fail (`-> !` = may fail, no value). `fail error("msg", code: "E_X", data: v)` or `fail "msg"`.
- Calling a fallible function REQUIRES `try` or `catch`:
  - `let v = try f()` propagates (the current function must be `-> !T`).
  - `let v = f() catch e { default_value }` handles it; `e.message`, `e.code`, `e.data`. The catch block may `return`/`fail`.
- Bugs (index out of range, missing key with `m[k]`, division by zero, overflow, failed assert/contract) are panics: not catchable, exit code 2.

## Structs, enums, match
```
struct User {
    name: Str
    age: Int = 0
    email: Str?
}
impl User {
    fn new(name: Str) -> User => User{name: name}
    fn greet(self) -> Str => "hi ${self.name}"
}
enum Shape {
    Circle(r: Float)
    Rect(w: Float, h: Float)
    Empty
}
let s = Shape.Rect(2.0, h: 3.0)
let a = match s {
    Shape.Circle(r) => 3.14 * r * r
    Shape.Rect(w, h) if w == h => w * w
    Shape.Rect(w, h) => w * h
    Shape.Empty => 0.0
}
```
- Struct literal `User{name: "a"}`: fields with a default or optional type may be omitted. Objects are references; `let` only prevents rebinding.
- Patterns: `_`, `name`, literals, `1 | 2`, `lo..=hi`, `lo..hi`, `Enum.Variant(p, _)`, guards `if cond`. match must be exhaustive (all variants, or `_`).

## Effects (capabilities)
Functions declare effects: `fn fetch(url: Str) -> !Str uses net { ... }`. Effects: `fs net env proc clock rand python`. Callers of effectful functions must declare them too. The runner grants them: `pygo run --allow net,fs app.pg` (default: none; missing → exit 4). print/log/time.sleep need no effect.

## Concurrency
`let t = spawn work(x)` → `Task[T]`; `try t.wait()`; `try wait_all(tasks)`. Channels: `let ch = chan(10)`, `ch.send(v)`, `ch.recv()` (T?, nil when closed), `ch.close()`, `for v in ch { }`. Lists/maps are thread-safe; prefer channels to shared state.

## Tests
`test "name" { assert expr, "optional message" }` in any file; `try` is allowed inside tests. Run `pygo test --json file_or_dir`. Failed asserts report operand values.

## Modules
`import "json"` (stdlib: json fs os http time log math re proc rand), `import "./lib/geo"` (local file geo.pg, used as `geo.area(...)`), `import "./x" as y`. All top-level names are public.

## Python libraries (extern)
Declare exactly what you use; the checker validates calls like any Pygo function:
```
extern python "statistics" {
    fn mean(data: List[Float]) -> !Float
}
extern python "pandas" as pd {
    type DataFrame {                      // Python object kept in Python (handle)
        fn head(self, n: Int = 5) -> !DataFrame
        fn to_dict(self, orient: Str = "records") -> !List[Map[Str, Any]]
    }
    fn read_csv(path: Str) -> !DataFrame
}
fn main() -> ! uses python { print(try statistics.mean([1.0, 2.0])) }
```
- Every extern fn/method is fallible (`-> !T`) and uses the `python` effect (run with `--allow python`).
- Parameter names must match the Python ones; the bridge passes positional-only parameters by position, others by keyword. `x: T? = nil` omitted → Python default.
- Results are checked against the declared type (`E_PYTHON_TYPE`); exceptions → failure `E_PYTHON` (traceback in `e.data`); missing package → `E_PYTHON_IMPORT`.
- Generate declarations instead of guessing: `pygo extern python MODULE [NAME ...]`.

## Exit codes
0 ok · 1 unhandled failure in main · 2 panic · 3 compile errors · 4 capability denied.

## Standard library (signatures)

### builtins
```
fn chan(capacity: Int = 0) -> Chan[Any]
fn eprint(values: ...Any)
fn error(message: Str, code: Str = "", data: Any = nil) -> Error
fn float(x: Any) -> Float
fn int(x: Any) -> Int
fn len(x: Any) -> Int
fn panic(message: Str)
fn parse_float(text: Str) -> !Float
fn parse_int(text: Str) -> !Int
fn print(values: ...Any)
fn read_all() -> Str
fn read_line() -> Str?
fn repr(x: Any) -> Str
fn str(x: Any) -> Str
fn type_of(x: Any) -> Str
fn wait_all[T](tasks: List[Task[T]]) -> !List[T]
struct Error { message: Str, code: Str, data: Any }
```

### Str methods
```
fn chars() -> List[Str]
fn contains(sub: Str) -> Bool
fn ends_with(suffix: Str) -> Bool
fn find(sub: Str) -> Int?
fn is_empty() -> Bool
fn len() -> Int
fn lines() -> List[Str]
fn lower() -> Str
fn pad_left(width: Int, fill: Str = " ") -> Str
fn pad_right(width: Int, fill: Str = " ") -> Str
fn repeat(count: Int) -> Str
fn replace(old: Str, new: Str) -> Str
fn split(sep: Str) -> List[Str]
fn starts_with(prefix: Str) -> Bool
fn trim() -> Str
fn trim_end() -> Str
fn trim_start() -> Str
fn upper() -> Str
```

### List methods (T = element type)
```
fn all(f: fn(T) -> Bool) -> Bool
fn any(f: fn(T) -> Bool) -> Bool
fn clear()
fn contains(item: T) -> Bool
fn copy() -> List[T]
fn count(f: fn(T) -> Bool) -> Int
fn extend(other: List[T])
fn filter(f: fn(T) -> Bool) -> List[T]
fn find(f: fn(T) -> Bool) -> T?
fn first() -> T?
fn index_of(item: T) -> Int?
fn insert(index: Int, item: T)
fn is_empty() -> Bool
fn join(sep: Str) -> Str
fn last() -> T?
fn len() -> Int
fn map[U](f: fn(T) -> U) -> List[U]
fn max() -> T?
fn min() -> T?
fn pop() -> T?
fn push(item: T)
fn reduce[U](init: U, f: fn(U, T) -> U) -> U
fn remove(index: Int) -> T
fn reversed() -> List[T]
fn sorted(key: fn(T) -> Any = nil, desc: Bool = false) -> List[T]
fn sum() -> T
fn unique() -> List[T]
```

### Map methods (K = key, V = value)
```
fn clear()
fn copy() -> Map[K, V]
fn delete(key: K) -> V?
fn get(key: K) -> V?
fn has(key: K) -> Bool
fn is_empty() -> Bool
fn keys() -> List[K]
fn len() -> Int
fn values() -> List[V]
```

### Range methods
```
fn contains(n: Int) -> Bool
fn len() -> Int
fn to_list() -> List[Int]
```

### Chan methods (T = element type)
```
fn close()
fn len() -> Int
fn recv() -> T?
fn send(value: T)
```

### Task methods (T = element type)
```
fn done() -> Bool
fn wait() -> !T
```

### import "fs"
```
fn fs.read(path: Str) -> !Str uses fs
fn fs.write(path: Str, data: Str) -> ! uses fs
fn fs.append(path: Str, data: Str) -> ! uses fs
fn fs.exists(path: Str) -> Bool uses fs
fn fs.list(dir: Str) -> !List[Str] uses fs
fn fs.remove(path: Str) -> ! uses fs
fn fs.mkdir(path: Str) -> ! uses fs
```

### import "http"
```
struct http.Request { method: Str, path: Str, query: Map[Str, Str], headers: Map[Str, Str], body: Str }
struct http.Response { status: Int, body: Str = "", headers: Map[Str, Str] = {} }
fn http.get(url: Str, headers: Map[Str, Str] = {}, timeout_ms: Int = 30000) -> !Response uses net
fn http.post(url: Str, body: Str, headers: Map[Str, Str] = {}, timeout_ms: Int = 30000) -> !Response uses net
fn http.request(method: Str, url: Str, body: Str = "", headers: Map[Str, Str] = {}, timeout_ms: Int = 30000) -> !Response uses net
fn http.serve(addr: Str, handler: fn(Request) -> Response) -> ! uses net
fn http.text(status: Int, body: Str) -> Response
fn http.json(status: Int, body: Any) -> Response
```

### import "json"
```
fn json.encode(value: Any, indent: Int = 0) -> Str
fn json.decode(text: Str) -> !Any
fn json.decode_as[T](text: Str, schema: Type[T]) -> !T
```

### import "log"
```
fn log.debug(message: Str, fields: Map[Str, Any] = {})
fn log.info(message: Str, fields: Map[Str, Any] = {})
fn log.warn(message: Str, fields: Map[Str, Any] = {})
fn log.error(message: Str, fields: Map[Str, Any] = {})
```

### import "math"
```
let math.pi = 3.141592653589793
let math.e = 2.718281828459045
fn math.abs[T](x: T) -> T
fn math.sqrt(x: Float) -> Float
fn math.pow(base: Float, exp: Float) -> Float
fn math.exp(x: Float) -> Float
fn math.log(x: Float) -> Float
fn math.sin(x: Float) -> Float
fn math.cos(x: Float) -> Float
fn math.tan(x: Float) -> Float
fn math.floor(x: Float) -> Int
fn math.ceil(x: Float) -> Int
fn math.round(x: Float) -> Int
fn math.is_nan(x: Float) -> Bool
```

### import "os"
```
fn os.args() -> List[Str]
fn os.env(name: Str) -> Str? uses env
fn os.exit(code: Int)
fn os.platform() -> Str
fn os.cwd() -> Str uses env
```

### import "proc"
```
struct proc.Result { code: Int, stdout: Str, stderr: Str }
fn proc.run(cmd: List[Str], stdin: Str = "", timeout_ms: Int = 60000) -> !Result uses proc
```

### import "rand"
```
fn rand.int(lo: Int, hi: Int) -> Int uses rand
fn rand.float() -> Float uses rand
fn rand.choice[T](items: List[T]) -> T? uses rand
fn rand.shuffle[T](items: List[T]) -> List[T] uses rand
fn rand.uuid() -> Str uses rand
```

### import "re"
```
fn re.matches(pattern: Str, text: Str) -> !Bool
fn re.find(pattern: Str, text: Str) -> !Str?
fn re.find_all(pattern: Str, text: Str) -> !List[Str]
fn re.groups(pattern: Str, text: Str) -> !List[Str]?
fn re.replace(pattern: Str, text: Str, with: Str) -> !Str
fn re.split(pattern: Str, text: Str) -> !List[Str]
```

### import "time"
```
fn time.now() -> Float uses clock
fn time.now_ms() -> Int uses clock
fn time.iso() -> Str uses clock
fn time.sleep(ms: Int)
```

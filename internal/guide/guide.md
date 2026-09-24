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
Functions declare effects: `fn fetch(url: Str) -> !Str uses net { ... }`. Effects: `fs net env proc clock rand`. Callers of effectful functions must declare them too. The runner grants them: `pygo run --allow net,fs app.pg` (default: none; missing → exit 4). print/log/time.sleep need no effect.

## Concurrency
`let t = spawn work(x)` → `Task[T]`; `try t.wait()`; `try wait_all(tasks)`. Channels: `let ch = chan(10)`, `ch.send(v)`, `ch.recv()` (T?, nil when closed), `ch.close()`, `for v in ch { }`. Lists/maps are thread-safe; prefer channels to shared state.

## Tests
`test "name" { assert expr, "optional message" }` in any file; `try` is allowed inside tests. Run `pygo test --json file_or_dir`. Failed asserts report operand values.

## Modules
`import "json"` (stdlib: json fs os http time log math re proc rand), `import "./lib/geo"` (local file geo.pg, used as `geo.area(...)`), `import "./x" as y`. All top-level names are public.

## Exit codes
0 ok · 1 unhandled failure in main · 2 panic · 3 compile errors · 4 capability denied.

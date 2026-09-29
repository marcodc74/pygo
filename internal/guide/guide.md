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
`Int` (64-bit, overflow = panic) `Float` `Str` (runes) `Bool` `Html` (safe markup, see HTML) `List[T]` `Map[K, V]` (insertion ordered; keys Int/Str/Bool/Float) `T?` (optional, the only place nil exists) `fn(A, B) -> R` `fn(A) -> !R` `Chan[T]` `Task[T]` `Range` `Error` `Any`.
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
- Limits: `--max-steps`/`--timeout` abort with panics `R0011`/`R0012`; too-deep recursion is `R0018`. A channel/task await blocks until its peer, so pass `--timeout` to bound it.

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
Functions declare effects: `fn fetch(url: Str) -> !Str uses net { ... }`. Effects: `fs net env proc clock rand python crypto sql`. Callers of effectful functions must declare them too, and so must a function that uses an effectful function as a value (callback, route handler). The runner grants them: `pygo run --allow net,fs app.pg` (default: none; missing → exit 4). print/log/time.sleep need no effect. Hashing, HMAC, PBKDF2 and JWT are pure (no effect); only `crypto.random_bytes` needs the `crypto` grant. Database access needs the `sql` grant.

## Concurrency
`let t = spawn work(x)` → `Task[T]`; `try t.wait()`; `try wait_all(tasks)`. Channels: `let ch = chan(10)`, `ch.send(v)`, `ch.recv()` (T?, nil when closed), `ch.close()`, `for v in ch { }`. Lists/maps are thread-safe; prefer channels to shared state.

## HTML
Markup has type `Html` and is written ONLY as `html"..."` / `html"""..."""`. Each `${x}` is escaped for where it appears (text, attribute, URL, `<script>`); an `Html` value is inserted as it is; a `List[Html]` is concatenated. `${}` accepts Str Int Float Bool Html List[Html]. A `Str` never becomes `Html`, so injected markup is impossible.
```
fn row(u: User) -> Html => html"<li data-age='${u.age}'>${u.name}</li>"
fn page(users: List[User]) -> http.Response => http.html(200, body: html"<ul>${users.map(row)}</ul>")
```
- Each literal is a complete fragment: close tags, quotes and comments inside it (E0312). Use `'` for attributes in one-line literals, or `"""`.
- `html.raw(s)` (`import "html"`) marks text as markup WITHOUT escaping: only for markup you wrote, never for input.

## HTTP server
```
fn routes() -> List[http.Route] uses fs {
    return [
        http.Route{method: "GET", path: "/items/{id}", handler: show},   // req.params["id"]
        http.Route{method: "POST", path: "/items", handler: create},      // try http.form(req) or json.decode_as(req.body, schema: T)
        http.Route{method: "GET", path: "/static/{path...}", handler: fn(req) => http.static(req, dir: "public")},
    ]
}
fn main() -> ! uses net, fs { try http.serve(":8080", handler: fn(req) => http.dispatch(req, routes: routes())) }
```
- The FIRST matching route wins; same path with another method → 405, no match → 404. Test handlers by calling them with an `http.Request{...}` literal.
- Responses: `http.text/json/html(status, body: x)`, `http.redirect("/items")` (303), `http.Response{status: 200, body: b, headers: {...}, cookies: [http.Cookie{name: "s", value: v}]}` (cookies default to HttpOnly, Secure, SameSite=Lax). Request cookies: `req.cookies.get("s")`.
- Bodies over `max_body` (serve argument, default 1 MiB) get 413.
- TLS: `try http.serve(":8443", handler: h, tls: http.Tls{cert: "cert.pem", key: "key.pem"})` serves HTTPS (TLS 1.2+). The pair is re-read from disk when it changes, so a renewal needs no restart; a broken replacement keeps the last good certificate. A wrong path is an `E_TLS` failure before the port opens. Terminating TLS at a reverse proxy is also fine.
- OpenAPI: `http.openapi(routes(), title: "Catalog API", version: "1.0.0")` returns an OpenAPI 3.1 document (JSON): one operation per route with path and query parameters and JSON Schemas for `body:`/`response:`. A route may add `summary`, `operation_id`, `tags`, `query: {"limit": "Int"}`, `body: Item` and `response: ItemPage` (a struct or enum type); wrap a collection in a named struct. Routes must map to distinct OpenAPI paths (parameter names included). Serve the string at `/openapi.json`.
- Middleware: `http.dispatch(req, routes: routes(), middleware: [http.request_id(), http.log_requests(), http.recover()])`. Built-ins: `request_id`, `log_requests`, `recover` (panic → 500), `timeout(ms)` (→ 503), `metrics` (counters by method+status, latency histogram, in-flight gauge); a custom one is `http.Middleware{name: "auth", apply: fn(next) => fn(req) { ... }}`. They run left-to-right (first is outermost) and may short-circuit without calling `next`; they also wrap 404/405.
- Observability: serve `http.metrics_text()` (Prometheus format) at `/metrics`; `/healthz` and `/readyz` are plain routes for liveness/readiness. `http.tracing("service", endpoint: "http://localhost:4318")` adds OTLP/HTTP spans (`uses net`).

## SQL (PostgreSQL)
`import "sql"`; `sql.open(dsn)` returns a `sql.Conn` handle and connects lazily on the first query. `sql.query(conn, text: sql"SELECT id FROM t WHERE id = $1", args: [id])` → `List[Map[Str, Any]]`; `sql.query_as(conn, text: sql"SELECT ...", schema: Row)` decodes each row into `Row` (E_SCHEMA on a mismatch); `sql.exec(conn, text: sql"UPDATE ...")` → affected rows `Int`. `?` placeholders are accepted and rewritten to `$1...`. A wrong DSN, a refused connection and a server error are all `E_SQL` failures. Everything needs `uses sql`.

## Tests
`test "name" { assert expr, "optional message" }` in any file; `try` is allowed inside tests. Run `pygo test --json file_or_dir`. Failed asserts report operand values.

## Modules
`import "json"` (stdlib: json fs os http html time log math re proc rand crypto jwt sql), `import "./lib/geo"` (local file geo.pg, used as `geo.area(...)`), `import "./x" as y`. All top-level names are public.

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

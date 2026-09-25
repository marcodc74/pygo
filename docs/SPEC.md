# Pygo Language Specification — v0.2

Pygo is a general-purpose language whose primary author and reader is an AI
model (LLM or agent). Human readability is not a design goal. The design
goals, in priority order, are:

1. **Generation reliability**: a model writing left-to-right, without
   backtracking, should produce valid code. The grammar has no ambiguity,
   only the most common syntactic forms seen in training data, and exactly
   one way to write each construct.
2. **Local verifiability**: everything needed to reason about a function is
   in its signature (types, fallibility, effects, contracts).
3. **Machine-actionable feedback**: every problem has a stable code, a
   position, a hint and, where possible, an applicable fix.
4. **Safe execution of generated code**: effects are capabilities, denied
   by default; runs can be bounded in steps and time.
5. **Determinism**: the same program and input give the same output.

The compact reference for model contexts is [GUIDE.md](GUIDE.md) (generated
by `pygo guide`). This document is the complete specification.

---

## 1. Lexical structure

- Source files are UTF-8 and have the extension `.pg`.
- **Comments**: `// text` until end of line. `/// text` is a doc comment
  attached to the next declaration, field or variant. Doc comments are kept
  by `pygo fmt`; plain comments are not.
- **Identifiers**: a letter or `_` followed by letters, digits or `_`.
- **Keywords** (reserved): `fn let var if else for in while break continue
  return match struct enum impl import as test assert fail try catch spawn
  defer uses requires ensures true false nil and or not`.
- **Integers**: `42`, `1_000_000`, `0xff`, `0b1010`, `0o17`. 64-bit signed.
- **Floats**: `3.14`, `1e9`, `2.5e-3` (a digit must follow the dot:
  `1..3` is a range, not a float).
- **Strings**:
  - `"text"` supports the escapes `\n \t \r \0 \\ \" \$ \u{1F600}` and
    interpolation `${expr}` or `${expr:spec}`. A regular string cannot span
    lines.
  - `"""..."""` is a multi-line string. A newline right after the opening
    quotes is dropped.
  - `r"..."` and `r"""..."""` are raw: no escapes and no interpolation.
  - `{` and `}` are ordinary characters in strings, so JSON needs no
    escaping.
- **Operators**: `+ - * / % == != < <= > >= = += -= *= /= %= -> => . .. ..=
  , : ? ! | ( ) [ ] { } ??`. The character `;` is an error (E0107).

### 1.1 Statement termination

A newline ends a statement when the last token of the line is an
identifier, a literal, `return`, `break`, `continue`, `true`, `false`,
`nil`, or one of `) ] } ? !`. There are two exceptions:

- Newlines inside `(...)` and `[...]` are ignored.
- A line whose first token is `.name` continues the previous line (method
  chains).

Consequently a line ending with a binary operator continues on the next
line, and `else` / `catch` must be on the same line as the preceding `}`.

## 2. Program structure

A file is a sequence of top-level declarations:

```
import "json"                  // standard module
import "./lib/geo"             // local module ./lib/geo.pg, bound as `geo`
import "./lib/geo" as g        // explicit binding name
let LIMIT = 100                // module constant (no `var` at top level: E0209)
struct ...   enum ...   impl ...   fn ...   test "..." { ... }
```

- Declarations may appear in any order.
- Every top-level name is public.
- Module constants are evaluated in order at load time and cannot use
  effects.
- Import cycles are errors (E0802).

`fn main()` or `fn main() -> !` is the entry point. It takes no parameters
(use `os.args()`).

## 3. Types

| Type | Values |
|---|---|
| `Int` | 64-bit signed; overflow is a panic (R0005), never wraps |
| `Float` | IEEE-754 double |
| `Str` | immutable Unicode text; indexing and length count runes |
| `Bool` | `true`, `false` |
| `List[T]` | growable, mutable, reference semantics |
| `Map[K, V]` | insertion-ordered; `K` ∈ {Int, Str, Bool, Float} |
| `T?` | `T` or `nil`; `nil` exists only in optional types |
| `fn(A, B) -> R`, `fn(A) -> !R` | functions and lambdas |
| `Chan[T]`, `Task[T]` | concurrency (§9) |
| `Range` | `a..b` (exclusive), `a..=b` (inclusive) of Int |
| `Error` | `struct Error { message: Str, code: Str, data: Any }` |
| `Any` | escape hatch for dynamic data (e.g. decoded JSON) |
| `Type[T]` | a type used as a value (e.g. `schema: User`) |

**Rules:**

- There are no implicit conversions. `Int` and `Float` never mix. Use
  `float(n)`, `int(x)` (truncates), `str(x)`, `try parse_int(s)` and
  `try parse_float(s)`.
- There is no truthiness. Conditions and the operands of
  `and`/`or`/`not` must be `Bool` (E0303).
- A value of type `T` is assignable to `T?` and to `Any`. `T?` is not
  assignable to `T` (E0301 or E0310).
- Local types are inferred from initializers. Parameters, results and
  fields are always annotated. Empty collections and `nil` need an
  annotation: `let xs: List[Int] = []`, `var u: User? = nil`.

### 3.1 Nil safety and narrowing

Reading a field, calling a method, indexing, iterating or doing arithmetic
on a `T?` value is an error (E0310). A value is narrowed to `T`:

- inside `if k != nil { ... }` and in the `else` branch of `if k == nil`;
- after `if k == nil { <return|fail|break|continue|panic> }`, for the rest
  of the block;
- in the right operand of `k != nil and ...` and `k == nil or ...`.

Here `k` is a variable or a field path such as `u.email`. The expression
`x ?? d` has type `T` when `d` is a `T`.

## 4. Declarations

### 4.1 Variables

`let x = e` is immutable and `var x = e` is mutable. Parameters are
immutable. **No shadowing** (E0206): a name must be unique within a
function (including nested blocks and lambdas) and may not reuse a
module-level or builtin name.

### 4.2 Functions

```
/// Doc.
fn name[T](a: Int, b: Str = "x", rest: ...Any) -> !T uses net, fs
    requires a > 0
    ensures result != nil
{
    ...
}
fn short(x: Int) -> Int => x * 2
```

- **Result**: `-> T` returns a value, `-> !T` may fail, and `-> !` may fail
  without a value. With no arrow the function has no result.
- **Returns**: a function with a result must return on every path (E0307).
  A path also ends with `fail`, `panic(...)`, `os.exit(...)` or
  `while true` without `break`.
- **Generics**: `[T, U]` declares type parameters. They are inferred at
  call sites from argument types.
- **Defaults**: default values are evaluated at each call. A default of
  `nil` makes the parameter implicitly optional.
- **Contracts**: `requires` is checked on entry and `ensures` on exit, with
  `result` bound to the return value. A violation is a panic (R0008) that
  reports the involved variables.

### 4.3 Calls: the named-argument rule

Only the **first** argument may be positional; every other argument must be
named (E0306). This prevents swapped arguments, the most common silent
error in generated code.

```
transfer(100, from: a, to: b)   // ok
transfer(100, a, b)             // E0306 (pygo fix inserts "from: " and "to: ")
```

Exceptions:

- variadic parameters (`print(a, b, c)`);
- calls through function values whose type has no parameter names
  (`f(x, y)` where `f: fn(Int, Int) -> Int`).

The receiver of a method is not an argument.

### 4.4 Lambdas

`fn(x) => x * 2` or `fn(a: Int, b: Int) -> Int { return a + b }`.

- Parameter types may be omitted when the expected function type provides
  them (for example `xs.map(fn(x) => ...)`).
- A lambda body that contains `try` or `fail` makes the lambda fallible.
- Lambdas capture variables by reference.

### 4.5 Structs, enums and methods

```
struct User { name: Str, age: Int = 0, email: Str? }
enum Shape { Circle(r: Float), Rect(w: Float, h: Float), Empty }
impl User {
    fn new(name: Str) -> User => User{name: name}   // static: User.new("a")
    fn greet(self) -> Str => "hi ${self.name}"       // method: u.greet()
}
```

- **Struct literal**: `User{name: "a"}`. Missing fields must have a default
  or an optional type (E0602). Unknown fields are errors (E0601).
- **Structs** are mutable objects with reference semantics. `let` only
  prevents rebinding.
- **Enum values** are immutable. They are built with
  `Shape.Circle(r: 1.0)` or `Shape.Circle(1.0)`, and `Shape.Empty` for a
  variant without fields. Their fields are read with `match`.

## 5. Statements

- Assignments: `x = e`, `x += e` (also `-= *= /= %=`), `a[i] = e`,
  `m[k] = e`, `obj.f = e`.
- Loops:
  - `for x in coll`, `for i, x in list`, `for k, v in map`;
  - `for x in map` iterates over the keys;
  - `for i in 0..n`, `for c in str`, `for v in chan`;
  - `while cond`, with `break` and `continue`.
- `return [e]`, `fail e`, `assert cond[, message]`, and `defer call(...)`
  (runs when the function exits, in LIFO order).
- `if`, `match` and blocks are expressions. Their value is the last
  expression statement of the chosen block.

## 6. Operators and precedence

From lowest to highest precedence:

| Level | Operators |
|---|---|
| 1 | `??` |
| 2 | `or` |
| 3 | `and` |
| 4 | `not` |
| 5 | `== != < <= > >= in` (non-associative) |
| 6 | `..` and `..=` |
| 7 | `+ -` |
| 8 | `* / %` |
| 9 | unary `-`, `try`, `spawn` |
| 10 | postfix: call `()`, index `[]`, field `.`, struct literal `T{}`, `catch` |

- Mixing `and` with `or`, or `??` with other binary operators, requires
  parentheses (E0116).
- Comparisons cannot be chained.
- `Int / Int` truncates toward zero, and `%` has the sign of the dividend.
- `+` concatenates `Str` and `List` values.
- `a[i..j]` slices lists and strings. Negative indexes are errors.

## 7. Errors

There are two kinds of error.

- **Failures** are expected errors. A fallible function (`-> !T`) raises
  one with `fail error("msg", code: "E_X", data: v)` or `fail "msg"`. At
  the call site a failure must be handled (E0401):
  - `try expr` propagates any failure in `expr` to the caller. It is only
    allowed in fallible functions and tests (E0402).
  - `expr catch e { block }` handles it. `e: Error`, and the block
    provides a replacement value or leaves with `return`/`fail`.

  A failure that escapes `main` gives exit code 1.
- **Panics** are bugs: index out of range, missing key with `m[k]`,
  division by zero, overflow, a failed `assert` or contract, or
  `panic(msg)`. They cannot be caught. The program stops with exit code 2
  and a JSON-serializable report containing the code (R00xx), the
  position, a hint, the values of the variables involved and the stack
  trace.

## 8. Effects (capabilities)

The effects are `fs`, `net`, `env`, `proc`, `clock`, `rand` and `python`
(calls into Python libraries, §14.1).

- **Declaration**: a function that performs an effect directly, or calls a
  function that does, must declare it: `uses net, fs` (E0501). Declared but
  unused effects produce a warning (W0503). Effects used in lambdas count
  toward the enclosing function.
- **Granting at run time**: `pygo run --allow net,fs` (or `--allow all`).
  - Before starting, the runner compares `main`'s `uses` clause with the
    grants and refuses to start if something is missing (exit code 4).
  - During execution every effectful builtin checks its grant again
    (R0010, exit code 4).
  - With no `--allow`, a program can only compute and print.
- **Free operations**: `print`, `eprint`, `log.*`, `time.sleep`, `read_line`
  and `os.args` need no grant.

## 9. Concurrency

- `spawn f(args)` runs a call on a new goroutine and returns a `Task[T]`.
  A failure is delivered by `try t.wait()` or `try wait_all(tasks)`, and a
  panic in a task is re-raised by `wait`.
- `chan(capacity)` creates a `Chan[T]` with the methods `send(v)`, `recv()`
  (returns `T?`, `nil` when closed and drained), `close()` and `len()`.
  `for v in ch` loops until the channel is closed.
- Lists, maps, structs and variable bindings are internally synchronized,
  so concurrent access is memory-safe. Read-modify-write sequences
  (`n += 1`) are not atomic; use channels for coordination.

## 10. Pattern matching

`match subject { pattern [| pattern] [if guard] => expr-or-block ... }`.

Patterns:

- `_` matches anything;
- `name` binds the value, or matches a field-less variant with that name;
- literals: `1`, `-2.5`, `"s"`, `true`, `nil`;
- ranges: `lo..hi` and `lo..=hi`;
- variants: `Enum.Variant(p1, p2)` or `Variant(p1, p2)`.

Match is exhaustive (E0701). Every variant of an enum, or both Bool values,
must be covered by unguarded arms, or there must be a final catch-all
(`_` or a binding). Arms after a catch-all are unreachable (W0702).

## 11. Tests

`test "name" { ... }` blocks live next to the code.

- Inside tests, `try` propagates a failure as a test failure.
- `assert a == b` reports both operand values on failure.
- `pygo test --json path` returns structured results.

## 12. Determinism

- Map iteration follows insertion order.
- `rand` is seeded with 0 unless `--seed N` is given.
- Wall-clock time is only reachable through the `clock` effect.
- There is no uninitialized memory, integer wraparound or implicit
  conversion.
- The order of goroutine scheduling is the only source of
  nondeterminism; results collected with `wait_all` keep the order of
  the tasks.

## 13. Toolchain contract (for agents)

| Command | Output |
|---|---|
| `check --json` | `{ok, diagnostics[{code, severity, message, file, line, col, hint, fix?}]}` |
| `fix [--all]` | applies `fix` edits (`safe` ones by default) and re-checks |
| `run [--json]` | program output on stdout; with `--json` a final line on stderr `{exit_code, status, panic?, error?, steps}` |
| `run/test --engine vm\|tree` | selects the execution engine (§16, default `vm`); results are identical |
| `compile [-o app.pgc]` | checks and compiles to a `.pgc` bytecode file; `run`/`test` accept it (§16) |
| `disasm [--json] [--fn name]` | the bytecode of a `.pg` or `.pgc` file |
| `test --json` | `{ok, passed, failed, tests[{name, file, line, passed, ms, failure?}]}` |
| `explain CODE` | description with wrong/right examples |
| `guide` | compact reference + stdlib signatures (~2.6k tokens) |
| `describe`, `outline`, `ast` | API, symbols with content hashes, AST — all JSON |
| `edit --replace KEY` | replaces a whole declaration (`fn:x`, `struct:X`, `impl:X`, `test:name`, `let:X`, `import:path`, `extern:name`); `--expect-hash` guards against stale edits |
| `extern python MODULE [NAME...]` | draft `extern` block generated from the real Python signatures and type hints |
| `fmt` | the canonical text form |
| `build` | self-contained executable (runtime + bytecode); `--runtime` selects a binary for another OS/arch, `--source` embeds the source instead |

Exit codes: 0 ok · 1 unhandled failure · 2 panic · 3 compile errors
(including an invalid `.pgc` file, `E0910`) · 4 capability denied.

Diagnostic code ranges:

| Range | Area |
|---|---|
| E01xx | syntax |
| E02xx | names |
| E03xx | types |
| E04xx | errors |
| E05xx | effects |
| E06xx | structs and enums |
| E07xx | match |
| E08xx | modules |
| E09xx | miscellaneous |
| Wxxxx | warnings |
| Rxxxx | runtime panics |

`pygo explain` lists them all.

How to connect the toolchain to LLM chats, coding CLIs (Claude Code, Codex,
Gemini CLI, opencode, Copilot, Cursor, Qwen Code, Aider) and APIs (Claude,
OpenAI, Gemini, local models): `docs/LLM.md` and `integrations/`.

## 14. Standard library

The signatures live in `internal/sig/std/*.pg`. They are written in Pygo
and embedded in the binary, and they are the single source of truth for
the checker, the runtime, `describe` and `guide`.

| Module | Contents |
|---|---|
| (core) | `print`, `len`, `str`, `int`, `float`, `parse_int`, `parse_float`, `error`, `panic`, `chan`, `wait_all`, `read_line`, ...; methods of Str, List, Map, Range, Chan, Task |
| `json` | `encode`, `decode`, `decode_as(text, schema: T)` (typed validation) |
| `fs` | `read`, `write`, `append`, `exists`, `list`, `remove`, `mkdir` (`uses fs`) |
| `os` | `args`, `env` (`uses env`), `exit`, `platform`, `cwd` |
| `http` | `get`, `post`, `request` (`uses net`); `serve` with graceful shutdown on SIGTERM; `text`, `json` helpers |
| `time` | `now`, `now_ms`, `iso` (`uses clock`), `sleep` |
| `log` | `debug`, `info`, `warn`, `error`: JSON lines on stderr |
| `math` | `pi`, `e`, `sqrt`, `pow`, `abs`, `floor`, `ceil`, `round`, ... |
| `re` | RE2 regular expressions |
| `proc` | `run(cmd: List[Str])` without a shell (`uses proc`) |
| `rand` | seeded, deterministic (`uses rand`) |

### 14.1 Python libraries (`extern python`)

```
extern python "module.path" [as name] {
    fn f(p: T, q: U = default) -> !R
    type Handle {
        fn method(self, ...) -> !R
    }
}
```

- **Binding.** The block binds `name`, which defaults to the last segment of
  the module path. Functions are called as `name.f(...)`, and extern types
  are referenced as `name.Handle`.
- **Fallibility and effect.** Every function and method must be fallible
  (E0610) and implicitly uses the `python` effect. Callers must declare
  `uses python`, and the runner must grant `--allow python`.
- **Security.** Granting `python` effectively grants everything: Python code
  can reach files, the network and processes. It is also outside
  `--max-steps` and determinism, although `--timeout` still stops it by
  terminating the worker.
- **Extern types** are opaque handles to Python objects that stay in
  Python. They cannot be built with literals (E0611), and their methods
  take `self` (E0612). Released handles are freed in Python.
- **Execution.** Python runs in a separate worker process
  (`python -u -c <bridge>`), started on first use. The interpreter is taken
  from `--python PATH`, then `PYGO_PYTHON`, then `python3`/`python`. Calls
  are serialized, and output printed by Python goes to stderr.
- **Arguments.** They are sent with their names. The bridge binds them to
  the real Python signature: positional-only parameters by position, all
  others by keyword. A name unknown to Python is an error that says so. A
  parameter declared `p: T? = nil` and left nil is omitted, so the Python
  default applies. Without a signature (some C functions), the leading run
  of consecutive arguments is positional and the rest are keywords.
- **Values.**
  - Pygo to Python: Int, Float (always a float), Str, Bool, nil, List and
    Map become the matching Python values; structs become dicts; handles
    pass through.
  - Python to Pygo: results convert to JSON-like data (tuples and sets
    become lists; numpy arrays and scalars become lists and numbers). An
    object that cannot be converted, or one whose declared result is an
    extern type, stays in Python and returns as a handle.
  - The result is validated against the declared type: a mismatch is the
    failure `E_PYTHON_TYPE`, with a path such as `result[2].name`.
- **Errors** are failures:
  - `E_PYTHON`: `message` is `"Type: text"`, and `data` holds `type` and
    `traceback`;
  - `E_PYTHON_IMPORT`: the message suggests `pip install`;
  - `E_PYTHON_TYPE`;
  - `E_PYTHON_UNAVAILABLE`: Python was not found, or the worker exited.

## 15. Deployment

- `make dist` builds static binaries for Linux, macOS and Windows on amd64
  and arm64.
- `deploy/Dockerfile` has two targets:
  - `app` checks and tests the program, bundles it with `pygo build`, and
    ships a 16 MB distroless non-root image;
  - `toolchain` ships the CLI, for CI or sandboxes.
- `deploy/k8s/deployment.yaml` defines a Deployment and a Service, with
  probes on `/healthz`, a read-only root filesystem and all Linux
  capabilities dropped.
- `deploy/Dockerfile` target `app-python` adds a Python runtime (and the
  packages of a `requirements.txt` next to the program) for programs with
  `extern python` blocks.
- `deploy/k8s/job-sandbox.yaml` runs untrusted, AI-generated code with no
  Pygo capabilities, a step budget, a timeout and a deny-all
  NetworkPolicy.

## 16. Execution engines

Pygo has two engines with identical observable behavior:

- `vm` (the default since v0.2): Pygo's bytecode virtual machine;
- `tree`: walks the syntax tree, selected with `--engine tree` on
  `run` and `test`.

Programs can be compiled ahead of time to a `.pgc` bytecode file
(`pygo compile`). `pygo run` and `pygo test` accept `.pgc` files, and
`pygo disasm` shows the bytecode. `docs/BYTECODE.md` describes the
machine, the instruction set and the file format.

The VM compiles each function into a flat instruction list for a stack
machine:
- local variables live in numbered slots;
- variables captured by closures live in shared cells (safe across
  `spawn`);
- `try`/`catch` become handler instructions.

It reuses the runtime of the interpreter (values, operators, stdlib,
Python bridge, calls, contracts, `defer`, tasks). The two engines can
call each other.

The guarantees:
- same output, same result, same panic (code, message, position, values
  and trace), same failure;
- same number of steps, so `--max-steps` stops both at the same statement.

The test suite runs every program three ways and compares all of this:
on the interpreter, on the VM, and on the VM from a `.pgc` round trip. A
function the compiler cannot handle stays on the interpreter; the tests
require that no such function exists.

Measured on the programs in `bench/` (`go test -bench . ./internal/interp/`):

| Program | tree | vm | speed-up |
|---|---|---|---|
| `fib.pg` (recursive calls) | 462 ms | 135 ms | 3.4x |
| `loops.pg` (loops, arithmetic) | 765 ms | 219 ms | 3.5x |
| `sort.pg` (merge sort, list ops) | 538 ms | 326 ms | 1.7x |
| `json.pg` (encode/decode, stdlib-bound) | 45 ms | 38 ms | 1.2x |

The VM helps most with code written in Pygo: loops, arithmetic and
calls. It helps least with work done by the stdlib, which both engines
share.

## 17. Roadmap

- a WebAssembly backend (from the bytecode);
- traits/interfaces;
- `select` on multiple channels;
- effect polymorphism for higher-order functions;
- an LSP server;
- a package manager;
- record/replay of effects for deterministic debugging;
- preserving plain comments in `fmt`.

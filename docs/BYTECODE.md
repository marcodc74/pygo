# Pygo bytecode and virtual machine

Pygo programs run on Pygo's own virtual machine (the default engine since
v0.2). This document describes:
- the machine;
- its instruction set;
- the `.pgc` file format;
- the commands that work with them.

## 1. Pipeline

```
file.pg ── parse ── check ── compile ──► bytecode ──► VM
                                  │
                                  └─ pygo compile ──► app.pgc ── pygo run app.pgc
```

| Command | What it does |
|---|---|
| `pygo run file.pg` | checks, compiles in memory and runs on the VM |
| `pygo compile -o app.pgc file.pg` | checks (exit 3 on errors), compiles and writes the file (default name: the source with `.pgc`) |
| `pygo run app.pgc` / `pygo test app.pgc` | run directly from bytecode (no parsing, checking or compiling) |
| `pygo disasm [--json] [--fn name] file.pg\|app.pgc` | shows the bytecode |
| `pygo build -o app file.pg` | a self-contained executable with the bytecode embedded (`--source` embeds the source instead, for runtimes older than v0.2) |
| `--engine tree` | on `run` and `test`, uses the tree-walking interpreter instead of the VM |

## 2. The machine

Each function is compiled into a `Proto`:
- a flat list of instructions `{op, a, b, c}` (three `int32` operands),
  each with the source position (line, column) it comes from;
- constants: `Int`, `Float`, `Str`;
- auxiliary tables: the syntax nodes an instruction needs for its checks
  and messages, pattern and assert tables;
- the number of **slots** (local variables, resolved at compile time) and
  the maximum depth of the operand stack;
- the **upvalues** (variables captured from enclosing functions) and the
  nested function literals.

Execution uses one operand stack and one slot array per call.

- **Variables captured by closures** live in cells shared with the closure,
  protected by a mutex. This makes them safe with `spawn`. Each loop
  iteration and each `let` creates a fresh cell, so closures created in a
  loop capture the value of their own iteration.
- **Module-level names** (functions, types, imports, constants) are looked
  up once and then cached. After a module is loaded its members never
  change, so the cache cannot go stale.
- **`try` and `catch`.** `try` raises the "handled" depth, so failures
  propagate instead of becoming a panic. `catch` also installs a handler:
  on a failure the VM restores the stack height and the handled depth
  recorded when the handler was installed, then jumps to the handler with
  the error value.
- **Breaking out of blocks.** `break` and `continue` first close the
  `try`/`catch` regions they leave and drop the stack values of the
  expressions they interrupt. `break` can appear inside a call argument or
  a `match` arm.
- **Shared runtime.** The VM reuses the runtime of the interpreter: values,
  operators, the stdlib, the Python bridge, argument binding, contracts,
  `defer` and tasks. The two engines can call each other; a function the
  compiler does not support runs on the interpreter.

### Guarantees

Both engines produce, for every program:
- the same output;
- the same result and exit code;
- the same panic: code, message, position, values and trace;
- the same failure;
- the **same number of steps**: `STEP` is emitted exactly where the
  interpreter counts a step (each statement, each loop iteration), so
  `--max-steps` stops both at the same statement.

The test suite checks this on every test program and example. Each one
runs three ways: on the interpreter, on the VM, and on the VM from a
`.pgc` round trip (compile, encode, decode). All three must agree.

### Performance

These are `go test -bench . ./internal/interp/` on the programs in
`bench/`:

| Program | tree | vm | speed-up |
|---|---|---|---|
| `fib.pg` (recursive calls) | 462 ms | 135 ms | 3.4x |
| `loops.pg` (loops, arithmetic) | 765 ms | 219 ms | 3.5x |
| `sort.pg` (merge sort, list operations) | 538 ms | 326 ms | 1.7x |
| `json.pg` (stdlib-bound) | 45 ms | 38 ms | 1.2x |

Startup from `.pgc` skips parsing and checking. For a generated program of
2,000 functions (28,000 lines), `pygo run app.pgc` starts in about 67 ms,
against about 140 ms for `pygo run app.pg`. For small programs the
difference is negligible, because process start dominates.

## 3. Instruction set

Operands are written `a b c`. "aux" is an index into the auxiliary table;
"slot" is a local variable. The stack effect is given for the case where
execution continues with the next instruction.

### Values and variables

| Op | Operands | Stack | Meaning |
|---|---|---|---|
| `NOP` | | | nothing |
| `CONST` | a = constant | +1 | push a constant |
| `NIL` `TRUE` `FALSE` | | +1 | push a literal |
| `POP` | | −1 | drop the top value |
| `POPN` | a = count | −a | drop `a` values (used by `break`/`continue`) |
| `LOAD_LOCAL` / `STORE_LOCAL` | a = slot | +1 / −1 | read / write a local |
| `LOAD_CELL` / `STORE_CELL` | a = slot | +1 / −1 | read / write a local held in a shared cell |
| `NEW_CELL` | a = slot | −1 | put the top value in a fresh cell in the slot |
| `LOAD_UPVAL` / `STORE_UPVAL` | a = upvalue | +1 / −1 | read / write a captured variable |
| `LOAD_GLOBAL` | a = name constant, b = cache entry | +1 | module-level or builtin name; panics `R0014` if undefined |
| `LOAD_GLOBAL_OPT` | same | +1 | same, but pushes a "missing" marker instead of panicking (assert introspection) |
| `STORE_GLOBAL` | b = aux identifier | −1 | assignment to a module-level name (panics: module names are immutable) |
| `PANIC_IMMUTABLE` | a = aux identifier | −1 | assignment to an immutable local: raise the panic |

### Operators and control flow

| Op | Operands | Stack | Meaning |
|---|---|---|---|
| `BINARY` | a = aux operator | −1 | `x op y`, with fast paths for Int and Float; overflow and division by zero panic as in the interpreter |
| `UNARY` | a = aux | 0 | `-x`, `not x` |
| `LOGIC` | a = aux, b = 0 left / 1 right | 0 | check that an operand of `and`/`or` is Bool |
| `JUMP` | a = target | 0 | |
| `JUMP_IF_FALSE` / `JUMP_IF_TRUE` | a = target | −1 | pop a Bool, jump on false / true |
| `JUMP_IF_NOT_NIL` | a = target | −1 / 0 | `??`: keep the value and jump if not nil, else pop it |
| `IF_COND` / `WHILE_COND` | a = aux | 0 | check that a condition is Bool (no truthiness) |
| `STEP` | | 0 | count a step (budget, timeout) and record the position for traces |
| `RETURN` | | −1 | return the top value |
| `BREAK_OUTSIDE` | a = 1 for continue | | `break`/`continue` outside a loop in this function |

### Data

| Op | Operands | Stack | Meaning |
|---|---|---|---|
| `MAKE_LIST` | a = count | 1−a | list from the top `a` values |
| `CHECK_KEY` | | 0 | check a map key type (before its value is evaluated) |
| `MAKE_MAP` | a = entries | 1−2a | map from key/value pairs |
| `MAKE_RANGE` | a = aux | −1 | `lo..hi` / `lo..=hi` |
| `FORMAT_PART` | a = aux string, b = part | 0 | format an interpolated value (`${x:.2f}`) |
| `STR_BUILD` | a = aux string, b = parts | 1−b | join literal text and formatted parts |
| `STRUCT_TYPE` | a = aux literal, b = slot | −1 | check the struct type and keep it in a slot |
| `STRUCT_PRE` / `STRUCT_CHECK` | a = aux, b = field, c = slot | 0 | check a field name / its value |
| `MAKE_STRUCT` | a = aux, b = fields, c = slot | 1−b | build the struct (defaults, missing fields) |
| `SELECTOR` | a = aux | 0 | `x.name`: field, method, module member, variant |
| `INDEX` | a = aux | −1 | `x[i]`, `m[k]`, slices |
| `ASSIGN_FIELD` | a = aux | −2 | `obj.f (op)= value` |
| `ASSIGN_INDEX` | a = aux | −3 | `obj[k] (op)= value` |
| `ASSIGN_COMPUTE` | a = aux | −1 | `old op value` for compound assignment |
| `LET_CHECK` | a = aux | 0 | check the annotated type of `let x: T = ...` |

### Calls, closures, errors

| Op | Operands | Stack | Meaning |
|---|---|---|---|
| `CALL` | a = positional, b = aux call, c = named | −(a+c) | call; named arguments follow the positional ones |
| `SPAWN` | same | −(a+c) | start a task, push the `Task` |
| `DEFER` | same | −(a+c+1) | register a deferred call |
| `MAKE_CLOSURE` | a = nested function | +1 | build a closure, capturing cells |
| `FAIL` | a = aux | −1 | raise a failure (`fail`) |
| `TRY_ENTER` / `TRY_EXIT` | | 0 | enter / leave a handled region |
| `CATCH_PUSH` | a = handler | 0 | install a `catch` handler |
| `CATCH_POP` | | 0 | remove it (the protected expression succeeded) |

### Loops, match, assert

| Op | Operands | Stack | Meaning |
|---|---|---|---|
| `ITER_INIT` | a = aux | 0 | replace a List/Map/Str/Range/Chan with an iterator (lists and maps are snapshotted) |
| `ITER_NEXT` | a = exit, b = 1 with key | +1+b | push the next element (and key), or jump to `a` when exhausted |
| `MATCH_PAT` | a = aux pattern, b = subject slot | +1 | match a pattern, bind its names, push Bool |
| `GUARD` | a = aux arm | 0 | check that a guard is Bool |
| `NO_MATCH` | a = aux, b = subject slot | | panic `R0016` |
| `ASSERT_BIN` | a = aux | −1 | compare the operands of `assert x op y`, recording their values |
| `ASSERT_COND` | a = aux | 0 | check a non-comparison assert condition is Bool |
| `ASSERT_COLLECT` | a = aux, b = names | −b | record the values of the condition's variables |
| `ASSERT_RAISE` | a = aux, b = 1 with message | −b | raise the assert panic with the values |

Example (`pygo disasm --fn main examples/hello.pg`, abridged):

```
fn main  (examples/hello.pg:2)
    ; slots: 0=name 1=nums 2=doubled 3=ages 4=age 5=who  (* = shared cell)  max stack: 4
     0     3:5    STEP
     1     3:5    CONST            0           ; "Pygo"
     2     3:5    STORE_LOCAL      0           ; name
     ...
    36     8:21   LOAD_LOCAL       3           ; ages
    37     8:5    ITER_INIT        6
    38     8:5    ITER_NEXT        52 1        ; exit -> 52
```

`--json` gives the same data for tools:
`{file, functions[{name, file, line, slots, boxed, upvalues, max_stack, code[{pc, line, col, op, a, b, c, note}], nested}]}`.

## 4. The `.pgc` file

```
"PYGC"                     magic
uvarint  format version    (1)
uvarint  opcode count      (the VM rejects a different instruction set)
string   pygo version      (informational)
string   source hash       (sha256 of the sources, hex)
graph    the program
[32]byte sha256 of everything before
```

**Contents.** The program is the checked syntax tree of every module
(declarations, types, signatures, defaults, contracts), their imports, and
the bytecode of every function, method and test. The runtime still needs
the declarations: it builds types from them, binds arguments, evaluates
defaults and contracts, and prints expressions in error messages.

**Encoding.**
- **Object graph.** Each pointer is written once; later occurrences refer
  back to it. Syntax nodes shared by the declarations and the bytecode
  stay shared.
- **Strings table.** Each distinct string is written once.
- **Compact tables.** Instructions take an opcode byte plus three zigzag
  varints. Source positions are stored as line deltas.
- **Deterministic.** The same source always gives the same bytes. The
  tests check this.

**Loading.** Loading checks:
1. the magic, the format version and the opcode count;
2. the checksum;
3. the structure of every function: sizes, opcodes, slot, constant, table,
   jump and closure references.

A corrupted, truncated, foreign or incompatible file is rejected with
diagnostic `E0910` (exit 3), and the fix is to recompile. The decoder is
fuzzed in the tests with thousands of corrupted files and never crashes.

**Trust.** The checks catch accidental corruption and incompatible
versions. Beyond that, the VM runs on Go's memory-safe runtime: a file
crafted to pass the checks can at worst stop the program with an
internal-error panic (`R0099`), not corrupt memory. Capabilities are
still granted only by the runner (`--allow`), whatever the file declares.

**Executables.** `pygo build` appends the file to a copy of the pygo
binary, preceded by the capabilities granted at build time.

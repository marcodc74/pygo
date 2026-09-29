# Pygo AI-Native Roadmap

This is the working plan for evolving Pygo from "a language an LLM can use" into
"a language an LLM can be trusted to run unattended". It is organized around
three levers that map directly to the failure modes of LLM-authored code:

1. **Generation reliability** — reduce the probability of a wrong generation.
2. **Repair cost** — reduce the tokens/turns needed to detect and fix an error.
3. **Execution trust** — make generated code safe to run without supervision.

Everything below either (a) makes a guarantee static instead of runtime, (b)
makes feedback machine-readable and actionable, or (c) makes effects and cost
bounded and replayable. Proposals that do none of these are out of scope.

Status legend: `done` · `planned` · `research`.

Touchpoints are given as paths in this repository so each item is actionable.

---

## 0. Shipped baseline (hardening round)

| ID | Item | Status |
|----|------|--------|
| H1 | Call-depth limit `R0018` in `callFunction` (tree/vm/pgc agree; no more Go stack-overflow fatal) | done |
| H2 | Blocking operations respect `--timeout` and never trigger the Go deadlock fatal (watchdog + `stop` channel) | done |
| H3 | `-9223372036854775808` literal accepted, positive magnitude still rejected | done |
| H4 | `--json` uniform for `describe`/`outline`; `fix --dry-run --json` keeps stdout machine-readable | done |
| H5 | Differential tree/vm/pgc corpus harness (adversarial testing) | done (manual) |

Remaining gap: H1/H2 are runtime panics; TRU-3 turns them into static guarantees.

---

## Milestones

- **M1 — Generation (weeks):** GEN-1, GEN-2, GEN-3.
- **M2 — Repair loop:** REP-1, REP-2, REP-3, REP-4.
- **M3 — Trust:** TRU-1..TRU-5.
- **M4 — AI in the loop:** AI-1, AI-2.
- **M5 — Scale & interop:** RT-1..RT-7, INT-1..INT-3, AGT-1..AGT-5, SYN-1..SYN-3.
- **M6 — Research:** AI-3, RT-2, RT-4, SYN-2.
- **M7 — Production services:** SRV-1..SRV-8 (SRV-1..SRV-5 shipped; SRV-6 is next).

Order is ROI-driven: M1/M2/M3 unlock safe unattended loops; M7 makes the result
deployable; M5 deepens the moat.

---

## M1 — Generation reliability

### GEN-1 · `pygo grammar` — constrained decoding
- **Problem:** an LLM with free decoding emits invalid syntax; every syntax error
  costs turns and tokens.
- **Design:** emit the language grammar in machine formats (`gbnf` for
  llama.cpp/vLLM, `json-schema`, `ebnf`) generated from the lexer/parser so it
  cannot drift. `pygo grammar --format gbnf --include stdlib`.
- **Touchpoints:** `internal/lexer`, `internal/parser`, `cmd/pygo`.
- **Acceptance:** `pygo grammar --format gbnf` output loads in llama.cpp and
  makes a sample completion syntactically valid; CI checks grammar/parser parity
  on a corpus.
- **Metric:** syntax-error rate per completion (~0), turns-to-green.
- **Effort:** S. **Deps:** none. **Risk:** grammars for format specs/interpolation.

### GEN-2 · Context compiler `pygo guide --task ... --budget N`
- **Problem:** the model wastes context on irrelevant stdlib and loses the middle.
- **Design:** rank signatures/sections by the task description and a symbol
  retrieval over `internal/sig/std/*.pg` + `guide.md`; emit a minimal digest with
  token accounting.
- **Touchpoints:** `internal/guide`, `internal/sig`, `cmd/pygo`.
- **Acceptance:** for a fixed task set, the digest is ≤ budget and still contains
  every signature the solution needs (checked by compiling solutions).
- **Metric:** tokens per successful task; missing-symbol rate.
- **Effort:** M. **Deps:** none.

### GEN-3 · `pygo plan` typed skeletons with holes
- **Problem:** going from a goal to a first correct decomposition is the costliest
  model step.
- **Design:** declare `todo`/`hole` placeholders that the checker accepts and
  tracks; `pygo plan` emits a typed skeleton; `pygo fill` proposes bodies.
- **Touchpoints:** `internal/parser`, `internal/check`, `internal/ast`.
- **Acceptance:** a skeleton with holes type-checks, holes are listed in
  `--json`, and filling them in any order keeps diagnostics bounded.
- **Effort:** M. **Deps:** none.

---

## M2 — Repair loop

### REP-1 · Multi-fix and confidence in `check --json`
- **Problem:** one diagnostic = one manual edit; some fixes interact.
- **Design:** group diagnostics into a minimal patch set; add `confidence` and
  `alternatives`; `pygo fix` applies the highest-confidence non-conflicting set.
- **Touchpoints:** `internal/check`, `internal/diag`, `cmd/pygo/tools.go`.
- **Acceptance:** applying a patch set reduces the error count and never
  introduces a new `E`; idempotent after one pass.
- **Effort:** M. **Deps:** none.

### REP-2 · Counterexample in `explain`/diagnostics
- **Problem:** codes are clear, but the model still needs a minimal repro.
- **Design:** each diagnostic optionally carries `example` (wrong → fixed) and a
  minimized program; `pygo explain --json` already exists, extend it.
- **Touchpoints:** `internal/diag/explain.go`.
- **Acceptance:** every `E/W` code has wrong/right examples; a test enforces it.
- **Effort:** S. **Deps:** none.

### REP-3 · Semantic patch / AST-first editing
- **Problem:** line diffs drift and break on regeneration.
- **Design:** stable symbol IDs (extend `outline` hashes), JSON patch over the
  AST, `pygo patch --check` (apply-or-fail atomically), `--expect-hash` already
  exists for `edit`.
- **Touchpoints:** `internal/ast/json.go`, `cmd/pygo/tools.go`.
- **Acceptance:** a patch applies or fails cleanly; round-trip
  source→AST→patch→AST is stable; `fmt` output unchanged.
- **Effort:** M. **Deps:** none.

### REP-4 · Verified repair search
- **Problem:** some errors need a search, not a single template.
- **Design:** beam over fix templates; each candidate is scored by
  `check` + `test`; only strictly-better candidates are surfaced.
- **Touchpoints:** `internal/check`, `internal/interp` (tests), `cmd/pygo`.
- **Acceptance:** on a seeded bug corpus, ≥X% fixed automatically with no test
  regression and a bounded step budget.
- **Effort:** L. **Deps:** REP-1, SYN-1.

---

## M3 — Execution trust

### TRU-1 · Proof-carrying `.pgc`
- **Problem:** running generated bytecode still trusts the compiler blindly.
- **Design:** embed a manifest in `.pgc`: capability set, declared effects,
  budgets, and a hash of the checker verdict; a verifier refuses to run a `.pgc`
  whose manifest is missing or inconsistent.
- **Touchpoints:** `internal/interp/pgc.go`, `cmd/pygo/bytecode.go`, `deploy/`.
- **Acceptance:** tampering with bytecode/caps is detected; the K8s sandbox
  verifies before executing.
- **Effort:** M. **Deps:** none.

### TRU-2 · Budget/resource types
- **Problem:** step/time/memory/token limits are external flags today.
- **Design:** make budgets composable values (`Budget{steps, wall, net_bytes,
  tokens}`) declared on `main` and checked so callees cannot exceed the parent's
  remaining budget.
- **Touchpoints:** `internal/check`, `internal/interp`, `internal/sig`.
- **Acceptance:** a program exceeding a declared budget is rejected statically
  where provable, and deterministically at runtime otherwise.
- **Effort:** L. **Deps:** TRU-3.

### TRU-3 · No-crash static analysis (deadlock, depth, unbounded blocking)
- **Problem:** H1/H2 are runtime panics; we want them proven.
- **Design:** analyze call graphs and blocking primitives: flag statically
  reachable unbounded recursion and channel/task waits with no possible sender;
  suggest loops, budgets, or `--timeout`.
- **Touchpoints:** `internal/check`.
- **Acceptance:** the F1/F2 reproducers are caught at `check` time with a fix
  hint; false-positive budget kept in tests.
- **Effort:** L. **Deps:** none.

### TRU-4 · Taint / provenance types
- **Problem:** untrusted input (`env/net/fs/llm`) can reach `Str` sinks.
- **Design:** generalize the `Html` guarantee: `@untrusted` flows from
  effect sources; the checker requires `declassify` before a `Str` reaches
  command/path/HTML/shell sinks. Capabilities already gate the sources.
- **Touchpoints:** `internal/check`, `internal/sig/sig.go`, `internal/safehtml`.
- **Acceptance:** an injection reproducer fails at `check`; declassify is
  explicit and auditable in `--json`.
- **Effort:** L. **Deps:** TRU-2 (budgets are a natural sibling).

### TRU-5 · `pygo fuzz` differential oracle in CI
- **Problem:** engine divergence silently breaks the trust model.
- **Design:** productize the manual collaudo: generate programs from a grammar
  and from mutations, run `tree`/`vm`/`pgc`, compare output/status/trace/steps,
  shrink the first divergence, and open a test case.
- **Touchpoints:** `internal/interp`, `cmd/pygo`, `.github/workflows/ci.yml`.
- **Acceptance:** CI fails on any divergence; the F1/F2 cases are permanent
  fixtures; runtime bounded.
- **Effort:** M. **Deps:** none.

---

## M4 — AI in the loop

### AI-1 · `extern llm` + typed `prompt"..."` literals
- **Problem:** calling a model from code is untyped, uncached, unbudgeted.
- **Design:** a new capability `llm`; `prompt"..."` with a declared output
  schema checked like any type; results validated, cached by content hash, and
  replayable; `uses llm` composes with TRU-2 budgets (tokens).
- **Touchpoints:** `internal/sig/sig.go` (capability set), new `internal/llm`,
  `internal/check`, `internal/interp`.
- **Acceptance:** a typed prompt call round-trips with the cache; a schema
  mismatch is a handled failure; token budget enforced.
- **Effort:** L. **Deps:** RT-3 (replay), TRU-2.

### AI-2 · `embed`/vector + deterministic store
- **Problem:** semantic retrieval inside programs is ad-hoc.
- **Design:** a `Vector` type and a store with seedable, replayable similarity
  ops; integrate with `math`/`rand` determinism.
- **Touchpoints:** `internal/sig/std`, `internal/interp`.
- **Acceptance:** same input+seed → same ranking across engines.
- **Effort:** M. **Deps:** AI-1.

---

## M5 — Scale & interop

### Runtime
- **RT-1 Deterministic scheduler:** virtual time and a fixed channel order so
  concurrent runs are reproducible across engines. (L)
- **RT-2 Checkpoint/fork/time-travel VM:** snapshot state, branch, compare. (L, research)
- **RT-3 Record/replay effects:** capture `clock/env/fs/net/rand/llm` and replay
  (already in the public roadmap). (M)
- **RT-4 Hot-patch `.pgc`:** adopt a replacement function only if `check`+`test`
  pass (self-healing). (L, research)
- **RT-5 Content-addressable cache + bytecode delta:** hash-keyed build cache,
  binary deltas for cheap shipping. (M)
- **RT-6 `select` + structured concurrency + deadlines:** channels with
  cancellation/deadlines, aligned with TRU-2. (M)
- **RT-7 Capability attenuation/delegation:** pass a reduced capability set to
  `spawn`, including across processes. (M)

### Interop
- **INT-1 `extern openapi|jsonschema|sql|wasm`:** generate typed bindings like
  `extern python` does today. (L)
- **INT-2 Package manager + provenance + capability manifest:** reproducible
  installs, per-package capabilities. (L)
- **INT-3 WASM backend:** the public roadmap target; enables edge/browser agents. (L)

### Agent tooling
- **AGT-1 MCP server:** expose every subcommand (already ~MCP-shaped JSON) as
  typed tools; formalize `integrations/`. (S)
- **AGT-2 `pygo ask "goal"`:** one command running plan→edit→check→test→fix. (M)
- **AGT-3 `pygo repo`:** semantic graph (functions, effects, capabilities, call
  graph) for context and impact analysis. (M)
- **AGT-4 `pygo check --watch --json`:** streaming deltas for write→fix loops. (S)
- **AGT-5 Versioned JSON protocol:** publish a schema for all `--json` outputs;
  CI validates it. (S)

### Synthesis
- **SYN-1 Property-based `property "..."` + auto-fuzz/shrink:** (M)
- **SYN-2 `pygo dream`:** synthesize function bodies from `describe`+`test`. (L, research)
- **SYN-3 Contracts from examples:** infer `requires/ensures` from `test` (and
  generate tests from contracts). (M, research)

### Research
- **AI-3 Confidence/probability type** with checked propagation. (research)

---

## M7 — Production services

Goal: a generated service can face real traffic with the boring guarantees
(auth, observability, persistence, integration) without falling back to
untyped Python. Breadth is delegated through typed interop; the language keeps
the safety guarantees.

SRV-1, SRV-2, SRV-3, SRV-4 and SRV-5 are **shipped**; SRV-6 is next and the
rest are queued. Definition
of done for an open item: signatures in `internal/sig/std/`, a Go
implementation, entries in `pygo guide` / `pygo explain` where relevant, tests
on all three engines, and a worked example under `examples/`.

### SRV-1 · Composable middleware  `done`
- **Problem:** every service re-implements logging, recovery, request ids and
  timeouts; there is no canonical pipeline, so generated handlers diverge and
  the missing concerns are invisible in `check`.
- **Design:** a `http.Middleware` value and a `middleware:` argument on
  `http.dispatch`. A middleware wraps `fn(Request) -> Response` and may
  short-circuit. Built-ins: `http.recover()`, `http.log_requests()`,
  `http.request_id()`, `http.timeout(ms)`. Order is left-to-right; middleware
  effects compose and are checked like any callback (`uses`).
- **Touchpoints:** `internal/sig/std/http.pg`, `internal/interp/web.go`,
  `examples/middleware.pg`.
- **Shipped:** `struct http.Middleware{name, apply}` and the four built-ins;
  `dispatch` wraps the outcome (including 404/405); `timeout` uses an R0019
  per-request deadline checked at step boundaries; `request_id` reuses or
  generates a reproducible id. Tests: `TestMiddleware*` (compose, request id,
  recover, access log, timeout, bad value) and `examples/middleware.pg`.
- **Effort:** M. **Deps:** none.

### SRV-2 · `crypto` + `jwt`  `done`
- **Problem:** authentication is the first real service need, and today it means
  `--allow python`, which grants file and network access too.
- **Design (standard library only, no new module dependency):**
  - `crypto.sha256(data) -> Str`, `crypto.hmac_sha256(key, data) -> Str`,
    `crypto.equal(a, b) -> Bool` (constant time), `crypto.pbkdf2(password, salt,
    iterations, length) -> Str` (PBKDF2-HMAC-SHA256, implemented over `hmac`),
    `crypto.random_bytes(n) -> !Str uses crypto`.
  - `jwt.verify_hs256(token, key) -> !Map[Str, Any]`,
    `jwt.sign_hs256(claims, key) -> !Str`; later RS256 with `crypto/x509` and
    `encoding/pem`.
  - New capability `crypto`, added to the closed set in `internal/sig/sig.go`.
    Hashing, HMAC, `equal` and JWT verification are pure; only randomness is an
    effect, and it is recorded so RT-3 replay stays deterministic.
- **Touchpoints:** `internal/sig/std/crypto.pg`, `internal/sig/std/jwt.pg`,
  `internal/sig/sig.go`, `internal/interp/`, guide and tests.
- **Acceptance:** RFC test vectors pass for SHA-256, HMAC-SHA256 and PBKDF2;
  `equal` is constant-time; a tampered JWT fails with a handled error;
  `uses crypto` is enforced and unknown capabilities are rejected by `check`.
- **Shipped:** `crypto.{sha256, hmac_sha256, equal, pbkdf2, random_bytes}` and
  `jwt.{sign_hs256, verify_hs256}`; `crypto` added to the closed capability set.
  Hashing, HMAC, PBKDF2 and JWT are pure; only `random_bytes` is an effect and
  it draws from the seeded generator, so replays and every engine agree.
  `verify_hs256` returns the claims map and leaves `exp`/`nbf` to the caller.
  Tests: SHA-256/HMAC-SHA256/PBKDF2 vectors, tampered-JWT failure, effect
  declaration and capability denial, plus `examples/crypto.pg`.
- **Effort:** L. **Deps:** none.

### SRV-3 · Observability  `done`
- `/metrics` in Prometheus text format and minimal OpenTelemetry spans;
  readiness/liveness endpoints (the example already has `/healthz`).
- **Acceptance:** counters for requests, latency histogram, in-flight gauge;
  spans exported through an OTLP endpoint configured by capability. (M)
- **Shipped:** the `http.metrics()` middleware and `http.metrics_text()`
  (Prometheus counters by method+status, a cumulative latency histogram, and
  in-flight/peak gauges); `http.tracing(service, endpoint)` exports one
  OTLP/HTTP JSON span per request (`uses net`, ids reproducible under `--seed`);
  `/healthz` and `/readyz` are plain routes. Tests: exposition format and bucket
  math, cross-engine dispatch, the OTLP payload against an `httptest` server,
  and effect enforcement; `examples/observability.pg`.
- **Effort:** M. **Deps:** none.

### SRV-4 · `extern sql`  `done`
- **Problem:** every service needs persistence, and today the only option is
  `--allow python`, which drops the typing (and the sandbox) on the floor.
- **Design:** a PostgreSQL client in the runtime itself (wire protocol over
  `net.Conn`), so the toolchain stays one static binary with no driver and no
  CGO. `sql.open(dsn)` returns an opaque handle and connects lazily; `query`
  returns `List[Map[Str, Any]]`, `query_as(..., schema: T)` decodes rows into a
  declared struct/enum (`E_SCHEMA` on a mismatch), `exec` returns the affected
  rows. Values travel out of band with the extended query protocol (`$1` or
  `?`, rewritten to `$1...`); nothing is concatenated into the SQL, so the
  `Sql` literal keeps its meaning. New capability `sql`, added to the closed
  set in `internal/sig/sig.go`.
- **Touchpoints:** `internal/sig/std/sql.pg`, `internal/sig/sig.go`,
  `internal/interp/sql.go`, guide and tests.
- **Acceptance:** queries work against a real PostgreSQL on all three engines;
  a tampered/missing column fails with `E_SCHEMA`; a refused connection,
  a bad DSN and a server error are `E_SQL` failures with the SQLSTATE;
  `uses sql` is enforced and denied without the grant.
- **Shipped:** `sql.{open, close, query, query_as, exec}` over the native
  wire protocol (startup, SCRAM-SHA-256/MD5/cleartext auth, TLS via
  `sslmode`); text results decode to `Int`/`Float`/`Bool`/`Str`, parameters
  accept `Int`/`Float`/`Str`/`Bool`/`nil`. Tests: DSN and placeholder
  parsing, value decoding, MD5/SCRAM, an in-process mock server on the three
  engines, and a real-server integration test in CI (service container).
  `examples/sql.pg`.
- **Effort:** L. **Deps:** none.

### SRV-5 · TLS  `done`
- `http.serve(addr, handler, tls: ...)` with a certificate path, or a documented
  reverse-proxy recipe; certificate reload without restart. (M)
- **Shipped:** `http.Tls{cert, key}` passed as the optional `tls:` argument of
  `http.serve`; HTTPS on the same port via `crypto/tls` (TLS 1.2 floor, HTTP/2
  negotiated), with the PEM pair re-read from disk whenever its modification
  time or size changes, so a renewal needs no restart. A broken replacement keeps
  the last good certificate; an unreadable path is an `E_TLS` failure before the
  port binds. Tests: the reloader (renewal, broken replacement, missing files),
  an end-to-end HTTPS server that swaps the certificate between requests, a TLS
  1.1 client refused by the floor, the `E_TLS` failure on all three engines and
  checker tests for the argument; `docs/SPEC.md` §14.3 documents the
  reverse-proxy alternative.
- **Effort:** M. **Deps:** none.

### SRV-6 · OpenAPI
- Serve a spec generated from `http.Route` and, with INT-1, generate a typed
  client from a spec. (M)

### SRV-7 · Multipart and streaming
- `http.multipart(req)` for file uploads; SSE and WebSocket handlers. (M)

### SRV-8 · Queues and streams
- Typed, capability-scoped `extern redis` / `extern nats` publishers and
  consumers, so background work does not need `python`. (L)

---

## Sequencing and ROI

| Wave | Items | Unlocks |
|------|-------|---------|
| 1 | GEN-1, REP-1, REP-2, TRU-5 | Cheaper, more reliable single-file generation |
| 2 | GEN-2, GEN-3, REP-3, TRU-1 | Robust multi-turn agent edits, verifiable artifacts |
| 3 | TRU-2, TRU-3, TRU-4 | Unattended execution of untrusted code |
| 4 | AI-1, RT-3, AGT-1..5 | Model-in-the-loop as a typed, replayable effect |
| 5 | RT-1, RT-5, INT-1, INT-2, SYN-1..3 | Ecosystem and scale |
| 6 | RT-2, RT-4, AI-2, AI-3, INT-3 | Moonshots |
| 7 | SRV-1, SRV-2 | A generated service can authenticate and has a canonical request pipeline |
| 8 | SRV-3..SRV-8 | Observability, persistence and integration for production |

## Measurement

Track per release, on a fixed task corpus:
- **Generation:** syntax-error rate, first-try compile rate, turns to green.
- **Repair:** tokens and turns per fixed error; auto-fix rate; regression rate.
- **Trust:** share of runs with no capability violation, no Go-fatal, verified
  `.pgc`; fuzz divergences found.
- **Cost:** tokens/latency/CPU per solved task.

## Principles

- A guarantee a human must remember is not a guarantee: move it into `check`.
- Every output is JSON with stable codes; anything not machine-readable is a bug.
- If it is not deterministic under a seed, it cannot be debugged or cached.
- New syntax is the last resort; new *guarantees* are the product.

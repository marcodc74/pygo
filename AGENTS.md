# AGENTS.md

Go implementation of **Pygo**, an AI-first programming language. This repo is the
*toolchain* (a single static Go binary); Pygo programs are `.pg` files. Do not
confuse this repo's Go source with Pygo language code.

## Build & test

```sh
go build -o pygo ./cmd/pygo          # single binary, no deps (CGO_ENABLED=0)
make test                            # gofmt check + go vet + go test ./... + pygo test examples/
make race                            # go test -race ./...
make integrations                    # self-test integrations/pygo_tools.py (needs python3)
make guide                           # regenerate docs/GUIDE.md from internal/guide/guide.md + stdlib sigs
go test -bench . ./internal/interp/  # engine benchmarks on bench/*.pg
```

- Single Go test: `go test ./internal/interp -run TestName`. The example golden
  and `fmt`-idempotency tests live in `cmd/pygo` (`go test ./cmd/pygo -run TestExamples`).
- `make vet` runs `gofmt -l .` + `go vet ./...`. Format with **gofmt** (not gofumpt).
- Makefile targets use POSIX shell (`grep`, `/tmp/`); on Windows run `go test ./...`
  and `go vet ./...` directly.
- CI uses Go 1.24 and Python 3.12 (go.mod says 1.22); race detector and gofmt
  check only run on Linux, examples run on all three OSes.

## Sources of truth & codegen

- `internal/sig/std/*.pg` are the stdlib signatures, **written in Pygo** and
  embedded via `//go:embed`. They drive the checker, runtime argument binding,
  `pygo describe` and `pygo guide`. Edit stdlib API here, not in Go.
- `internal/guide/guide.md` is the embedded language reference; `docs/GUIDE.md` is
  a generated snapshot. Run `make guide` after touching either. `TestGuide` fails
  if the guide loses key signatures or grows past ~6000 tokens.

## Test quirks

- `TestExamples` runs every `examples/*.pg` through **three engines** — `tree`,
  `vm`, and `pgc` (VM from a compiled `.pgc`) — and requires identical output,
  errors, trace and step count. It also fails on any bytecode-compiler
  **fallback** (`VMFallbacks()`): a new language construct must be implemented in
  the VM compiler, not only the tree interpreter. The VM is the default engine
  (`--engine vm`).
- `examples_test.go` holds golden output for `hello/errors/concurrency.pg`; update
  it when output changes intentionally.
- `TestFmtIsStable` enforces `pygo fmt` idempotency.
- `extern python` tests need a real Python 3 (`internal/interp/pybridge/bridge.py`);
  the interpreter runs Python in a separate process.

## Layout

- `cmd/pygo/` CLI; every subcommand supports `--json`.
- `internal/interp/` runtime: tree interpreter + bytecode VM (`vm_compile.go`
  compiles, `vm.go` executes, `pgc.go` `.pgc` format, `disasm.go`).
- `internal/check/` static checker (types, effects, nil, exhaustiveness, fixes).
- `internal/loader/` local modules (`import "./x"`), cycles, bundling.
- `bench/*.pg` feed the engine benchmarks; `integrations/` is the LLM-facing
  template/agent, not toolchain code.

## Conventions

- CLI exit codes are a public contract for agents: `0` ok, `1` unhandled failure,
  `2` panic, `3` compile error, `4` capability not granted.
- Stdlib capabilities are the closed set `clock crypto env fs net proc python rand`
  (`internal/sig/sig.go`).
- README.md is Italian; code, comments, `docs/*.md` and `internal/guide/guide.md`
  are English — don't translate between them.
- `integrations/AGENTS.md` is a **template** for other projects that *write Pygo*,
  copied into their root; it is not instructions for developing this toolchain.

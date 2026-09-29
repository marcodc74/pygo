# AGENTS.md

Go implementation of **Pygo**, an AI-first programming language. This repo is the
*toolchain* (a single static Go binary); Pygo programs are `.pg` files. Do not
confuse this repo's Go source with Pygo language code.

## Build & test

```sh
go build -o pygo ./cmd/pygo      # single binary, no deps (CGO_ENABLED=0)
make test                        # gofmt check + go vet + go test ./... + pygo test examples/
make race                        # go test -race ./...
make integrations                # self-test integrations/pygo_tools.py (needs python3)
make guide                       # regenerate docs/GUIDE.md from internal/guide/guide.md + stdlib sigs
```

- Single Go test: `go test ./internal/interp -run TestName`.
- `make vet` runs `gofmt -l .` + `go vet ./...`. Format with **gofmt** (not gofumpt).
- Makefile targets use POSIX shell (`grep`, `/tmp/`); on Windows run `go test ./...`
  and `go vet ./...` directly.
- CI uses Go 1.24 and Python 3.12 (go.mod says 1.22); race detector and gofmt
  check only run on Linux.

## Sources of truth & codegen

- `internal/sig/std/*.pg` are the stdlib signatures, **written in Pygo** and
  embedded via `//go:embed`. They drive the checker, runtime argument binding,
  `pygo describe` and `pygo guide`. Edit stdlib API here, not in Go.
- `internal/guide/guide.md` is the embedded language reference; `docs/GUIDE.md` is
  a generated snapshot. Run `make guide` after touching either.

## Test quirks

- `TestExamples` runs every `examples/*.pg` through **three engines** — `tree`,
  `vm`, and `pgc` (VM from a compiled `.pgc`) — and requires identical output,
  errors, trace and step count. When you change the interpreter or VM, both must
  agree; the VM is the default engine (`--engine vm`).
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

## Conventions

- CLI exit codes are a public contract for agents: `0` ok, `1` unhandled failure,
  `2` panic, `3` compile error, `4` capability not granted.
- Stdlib capabilities are the closed set `clock env fs net proc python rand`
  (`internal/sig/sig.go`).
- `integrations/AGENTS.md` is a **template** for other projects that *write Pygo*,
  copied into their root; it is not instructions for developing this toolchain.

# Pygo project instructions (for AI coding agents)

This project is written in Pygo (`.pg` files), a statically checked language
designed for AI agents. Pygo is probably not in your training data: do not
guess its syntax from Python, Go or Rust.

## Before writing any Pygo
Run `pygo guide` once and follow it exactly (about 3k tokens: syntax, types,
errors, effects, the whole stdlib signature list). There is one canonical
way to write each thing. Use only stdlib functions listed there.

## Loop for every change
1. Edit the file. For a whole declaration prefer
   `pygo edit FILE --replace fn:NAME` (keys from `pygo outline FILE`).
2. `pygo check --json FILE`. Fix every error; each diagnostic has a stable
   `code`, a `hint` and often a `fix`. `pygo fix FILE` applies the safe fixes;
   `pygo explain CODE` shows a wrong/right example.
3. `pygo test --json FILE_OR_DIR`. Failed asserts report operand values.
4. `pygo run --json --max-steps 10000000 --timeout 30s FILE`, adding
   `--allow CAPS` only for the capabilities the task needs
   (`fs net env proc clock rand python`).
5. `pygo fmt -w FILE` before finishing.

Exit codes: 0 ok, 1 unhandled failure, 2 panic, 3 compile error,
4 capability not granted. Read the JSON (codes, hints, values, trace)
before changing code.

## Rules of thumb
- Write `test "..." { }` blocks next to the logic you add.
- Fallible functions return `-> !T`; call them with `try` or `catch`.
- Python libraries: generate declarations with `pygo extern python MODULE NAME...`,
  never write them from memory.
- Never grant `--allow` capabilities the task does not need, and never
  `--allow python` on untrusted code (Python has no sandbox).

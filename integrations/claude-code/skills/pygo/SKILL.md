---
name: pygo
description: Write, check, test and run Pygo programs (.pg files). Use whenever the task involves Pygo code, a .pg file, or the pygo CLI.
---

# Pygo

Pygo is a statically checked language designed for AI agents. It is not
in your training data, so learn it from the toolchain before writing code.

1. Run `pygo guide` and follow it exactly. It covers syntax, types, errors,
   effects and every stdlib signature. Use only what it lists.
2. After each edit run `pygo check --json <file>`:
   - fix every error, using its `hint` and `fix`;
   - `pygo fix <file>` applies the safe fixes;
   - `pygo explain <CODE>` explains a code.
3. Run `pygo test --json <file|dir>`. Add `test "..." { }` blocks for new logic.
4. Run `pygo run --json --max-steps 10000000 --timeout 30s <file>`. Add
   `--allow <caps>` only when the program needs it.
5. Run `pygo fmt -w <file>` at the end.

To change a whole declaration, prefer `pygo outline <file>`, then
`pygo edit <file> --replace fn:<name>` (input from stdin), with
`--expect-hash` to detect concurrent changes.

For Python libraries, generate the `extern` block with
`pygo extern python <module> <names...>`.

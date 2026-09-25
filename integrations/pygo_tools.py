"""Pygo tools for LLM agents (provider-neutral).

Gives any model with function calling a sandboxed Pygo toolchain:

    from pygo_tools import PygoTools
    tools = PygoTools()                      # finds `pygo` on PATH (or PYGO_BIN)
    system = tools.system_prompt()           # rules + `pygo guide` of this binary
    specs = tools.anthropic_tools()          # or openai_tools() / gemini_declarations()
    output = tools.execute("pygo_run", {"code": "fn main() { print(1) }"})

Every tool call runs in a fresh temporary directory with no capabilities
unless the host allows them (`allow_caps`), a step budget and a timeout.
Only the Python standard library is used. Tested by `python3 pygo_tools.py`.
"""

import json
import os
import shutil
import subprocess
import tempfile

SYSTEM_RULES = """You write programs in Pygo, a statically checked language designed for AI agents.
Work in this loop and never skip a step:
1. Write the whole program (a `fn main()` plus `test "..." { }` blocks for the logic).
2. Call pygo_check. Fix every error; prefer the `fix` edit or `hint` of each diagnostic.
3. Call pygo_test until all tests pass, then pygo_run.
Rules: follow the reference below exactly; there is one canonical way to write each thing.
Do not invent stdlib functions: only use the signatures listed in the reference.
Declare effects with `uses` and ask only for the capabilities the task needs.
When a result is wrong, read the JSON (codes, hints, values, trace) before changing code."""

_SCHEMAS = [
    {
        "name": "pygo_check",
        "description": "Statically check a Pygo program. Returns JSON {ok, diagnostics[{code, severity, message, line, col, hint, fix?}]}. Run it after every edit.",
        "parameters": {
            "type": "object",
            "properties": {"code": {"type": "string", "description": "Complete source of main.pg"}},
            "required": ["code"],
        },
    },
    {
        "name": "pygo_fix",
        "description": "Apply the safe machine fixes of the diagnostics. Returns the fixed source and the remaining diagnostics.",
        "parameters": {
            "type": "object",
            "properties": {"code": {"type": "string", "description": "Complete source of main.pg"}},
            "required": ["code"],
        },
    },
    {
        "name": "pygo_test",
        "description": "Run the `test \"...\" { }` blocks. Returns JSON {ok, passed, failed, tests[{name, passed, failure?}]} with the values of failed asserts.",
        "parameters": {
            "type": "object",
            "properties": {
                "code": {"type": "string", "description": "Complete source of main.pg"},
                "filter": {"type": "string", "description": "Run only tests whose name contains this text"},
            },
            "required": ["code"],
        },
    },
    {
        "name": "pygo_run",
        "description": "Run main() in a sandbox. Returns JSON {exit_code, stdout, result{status, panic?, error?, steps}}. Exit codes: 0 ok, 1 failure, 2 panic, 3 compile error, 4 capability denied.",
        "parameters": {
            "type": "object",
            "properties": {
                "code": {"type": "string", "description": "Complete source of main.pg"},
                "allow": {
                    "type": "array",
                    "items": {"type": "string", "enum": ["fs", "net", "env", "proc", "clock", "rand", "python"]},
                    "description": "Capabilities the program needs (the host may refuse some)",
                },
                "args": {"type": "array", "items": {"type": "string"}, "description": "Command-line arguments"},
            },
            "required": ["code"],
        },
    },
    {
        "name": "pygo_explain",
        "description": "Explain a diagnostic or runtime code (e.g. E0306, R0009) with a wrong and a right example.",
        "parameters": {
            "type": "object",
            "properties": {"code_id": {"type": "string", "description": "The code, e.g. E0306"}},
            "required": ["code_id"],
        },
    },
]


class PygoTools:
    def __init__(self, pygo=None, allow_caps=(), max_steps=10_000_000, timeout_s=10,
                 engine=None, max_output=20_000):
        """allow_caps: capabilities the host lets programs use (default none).
        engine: None for the default engine, or "tree" / "vm"."""
        self.pygo = pygo or os.environ.get("PYGO_BIN") or shutil.which("pygo") or "pygo"
        if os.sep in self.pygo or "/" in self.pygo:
            self.pygo = os.path.abspath(self.pygo)  # tools run in temporary directories
        self.allow_caps = set(allow_caps)
        self.max_steps = max_steps
        self.timeout_s = timeout_s
        self.engine = engine
        self.max_output = max_output

    # ---------- prompts and tool definitions ----------

    def guide(self):
        """The compact language reference of the installed pygo (~3k tokens)."""
        return subprocess.run([self.pygo, "guide"], capture_output=True, text=True,
                              encoding="utf-8", check=True).stdout

    def system_prompt(self):
        return SYSTEM_RULES + "\n\n" + self.guide()

    @staticmethod
    def schemas():
        """Neutral definitions: name, description, JSON Schema parameters."""
        return json.loads(json.dumps(_SCHEMAS))

    def anthropic_tools(self):
        return [{"name": s["name"], "description": s["description"], "input_schema": s["parameters"]}
                for s in self.schemas()]

    def openai_tools(self):
        """Responses API format (type/name/description/parameters)."""
        return [{"type": "function", "name": s["name"], "description": s["description"],
                 "parameters": s["parameters"]} for s in self.schemas()]

    def openai_chat_tools(self):
        """Chat Completions format (also Ollama, vLLM, llama.cpp, LM Studio)."""
        return [{"type": "function", "function": {"name": s["name"], "description": s["description"],
                                                  "parameters": s["parameters"]}} for s in self.schemas()]

    def gemini_declarations(self):
        """google-genai FunctionDeclaration dicts (no enum on array items)."""
        out = []
        for s in self.schemas():
            params = s["parameters"]
            allow = params["properties"].get("allow")
            if allow:
                allow["items"].pop("enum", None)
            out.append({"name": s["name"], "description": s["description"], "parameters": params})
        return out

    # ---------- execution ----------

    def execute(self, name, args):
        """Run a tool call and return its output as a string for the model."""
        try:
            if name == "pygo_explain":
                return self._pygo(["explain", "--json", str(args.get("code_id", ""))], None)[1]
            code = args.get("code")
            if not isinstance(code, str) or not code.strip():
                return json.dumps({"error": "missing 'code' (the complete program)"})
            with tempfile.TemporaryDirectory(prefix="pygo-") as d:
                path = os.path.join(d, "main.pg")
                with open(path, "w", encoding="utf-8") as f:
                    f.write(code)
                if name == "pygo_check":
                    return self._pygo(["check", "--json", "main.pg"], d)[1]
                if name == "pygo_fix":
                    _, out, _ = self._pygo(["fix", "--json", "main.pg"], d)
                    with open(path, encoding="utf-8") as f:
                        fixed = f.read()
                    return json.dumps({"code": fixed, "report": _json_or_text(out)})
                if name == "pygo_test":
                    cmd = ["test", "--json", "--allow", ",".join(sorted(self.allow_caps))]
                    cmd += self._engine_flag()
                    if args.get("filter"):
                        cmd += ["--filter", str(args["filter"])]
                    return self._pygo(cmd + ["main.pg"], d)[1]
                if name == "pygo_run":
                    return self._run(path, d, args)
            return json.dumps({"error": "unknown tool " + name})
        except subprocess.TimeoutExpired:
            return json.dumps({"error": "the tool timed out"})

    def _run(self, path, d, args):
        wanted = [str(c) for c in args.get("allow") or []]
        refused = [c for c in wanted if c not in self.allow_caps]
        granted = [c for c in wanted if c in self.allow_caps]
        cmd = ["run", "--json", "--max-steps", str(self.max_steps), "--timeout", f"{self.timeout_s}s"]
        cmd += self._engine_flag()
        if granted:
            cmd += ["--allow", ",".join(granted)]
        cmd += ["main.pg", "--"] + [str(a) for a in args.get("args") or []]
        code, out, err = self._pygo(cmd, d)
        result, stderr = _split_result(err)
        resp = {"exit_code": code, "stdout": self._clip(out), "result": result}
        if stderr:
            resp["stderr"] = self._clip(stderr)
        if refused:
            resp["refused_capabilities"] = refused
        return json.dumps(resp, ensure_ascii=False)

    def _engine_flag(self):
        return ["--engine", self.engine] if self.engine else []

    def _pygo(self, cmd, cwd):
        """Returns (exit code, stdout, stderr); tools that answer in JSON on
        stdout fall back to stderr when stdout is empty."""
        p = subprocess.run([self.pygo] + cmd, cwd=cwd, capture_output=True, text=True,
                           encoding="utf-8", errors="replace", timeout=self.timeout_s + 20)
        if cmd[0] == "run":
            return p.returncode, p.stdout, p.stderr
        out = p.stdout if p.stdout.strip() else p.stderr
        return p.returncode, self._clip(out), p.stderr

    def _clip(self, s):
        if len(s) <= self.max_output:
            return s
        return s[: self.max_output] + f"\n... [{len(s) - self.max_output} more characters cut]"


def _split_result(stderr):
    """`pygo run --json` prints the result as the last stderr line."""
    lines = stderr.rstrip("\n").split("\n")
    if lines and lines[-1].startswith("{"):
        try:
            return json.loads(lines[-1]), "\n".join(lines[:-1])
        except ValueError:
            pass
    return None, stderr


def _json_or_text(s):
    try:
        return json.loads(s)
    except ValueError:
        return s


if __name__ == "__main__":
    # Self-test (no LLM needed): python3 pygo_tools.py
    t = PygoTools(allow_caps={"clock"})
    assert "Pygo quick reference" in t.system_prompt()
    assert len(t.anthropic_tools()) == len(t.openai_tools()) == len(t.gemini_declarations()) == 5
    bad = "fn main() {\n    let x = 1\n    x = 2\n    print(x)\n}\n"
    chk = json.loads(t.execute("pygo_check", {"code": bad}))
    assert not chk["ok"] and chk["diagnostics"][0]["code"] == "E0202", chk
    fixed = json.loads(t.execute("pygo_fix", {"code": bad}))
    assert "var x = 1" in fixed["code"], fixed
    run = json.loads(t.execute("pygo_run", {"code": fixed["code"]}))
    assert run["exit_code"] == 0 and run["stdout"] == "2\n", run
    assert chk["diagnostics"][0]["file"] == "main.pg", chk
    loop = json.loads(t.execute("pygo_run", {"code": "fn main() {\n    while true {\n    }\n}\n"}))
    assert loop["exit_code"] == 2 and loop["result"]["panic"]["code"] == "R0011", loop
    fs = "import \"fs\"\nfn main() -> ! uses fs {\n    print(try fs.read(\"x\"))\n}\n"
    denied = json.loads(t.execute("pygo_run", {"code": fs, "allow": ["fs"]}))
    assert denied["exit_code"] == 4 and denied["refused_capabilities"] == ["fs"], denied
    tst = json.loads(t.execute("pygo_test", {"code": "test \"t\" {\n    assert 1 + 1 == 3\n}\n"}))
    assert tst["failed"] == 1, tst
    assert "wrong" in json.loads(t.execute("pygo_explain", {"code_id": "E0306"}))
    print("pygo_tools self-test: ok")

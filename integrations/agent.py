"""Minimal Pygo coding agent for the main LLM providers.

    python agent.py --provider claude "Write a program that prints the first 20 primes"
    python agent.py --provider openai  "..."
    python agent.py --provider gemini  "..."
    python agent.py --provider local   "..."      # Ollama / vLLM / llama.cpp / LM Studio

The model gets the Pygo reference as system prompt and the tools of
pygo_tools.py (check, fix, test, run, explain); the loop runs until the
model answers without calling tools. Environment variables:

    ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY   provider keys
    PYGO_MODEL          model name (defaults below)
    PYGO_ALLOW          capabilities programs may use, e.g. "clock,rand"
    OPENAI_BASE_URL     endpoint of a local server (default http://localhost:11434/v1)
    PYGO_BIN            path of the pygo binary (default: pygo on PATH)
"""

import argparse
import json
import os
import sys

from pygo_tools import PygoTools

DEFAULT_MODELS = {
    "claude": "claude-sonnet-5",
    "openai": "gpt-5",
    "gemini": "gemini-2.5-pro",
    "local": "qwen2.5-coder:32b",
}
MAX_TURNS = 25


def log(name, args, output):
    shown = {k: v for k, v in args.items() if k != "code"}
    print(f"  -> {name}({json.dumps(shown)}): {output[:200]}", file=sys.stderr)


def run_claude(tools, model, task, client=None):
    import anthropic

    client = client or anthropic.Anthropic()
    # the reference is the same on every turn: cache it
    system = [{"type": "text", "text": tools.system_prompt(), "cache_control": {"type": "ephemeral"}}]
    messages = [{"role": "user", "content": task}]
    for _ in range(MAX_TURNS):
        resp = client.messages.create(model=model, max_tokens=8000, system=system,
                                      tools=tools.anthropic_tools(), messages=messages)
        messages.append({"role": "assistant", "content": resp.content})
        calls = [b for b in resp.content if b.type == "tool_use"]
        if not calls:
            return "".join(b.text for b in resp.content if b.type == "text")
        results = []
        for c in calls:
            out = tools.execute(c.name, c.input)
            log(c.name, c.input, out)
            results.append({"type": "tool_result", "tool_use_id": c.id, "content": out})
        messages.append({"role": "user", "content": results})
    return "(stopped after %d turns)" % MAX_TURNS


def run_openai(tools, model, task, client=None):
    from openai import OpenAI

    client = client or OpenAI()
    items = [{"role": "user", "content": task}]
    instructions = tools.system_prompt()
    for _ in range(MAX_TURNS):
        resp = client.responses.create(model=model, instructions=instructions,
                                       tools=tools.openai_tools(), input=items)
        items += resp.output
        calls = [o for o in resp.output if o.type == "function_call"]
        if not calls:
            return resp.output_text
        for c in calls:
            args = json.loads(c.arguments or "{}")
            out = tools.execute(c.name, args)
            log(c.name, args, out)
            items.append({"type": "function_call_output", "call_id": c.call_id, "output": out})
    return "(stopped after %d turns)" % MAX_TURNS


def run_gemini(tools, model, task, client=None):
    from google import genai
    from google.genai import types

    client = client or genai.Client()
    config = types.GenerateContentConfig(
        system_instruction=tools.system_prompt(),
        tools=[types.Tool(function_declarations=tools.gemini_declarations())],
        automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True),
    )
    contents = [types.Content(role="user", parts=[types.Part(text=task)])]
    for _ in range(MAX_TURNS):
        resp = client.models.generate_content(model=model, contents=contents, config=config)
        contents.append(resp.candidates[0].content)
        calls = resp.function_calls or []
        if not calls:
            return resp.text
        parts = []
        for c in calls:
            args = dict(c.args or {})
            out = tools.execute(c.name, args)
            log(c.name, args, out)
            parts.append(types.Part.from_function_response(name=c.name, response={"result": out}))
        contents.append(types.Content(role="user", parts=parts))
    return "(stopped after %d turns)" % MAX_TURNS


def run_local(tools, model, task, client=None):
    """Any OpenAI-compatible Chat Completions server with tool calling."""
    from openai import OpenAI

    client = client or OpenAI(base_url=os.environ.get("OPENAI_BASE_URL", "http://localhost:11434/v1"),
                              api_key=os.environ.get("OPENAI_API_KEY", "local"))
    messages = [{"role": "system", "content": tools.system_prompt()}, {"role": "user", "content": task}]
    for _ in range(MAX_TURNS):
        resp = client.chat.completions.create(model=model, messages=messages,
                                              tools=tools.openai_chat_tools())
        msg = resp.choices[0].message
        messages.append(msg.model_dump(exclude_none=True))
        if not msg.tool_calls:
            return msg.content or ""
        for tc in msg.tool_calls:
            args = json.loads(tc.function.arguments or "{}")
            out = tools.execute(tc.function.name, args)
            log(tc.function.name, args, out)
            messages.append({"role": "tool", "tool_call_id": tc.id, "content": out})
    return "(stopped after %d turns)" % MAX_TURNS


RUNNERS = {"claude": run_claude, "openai": run_openai, "gemini": run_gemini, "local": run_local}


def main():
    ap = argparse.ArgumentParser(description="Pygo coding agent")
    ap.add_argument("--provider", choices=sorted(RUNNERS), default="claude")
    ap.add_argument("--model", default=os.environ.get("PYGO_MODEL"))
    ap.add_argument("task")
    a = ap.parse_args()
    allow = [c for c in os.environ.get("PYGO_ALLOW", "").split(",") if c]
    tools = PygoTools(allow_caps=allow)
    print(RUNNERS[a.provider](tools, a.model or DEFAULT_MODELS[a.provider], a.task))


if __name__ == "__main__":
    main()

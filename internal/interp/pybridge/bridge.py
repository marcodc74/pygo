# Pygo <-> Python bridge. Started by the Pygo runtime as `python -u -c <this>`.
# Protocol: one JSON object per line on stdin (requests) and stdout (responses).
#   {"id": 1, "op": "call", "module": "statistics", "func": "mean",
#    "params": [{"name", "index", "value"}], "varargs": [...],
#    "ret": "value" | "handle", "release": [ids]}
#   {"id": 2, "op": "method", "handle": 7, "func": "describe", ...}
#   {"id": 3, "op": "describe", "module": "statistics", "names": ["mean"]}
# Responses: {"id": 1, "ok": true, "value": ...}
#            {"id": 1, "ok": false, "error": {"type", "message", "traceback", "import_error"}}
# Values: JSON; floats that are not finite as {"$float": "nan"|"inf"|"-inf"},
# ints outside int64 as {"$bigint": "..."}, objects that cannot be converted
# stay in Python and travel as {"$handle": id, "type": "module.Class"}.
import sys
import json
import math
import importlib
import traceback

# The protocol is UTF-8 on every OS (Windows defaults to the locale encoding).
for _stream in (sys.stdin, sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8", newline="\n")
    except (AttributeError, ValueError):
        pass
_proto_out = sys.stdout
sys.stdout = sys.stderr  # prints from library code must not corrupt the protocol

_handles = {}
_next_handle = [1]
_modules = {}


def _module(name):
    mod = _modules.get(name)
    if mod is None:
        mod = importlib.import_module(name)
        _modules[name] = mod
    return mod


def _new_handle(obj):
    hid = _next_handle[0]
    _next_handle[0] += 1
    _handles[hid] = obj
    t = type(obj)
    return {"$handle": hid, "type": t.__module__ + "." + t.__qualname__}


def _to_json(obj, depth=0):
    if depth > 100:
        raise ValueError("value too deeply nested to convert")
    if obj is None or isinstance(obj, (bool, str)):
        return obj
    if isinstance(obj, int):
        if -(2 ** 63) <= obj < 2 ** 63:
            return obj
        return {"$bigint": str(obj)}
    if isinstance(obj, float):
        if math.isnan(obj):
            return {"$float": "nan"}
        if math.isinf(obj):
            return {"$float": "inf" if obj > 0 else "-inf"}
        return obj
    mod = type(obj).__module__ or ""
    if mod == "numpy" or mod.startswith("numpy."):
        if hasattr(obj, "tolist"):
            return _to_json(obj.tolist(), depth + 1)
    if isinstance(obj, (list, tuple)):
        return [_to_json(x, depth + 1) for x in obj]
    if isinstance(obj, (set, frozenset)):
        items = list(obj)
        try:
            items.sort()
        except TypeError:
            pass
        return [_to_json(x, depth + 1) for x in items]
    if isinstance(obj, dict):
        if all(isinstance(k, str) for k in obj.keys()):
            return {k: _to_json(v, depth + 1) for k, v in obj.items()}
        return _new_handle(obj)
    try:
        import dataclasses
        if dataclasses.is_dataclass(obj) and not isinstance(obj, type):
            return _to_json(dataclasses.asdict(obj), depth + 1)
    except Exception:
        pass
    return _new_handle(obj)


def _from_json(v):
    if isinstance(v, list):
        return [_from_json(x) for x in v]
    if isinstance(v, dict):
        if "$handle" in v and len(v) == 1:
            hid = v["$handle"]
            if hid not in _handles:
                raise ValueError("Python object handle %d was released" % hid)
            return _handles[hid]
        if "$float" in v and len(v) == 1:
            return float(v["$float"])
        return {k: _from_json(x) for k, x in v.items()}
    return v


_PY_TO_PYGO = {int: "Int", float: "Float", str: "Str", bool: "Bool", type(None): "nil", object: "Any"}


def _pygo_type(ann):
    """Best-effort mapping of a Python annotation to a Pygo type."""
    import inspect
    import typing
    if ann is inspect.Parameter.empty or ann is typing.Any:
        return "Any"
    if ann in _PY_TO_PYGO:
        return _PY_TO_PYGO[ann]
    origin = getattr(typing, "get_origin", lambda a: None)(ann)
    args = getattr(typing, "get_args", lambda a: ())(ann)
    if origin in (list, tuple, set, frozenset) or ann in (list, tuple, set, frozenset):
        inner = _pygo_type(args[0]) if args else "Any"
        return "List[%s]" % inner
    if origin is dict or ann is dict:
        if len(args) == 2:
            return "Map[%s, %s]" % (_pygo_type(args[0]), _pygo_type(args[1]))
        return "Map[Str, Any]"
    if origin is typing.Union:
        non_none = [a for a in args if a is not type(None)]
        if len(non_none) == 1 and len(args) == 2:
            t = _pygo_type(non_none[0])
            return t if t == "Any" else t + "?"
    return "Any"


def _pygo_literal(v):
    if v is None:
        return "nil"
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int) and -(2 ** 63) <= v < 2 ** 63:
        return str(v)
    if isinstance(v, float) and math.isfinite(v):
        s = repr(v)
        return s if ("." in s or "e" in s) else s + ".0"
    if isinstance(v, str):
        return json.dumps(v)
    return None


def _describe(module, names):
    import inspect
    mod = _module(module)
    out = []
    if not names:
        # public callables defined in this module (not re-exported from others)
        names = [n for n in dir(mod) if not n.startswith("_") and callable(getattr(mod, n))
                 and getattr(getattr(mod, n), "__module__", module) == module]
    for name in names:
        obj = getattr(mod, name)
        entry = {"name": name, "doc": (inspect.getdoc(obj) or "").split("\n")[0], "params": [], "ret": "Any", "ok": True}
        try:
            sig = inspect.signature(obj)
        except (TypeError, ValueError):
            entry["ok"] = False
            out.append(entry)
            continue
        for p in sig.parameters.values():
            if p.kind in (p.VAR_KEYWORD,) or p.name.startswith("_"):
                continue
            prm = {"name": p.name, "type": _pygo_type(p.annotation), "variadic": p.kind == p.VAR_POSITIONAL}
            if p.default is not p.empty:
                lit = _pygo_literal(p.default)
                if lit is None or lit == "nil":
                    prm["default"] = "nil"
                    if not prm["type"].endswith("?") and prm["type"] != "Any":
                        prm["type"] += "?"
                else:
                    prm["default"] = lit
            entry["params"].append(prm)
        entry["ret"] = _pygo_type(sig.return_annotation)
        out.append(entry)
    return out


_sig_cache = {}


def _signature(target):
    import inspect
    key = id(target)
    if key not in _sig_cache:
        try:
            _sig_cache[key] = (target, inspect.signature(target))
        except (TypeError, ValueError):
            _sig_cache[key] = (target, None)
    return _sig_cache[key][1]


def _bind(target, params, varargs):
    """Build (args, kwargs) from the Pygo arguments.

    params: [{"name", "index", "value"}] for the arguments actually given, in
    declaration order; varargs: extra positional values. With a signature,
    positional-only parameters are passed by position and all others by
    keyword (so names are checked); without one, the leading run of
    consecutive arguments is positional and the rest are keywords."""
    sig = _signature(target)
    args, kwargs = [], {}
    if sig is None:
        expected = 0
        positional = True
        for p in params:
            if positional and p["index"] == expected:
                args.append(_from_json(p["value"]))
                expected += 1
            else:
                positional = False
                kwargs[p["name"]] = _from_json(p["value"])
        args.extend(_from_json(v) for v in varargs)
        return args, kwargs
    pyparams = list(sig.parameters.values())
    pos_only = [q for q in pyparams if q.kind == q.POSITIONAL_ONLY]
    has_varkw = any(q.kind == q.VAR_KEYWORD for q in pyparams)
    slots = {}
    for p in params:
        q = sig.parameters.get(p["name"])
        if q is not None and q.kind != q.POSITIONAL_ONLY:
            if q.kind == q.VAR_POSITIONAL:
                raise TypeError("parameter %r is variadic in Python: declare it as %s: ...T" % (p["name"], p["name"]))
            kwargs[p["name"]] = _from_json(p["value"])
            continue
        if q is not None:
            slots[pos_only.index(q)] = _from_json(p["value"])
            continue
        # a name unknown to Python: fill the next free positional-only slot
        free = [i for i in range(len(pos_only)) if i not in slots]
        if free and p["index"] == len(slots):
            slots[free[0]] = _from_json(p["value"])
        elif has_varkw:
            kwargs[p["name"]] = _from_json(p["value"])
        else:
            names = ", ".join(q.name for q in pyparams)
            raise TypeError("the Python function has no parameter %r (its parameters: %s); fix the extern declaration" % (p["name"], names))
    for i in range(len(slots)):
        if i not in slots:
            raise TypeError("positional-only parameter %r of the Python function was not given" % pos_only[i].name)
        args.append(slots[i])
    args.extend(_from_json(v) for v in varargs)
    return args, kwargs


def _handle(req):
    for hid in req.get("release") or []:
        _handles.pop(hid, None)
    op = req.get("op")
    if op == "ping":
        return {"python": sys.version.split()[0]}
    if op == "describe":
        return _describe(req["module"], req.get("names") or [])
    if op == "call":
        target = _module(req["module"])
        for part in req["func"].split("."):
            target = getattr(target, part)
    elif op == "method":
        hid = req["handle"]
        if hid not in _handles:
            raise ValueError("Python object handle %d was released" % hid)
        target = getattr(_handles[hid], req["func"])
    else:
        raise ValueError("unknown bridge op %r" % op)
    args, kwargs = _bind(target, req.get("params") or [], req.get("varargs") or [])
    result = target(*args, **kwargs)
    if req.get("ret") == "handle":
        return _new_handle(result)
    return _to_json(result)


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        rid = None
        try:
            req = json.loads(line)
            rid = req.get("id")
            resp = {"id": rid, "ok": True, "value": _handle(req)}
        except BaseException as e:  # report everything, including SystemExit from libraries
            resp = {"id": rid, "ok": False, "error": {
                "type": type(e).__name__,
                "message": str(e),
                "traceback": traceback.format_exc(limit=20),
                "import_error": isinstance(e, ImportError),
                "missing_module": getattr(e, "name", None) if isinstance(e, ImportError) else None,
            }}
        try:
            text = json.dumps(resp, allow_nan=False)
        except (TypeError, ValueError) as e:
            text = json.dumps({"id": rid, "ok": False, "error": {
                "type": type(e).__name__, "message": "cannot encode result: %s" % e,
                "traceback": "", "import_error": False, "missing_module": None}})
        _proto_out.write(text + "\n")
        _proto_out.flush()


main()

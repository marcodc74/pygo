package interp

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/printer"
)

//go:embed pybridge/bridge.py
var bridgeSource string

// ExternType is an opaque foreign type declared in an extern block.
type ExternType struct {
	Name    string // qualified: alias.Type
	Methods map[string]*ast.FuncDecl
	Module  *Module
}

// PyHandle references a Python object that lives in the bridge process.
type PyHandle struct {
	ID     int64
	PyType string      // Python type, e.g. "fractions.Fraction"
	T      *ExternType // declared Pygo type (nil if unknown, e.g. under Any)
	bridge *pyBridge
}

// pyBigInt is a Python int that does not fit Int (only transient).
type pyBigInt string

type pyBridge struct {
	in      *Interp
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  int64
	relMu   sync.Mutex
	release []int64
}

// python returns the bridge of this interpreter (the process starts lazily).
func (in *Interp) python() *pyBridge {
	in.pyOnce.Do(func() { in.py = &pyBridge{in: in} })
	return in.py
}

// ClosePython stops the bridge process, if running.
func (in *Interp) ClosePython() {
	if in.py == nil {
		return
	}
	in.py.mu.Lock()
	defer in.py.mu.Unlock()
	in.py.stopLocked()
}

func (b *pyBridge) stopLocked() {
	if b.cmd != nil && b.cmd.Process != nil {
		b.stdin.Close()
		b.cmd.Process.Kill()
		b.cmd.Wait()
	}
	b.cmd, b.stdin, b.stdout = nil, nil, nil
}

// pythonCandidates lists the interpreters to try, in order.
func (in *Interp) pythonCandidates() []string {
	if in.opt.Python != "" {
		return []string{in.opt.Python}
	}
	if p := os.Getenv("PYGO_PYTHON"); p != "" {
		return []string{p}
	}
	if runtime.GOOS == "windows" {
		return []string{"python", "python3", "py"}
	}
	return []string{"python3", "python"}
}

func (b *pyBridge) startLocked(th *Thread) error {
	var lastErr error
	for _, exe := range b.in.pythonCandidates() {
		path, err := exec.LookPath(exe)
		if err != nil {
			lastErr = err
			continue
		}
		cmd := exec.Command(path, "-u", "-c", bridgeSource)
		cmd.Stderr = b.in.opt.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		b.cmd, b.stdin, b.stdout = cmd, stdin, bufio.NewReaderSize(stdout, 1<<16)
		return nil
	}
	return th.fail("E_PYTHON_UNAVAILABLE", "Python not found (%v): install Python 3 or pass --python /path/to/python", lastErr)
}

func (b *pyBridge) queueRelease(id int64) {
	b.relMu.Lock()
	b.release = append(b.release, id)
	b.relMu.Unlock()
}

type pyError struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	Traceback     string `json:"traceback"`
	ImportError   bool   `json:"import_error"`
	MissingModule string `json:"missing_module"`
}

type pyResponse struct {
	ID    int64           `json:"id"`
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error *pyError        `json:"error"`
}

// request sends one request and waits for its response, honoring --timeout.
func (b *pyBridge) request(th *Thread, req map[string]any) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd == nil {
		if err := b.startLocked(th); err != nil {
			return nil, err
		}
	}
	b.nextID++
	req["id"] = b.nextID
	b.relMu.Lock()
	if len(b.release) > 0 {
		req["release"] = b.release
		b.release = nil
	}
	b.relMu.Unlock()
	line, err := json.Marshal(req)
	if err != nil {
		return nil, perr(PType, "", "cannot send value to Python: %v", err)
	}
	if _, err := b.stdin.Write(append(line, '\n')); err != nil {
		b.stopLocked()
		return nil, th.fail("E_PYTHON_UNAVAILABLE", "the Python worker exited: %v", err)
	}
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	out := b.stdout
	go func() {
		l, err := out.ReadString('\n')
		ch <- result{l, err}
	}()
	var r result
	if dl := b.in.deadline; !dl.IsZero() {
		select {
		case r = <-ch:
		case <-time.After(time.Until(dl)):
			b.stopLocked()
			return nil, &Panic{Code: PTimeout, Message: fmt.Sprintf("timeout of %s exceeded while waiting for Python", b.in.opt.Timeout), Hint: "raise --timeout, or make the Python call faster"}
		}
	} else {
		r = <-ch
	}
	if r.err != nil {
		b.stopLocked()
		return nil, th.fail("E_PYTHON_UNAVAILABLE", "the Python worker exited unexpectedly (see stderr)")
	}
	var resp pyResponse
	if err := json.Unmarshal([]byte(r.line), &resp); err != nil {
		b.stopLocked()
		return nil, th.fail("E_PYTHON_UNAVAILABLE", "invalid response from the Python worker: %v", err)
	}
	if !resp.OK {
		e := resp.Error
		if e == nil {
			e = &pyError{Type: "Error", Message: "unknown Python error"}
		}
		data := NewMap()
		data.Set("type", e.Type)
		data.Set("traceback", e.Traceback)
		code, msg := "E_PYTHON", e.Type+": "+e.Message
		if e.ImportError {
			code = "E_PYTHON_IMPORT"
			if e.MissingModule != "" {
				root := strings.SplitN(e.MissingModule, ".", 2)[0]
				msg += " (install it: pip install " + root + ")"
			}
		}
		return nil, &Failure{Err: b.in.newError(msg, code, data), Trace: th.trace()}
	}
	return resp.Value, nil
}

// ---------- values Pygo -> Python ----------

type pyFloat float64

func (f pyFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	switch {
	case math.IsNaN(v):
		return []byte(`{"$float":"nan"}`), nil
	case math.IsInf(v, 1):
		return []byte(`{"$float":"inf"}`), nil
	case math.IsInf(v, -1):
		return []byte(`{"$float":"-inf"}`), nil
	}
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return []byte(s), nil
}

func pyEncode(v Value, depth int) (any, error) {
	if depth > 100 {
		return nil, fmt.Errorf("value too deeply nested")
	}
	switch x := v.(type) {
	case nil, bool, int64, string:
		return x, nil
	case float64:
		return pyFloat(x), nil
	case *List:
		items := x.Snapshot()
		out := make([]any, len(items))
		for i, it := range items {
			e, err := pyEncode(it, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case *Map:
		ks, vs := x.Items()
		out := make(map[string]any, len(ks))
		for i := range ks {
			e, err := pyEncode(vs[i], depth+1)
			if err != nil {
				return nil, err
			}
			out[Str(ks[i])] = e
		}
		return out, nil
	case *Struct:
		vals := x.Snapshot()
		out := make(map[string]any, len(vals))
		for i, f := range x.T.Fields {
			e, err := pyEncode(vals[i], depth+1)
			if err != nil {
				return nil, err
			}
			out[f.Name] = e
		}
		return out, nil
	case *Enum:
		out := map[string]any{"variant": x.V.Name}
		for i, f := range x.V.Fields {
			e, err := pyEncode(x.F[i], depth+1)
			if err != nil {
				return nil, err
			}
			out[f.Name] = e
		}
		return out, nil
	case *PyHandle:
		return map[string]any{"$handle": x.ID}, nil
	}
	return nil, fmt.Errorf("a %s cannot be passed to Python", TypeName(v))
}

// ---------- values Python -> Pygo ----------

func (b *pyBridge) decode(raw json.RawMessage) (Value, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	v, err := decodeJSON(string(raw))
	if err != nil {
		return nil, err
	}
	return b.lift(v), nil
}

// lift turns the bridge's JSON markers into runtime values.
func (b *pyBridge) lift(v Value) Value {
	switch x := v.(type) {
	case *List:
		items := x.Snapshot()
		for i, it := range items {
			items[i] = b.lift(it)
		}
		return NewList(items)
	case *Map:
		ks, vs := x.Items()
		if len(ks) == 2 {
			if id, ok := x.Get("$handle"); ok {
				if n, ok := id.(int64); ok {
					t, _ := x.Get("type")
					h := &PyHandle{ID: n, PyType: Str(t), bridge: b}
					runtime.SetFinalizer(h, func(h *PyHandle) { h.bridge.queueRelease(h.ID) })
					return h
				}
			}
		}
		if len(ks) == 1 {
			if f, ok := x.Get("$float"); ok {
				p, _ := strconv.ParseFloat(Str(f), 64)
				return p
			}
			if n, ok := x.Get("$bigint"); ok {
				return pyBigInt(Str(n))
			}
		}
		m := NewMap()
		for i := range ks {
			m.Set(ks[i], b.lift(vs[i]))
		}
		return m
	}
	return v
}

// ---------- extern modules ----------

func withPython(fd *ast.FuncDecl) *ast.FuncDecl {
	for _, u := range fd.Uses {
		if u == "python" {
			return fd
		}
	}
	c := *fd
	c.Uses = append(append([]string{}, fd.Uses...), "python")
	return &c
}

// externModule builds the runtime module of an extern python block.
func (in *Interp) externModule(d *ast.ExternDecl, file string) *Module {
	name := d.Name()
	m := &Module{Name: name, Path: file, Members: map[string]Value{}, Types: map[string]any{}, Imports: map[string]*Module{}}
	m.Env = NewEnv(in.universe)
	for _, t := range d.Types {
		et := &ExternType{Name: name + "." + t.Name, Methods: map[string]*ast.FuncDecl{}, Module: m}
		for _, md := range t.Methods {
			et.Methods[md.Name] = withPython(md)
		}
		m.Types[t.Name] = et
	}
	for _, fd := range d.Funcs {
		fd := withPython(fd)
		module, fn := d.Module, fd.Name
		m.Members[fn] = &Builtin{Name: name + "." + fn, Decl: fd, Mod: "", TypeMod: m, Fn: func(th *Thread, _ Value, args []Value) (Value, error) {
			return th.pyInvoke(map[string]any{"op": "call", "module": module, "func": fn}, fd, args, m)
		}}
	}
	return m
}

// pyMethod returns the bound method name of a handle.
func (th *Thread) pyMethod(h *PyHandle, name string, pos ast.Pos) (Value, error) {
	if h.T == nil {
		return nil, th.panicAt(pos, PName, "declare the object's type in the extern block and return it from a function typed with it", "cannot call '%s' on a Python %s of undeclared type", name, h.PyType)
	}
	md, ok := h.T.Methods[name]
	if !ok {
		var names []string
		for k := range h.T.Methods {
			names = append(names, k)
		}
		return nil, th.panicAt(pos, PName, suggestHint(name, names), "%s has no method '%s' (declare it in the extern block)", h.T.Name, name)
	}
	mod := h.T.Module
	return &Builtin{Name: h.T.Name + "." + name, Decl: md, Recv: h, TypeMod: mod, Fn: func(th *Thread, recv Value, args []Value) (Value, error) {
		hh := recv.(*PyHandle)
		return th.pyInvoke(map[string]any{"op": "method", "handle": hh.ID, "func": md.Name}, md, args, mod)
	}}, nil
}

// pyInvoke performs a call. Arguments are sent with their names and
// declaration index; the bridge binds them against the real Python
// signature (positional-only by position, the rest by keyword). A parameter
// whose default is nil and whose value is nil is omitted, so the Python
// default applies. The result is validated against the declared type.
func (th *Thread) pyInvoke(req map[string]any, fd *ast.FuncDecl, args []Value, m *Module) (Value, error) {
	params := []map[string]any{}
	varargs := []any{}
	for i, p := range fd.Params {
		v := args[i]
		if v == nil {
			if _, isNil := p.Default.(*ast.NilLit); isNil {
				continue
			}
		}
		if p.Variadic {
			for _, it := range v.(*List).Snapshot() {
				e, err := pyEncode(it, 0)
				if err != nil {
					return nil, perr(PType, "", "argument '%s' of %s: %v", p.Name, fd.Name, err)
				}
				varargs = append(varargs, e)
			}
			continue
		}
		e, err := pyEncode(v, 0)
		if err != nil {
			return nil, perr(PType, "", "argument '%s' of %s: %v", p.Name, fd.Name, err)
		}
		params = append(params, map[string]any{"name": p.Name, "index": i, "value": e})
	}
	req["params"] = params
	req["varargs"] = varargs
	req["ret"] = "value"
	if isExternType(fd.Ret, m) {
		req["ret"] = "handle"
	}
	b := th.in.python()
	raw, err := b.request(th, req)
	if err != nil {
		return nil, err
	}
	v, err := b.decode(raw)
	if err != nil {
		return nil, th.fail("E_PYTHON", "invalid value from Python: %v", err)
	}
	if fd.Ret == nil {
		return nil, nil
	}
	out, cerr := th.convert(v, fd.Ret, m, "result")
	if cerr != "" {
		return nil, th.fail("E_PYTHON_TYPE", "%s returned a value that does not match %s: %s", fd.Name, printer.Type(fd.Ret), cerr)
	}
	return out, nil
}

func isExternType(te *ast.TypeExpr, m *Module) bool {
	if te == nil || m == nil {
		return false
	}
	_, ok := m.Types[te.Name].(*ExternType)
	return ok
}

// DescribePython asks Python for the signatures of names in module (for
// `pygo extern`). An empty names list describes every public callable.
func (in *Interp) DescribePython(module string, names []string) (json.RawMessage, error) {
	th := in.newThread(nil)
	defer in.ClosePython()
	raw, err := in.python().request(th, map[string]any{"op": "describe", "module": module, "names": names})
	if err != nil {
		if f, ok := err.(*Failure); ok {
			return nil, fmt.Errorf("%s", f.Error())
		}
		return nil, err
	}
	return raw, nil
}

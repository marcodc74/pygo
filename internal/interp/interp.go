// Package interp is the tree-walking runtime of Pygo.
package interp

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

// Options configure a run.
type Options struct {
	Allow    map[string]bool // granted capabilities
	MaxSteps int64           // 0 = unlimited
	Timeout  time.Duration   // 0 = unlimited
	Seed     int64
	Args     []string
	Stdout   io.Writer
	Stderr   io.Writer
	Stdin    io.Reader
	Python   string    // Python interpreter for extern python blocks ("" = PYGO_PYTHON or python3)
	Engine   string    // "vm" (bytecode virtual machine, default) or "tree" (interpreter)
	Compiled *Compiled // bytecode already compiled (from a .pgc file); nil = compile at load; can be shared
}

// UseVM reports whether function bodies run on the bytecode VM.
func (o Options) UseVM() bool { return o.Engine != "tree" }

// precompiled returns the stored bytecode for a declaration, if any.
func (in *Interp) precompiled(d *ast.FuncDecl) (*Proto, bool) {
	if in.opt.Compiled == nil {
		return nil, false
	}
	p := in.opt.Compiled.Funcs[d]
	if p == nil {
		return nil, false
	}
	return p.instance(), true
}

type Interp struct {
	opt      Options
	prog     *loader.Program
	steps    atomic.Int64
	deadline time.Time
	out      *syncWriter
	errw     *syncWriter
	stdin    *bufio.Reader
	stdinMu  sync.Mutex
	rngMu    sync.Mutex
	rng      *rand.Rand
	modMu    sync.Mutex
	modules  map[string]*Module
	std      map[string]*Module
	universe *Env
	errType  *StructType
	py       *pyBridge
	pyOnce   sync.Once
	vmMu     sync.Mutex
	vmFail   []string // functions left to the interpreter (compile errors)
}

// VMFallbacks lists the functions the bytecode compiler could not compile
// (they ran on the interpreter), with the reason.
func (in *Interp) VMFallbacks() []string {
	in.vmMu.Lock()
	defer in.vmMu.Unlock()
	return append([]string(nil), in.vmFail...)
}

func (in *Interp) vmFallback(name string, err error) {
	in.vmMu.Lock()
	in.vmFail = append(in.vmFail, name+": "+err.Error())
	in.vmMu.Unlock()
}

type syncWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (s *syncWriter) WriteString(str string) {
	s.mu.Lock()
	s.w.WriteString(str)
	s.mu.Unlock()
}

func (s *syncWriter) Flush() {
	s.mu.Lock()
	s.w.Flush()
	s.mu.Unlock()
}

func New(prog *loader.Program, opt Options) *Interp {
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if opt.Stdin == nil {
		opt.Stdin = os.Stdin
	}
	if opt.Allow == nil {
		opt.Allow = map[string]bool{}
	}
	in := &Interp{
		opt:     opt,
		prog:    prog,
		out:     &syncWriter{w: bufio.NewWriter(opt.Stdout)},
		errw:    &syncWriter{w: bufio.NewWriter(opt.Stderr)},
		stdin:   bufio.NewReader(opt.Stdin),
		rng:     rand.New(rand.NewSource(opt.Seed)),
		modules: map[string]*Module{},
		std:     map[string]*Module{},
	}
	if opt.Timeout > 0 {
		in.deadline = time.Now().Add(opt.Timeout)
	}
	in.initUniverse()
	return in
}

// Flush writes buffered output.
func (in *Interp) Flush() {
	in.out.Flush()
	in.errw.Flush()
}

// ---------- environments ----------

type binding struct {
	v       Value
	mutable bool
}

type Env struct {
	mu     sync.RWMutex
	vars   map[string]*binding
	parent *Env
}

func NewEnv(parent *Env) *Env { return &Env{vars: make(map[string]*binding, 4), parent: parent} }

func (e *Env) Define(name string, v Value, mutable bool) {
	e.mu.Lock()
	e.vars[name] = &binding{v: v, mutable: mutable}
	e.mu.Unlock()
}

func (e *Env) Get(name string) (Value, bool) {
	for s := e; s != nil; s = s.parent {
		s.mu.RLock()
		b, ok := s.vars[name]
		var v Value
		if ok {
			v = b.v
		}
		s.mu.RUnlock()
		if ok {
			return v, true
		}
	}
	return nil, false
}

// Set assigns an existing variable. found=false if undefined.
func (e *Env) Set(name string, v Value) (found, mutable bool) {
	for s := e; s != nil; s = s.parent {
		s.mu.Lock()
		b, ok := s.vars[name]
		if ok {
			if b.mutable {
				b.v = v
			}
			s.mu.Unlock()
			return true, b.mutable
		}
		s.mu.Unlock()
	}
	return false, false
}

// ---------- threads ----------

type frame struct {
	name   string
	mod    *Module
	pos    ast.Pos
	defers []func() error
}

// Thread is the per-goroutine execution state.
type Thread struct {
	in       *Interp
	frames   []*frame
	tryDepth int
	pending  int64 // steps not yet added to Interp.steps (no step budget)
}

func (in *Interp) newThread(mod *Module) *Thread {
	th := &Thread{in: in}
	th.frames = append(th.frames, &frame{name: "<module>", mod: mod})
	return th
}

func (th *Thread) top() *frame { return th.frames[len(th.frames)-1] }

func (th *Thread) mod() *Module { return th.top().mod }

func (th *Thread) file() string {
	if m := th.mod(); m != nil {
		return m.Path
	}
	return ""
}

func (th *Thread) trace() []string {
	var out []string
	for i := len(th.frames) - 1; i >= 0 && len(out) < 30; i-- {
		f := th.frames[i]
		if f.name == "<module>" && f.pos.Line == 0 {
			continue
		}
		file := ""
		if f.mod != nil {
			file = f.mod.Path
		}
		out = append(out, fmt.Sprintf("%s (%s:%d:%d)", f.name, file, f.pos.Line, f.pos.Col))
	}
	return out
}

func (th *Thread) step(pos ast.Pos) error {
	th.top().pos = pos
	if th.in.opt.MaxSteps == 0 {
		// no budget: count locally, publish in batches (and at thread end)
		th.pending++
		if th.pending&1023 == 0 {
			th.flushSteps()
			if !th.in.deadline.IsZero() && time.Now().After(th.in.deadline) {
				return th.panicAt(pos, PTimeout, "raise --timeout or look for an infinite loop / blocking call", "timeout of %s exceeded", th.in.opt.Timeout)
			}
		}
		return nil
	}
	n := th.in.steps.Add(1)
	if n > th.in.opt.MaxSteps {
		return th.panicAt(pos, PBudget, "raise --max-steps or look for an infinite loop", "step budget of %d exhausted", th.in.opt.MaxSteps)
	}
	if n&1023 == 0 && !th.in.deadline.IsZero() && time.Now().After(th.in.deadline) {
		return th.panicAt(pos, PTimeout, "raise --timeout or look for an infinite loop / blocking call", "timeout of %s exceeded", th.in.opt.Timeout)
	}
	return nil
}

// flushSteps adds the locally counted steps to the program total.
func (th *Thread) flushSteps() {
	if th.pending > 0 {
		th.in.steps.Add(th.pending)
		th.pending = 0
	}
}

// evalDetached evaluates e on a short-lived thread of module m (defaults
// of struct fields).
func (in *Interp) evalDetached(m *Module, env *Env, e ast.Expr) (Value, error) {
	th := in.newThread(m)
	defer th.flushSteps()
	return th.eval(env, e)
}

// ---------- modules ----------

func (in *Interp) initUniverse() {
	in.universe = NewEnv(nil)
	core := sig.Get("core")
	coreMod := &Module{Name: "core", Path: "<core>", Members: map[string]Value{}, Types: map[string]any{}, Imports: map[string]*Module{}, Std: true, Env: in.universe}
	for name, sd := range core.Structs {
		st := in.makeStructType(sd, coreMod)
		coreMod.Types[name] = st
		in.universe.Define(name, st, false)
	}
	in.errType = coreMod.Types["Error"].(*StructType)
	for name, fd := range core.Funcs {
		impl := stdImpls["core"][name]
		if impl == nil {
			panic("missing implementation of core builtin " + name)
		}
		b := &Builtin{Name: name, Decl: fd, Fn: impl, Mod: "core"}
		coreMod.Members[name] = b
		in.universe.Define(name, b, false)
	}
	in.std["core"] = coreMod
}

func (in *Interp) stdModule(name string) *Module {
	in.modMu.Lock()
	defer in.modMu.Unlock()
	if m, ok := in.std[name]; ok {
		return m
	}
	h := sig.Get(name)
	if h == nil {
		return nil
	}
	m := &Module{Name: name, Path: "<std/" + name + ">", Members: map[string]Value{}, Types: map[string]any{}, Imports: map[string]*Module{}, Std: true}
	m.Env = NewEnv(in.universe)
	for sn, sd := range h.Structs {
		st := in.makeStructType(sd, m)
		m.Types[sn] = st
		m.Members[sn] = st
		m.Env.Define(sn, st, false)
	}
	for fn, fd := range h.Funcs {
		impl := stdImpls[name][fn]
		if impl == nil {
			panic("missing implementation of " + name + "." + fn)
		}
		b := &Builtin{Name: name + "." + fn, Decl: fd, Fn: impl, Mod: name}
		m.Members[fn] = b
	}
	th := in.newThread(m)
	defer th.flushSteps()
	for _, cn := range h.Order {
		if cd, ok := h.Consts[cn]; ok {
			v, err := th.eval(m.Env, cd.Let.Value)
			if err != nil {
				panic(err)
			}
			m.Members[cn] = v
		}
	}
	in.std[name] = m
	return m
}

func (in *Interp) makeStructType(sd *ast.StructDecl, m *Module) *StructType {
	st := &StructType{Name: sd.Name, Module: m, FieldIdx: map[string]int{}, Methods: map[string]*Function{}}
	for i, f := range sd.Fields {
		st.Fields = append(st.Fields, &FieldInfo{Name: f.Name, Type: f.Type, Default: f.Default})
		st.FieldIdx[f.Name] = i
	}
	return st
}

// ModuleName returns the binding name for an import path.
func ModuleName(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, ".pg")
}

// Load evaluates a module (and its imports) once.
func (in *Interp) Load(file string) (*Module, error) {
	in.modMu.Lock()
	if m, ok := in.modules[file]; ok {
		in.modMu.Unlock()
		return m, nil
	}
	f := in.prog.Files[file]
	if f == nil {
		in.modMu.Unlock()
		return nil, fmt.Errorf("module %s not loaded", file)
	}
	m := &Module{Name: ModuleName(file), Path: file, Members: map[string]Value{}, Types: map[string]any{}, Imports: map[string]*Module{}}
	m.Env = NewEnv(in.universe)
	in.modules[file] = m
	in.modMu.Unlock()

	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.StructDecl:
			st := in.makeStructType(d, m)
			m.Types[d.Name] = st
			m.Members[d.Name] = st
			m.Env.Define(d.Name, st, false)
		case *ast.EnumDecl:
			et := &EnumType{Name: d.Name, Module: m, ByName: map[string]*VariantInfo{}, Methods: map[string]*Function{}}
			for i, v := range d.Variants {
				vi := &VariantInfo{Enum: et, Name: v.Name, Index: i}
				for _, f := range v.Fields {
					vi.Fields = append(vi.Fields, &FieldInfo{Name: f.Name, Type: f.Type, Default: f.Default})
				}
				et.Variants = append(et.Variants, vi)
				et.ByName[v.Name] = vi
			}
			m.Types[d.Name] = et
			m.Members[d.Name] = et
			m.Env.Define(d.Name, et, false)
		}
	}
	for _, d := range f.Decls {
		if ex, ok := d.(*ast.ExternDecl); ok {
			em := in.externModule(ex, file)
			m.Imports[ex.Name()] = em
			m.Env.Define(ex.Name(), em, false)
			continue
		}
		imp, ok := d.(*ast.ImportDecl)
		if !ok {
			continue
		}
		var im *Module
		if loader.IsLocal(imp.Path) {
			target := in.prog.Imports[file][imp.Path]
			var err error
			im, err = in.Load(target)
			if err != nil {
				return nil, err
			}
		} else {
			im = in.stdModule(imp.Path)
			if im == nil {
				return nil, fmt.Errorf("unknown module %q", imp.Path)
			}
		}
		name := imp.Alias
		if name == "" {
			name = ModuleName(imp.Path)
		}
		m.Imports[name] = im
		m.Env.Define(name, im, false)
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			fn := in.makeFunc(d, m, nil)
			m.Members[d.Name] = fn
			m.Env.Define(d.Name, fn, false)
		case *ast.ImplDecl:
			var methods map[string]*Function
			var recv any
			switch t := m.Types[d.Type].(type) {
			case *StructType:
				methods, recv = t.Methods, t
			case *EnumType:
				methods, recv = t.Methods, t
			default:
				return nil, fmt.Errorf("%s: impl of unknown type %s", file, d.Type)
			}
			for _, md := range d.Methods {
				methods[md.Name] = in.makeFunc(md, m, recv)
			}
		}
	}
	th := in.newThread(m)
	defer th.flushSteps()
	for _, d := range f.Decls {
		if c, ok := d.(*ast.ConstDecl); ok {
			v, err := th.eval(m.Env, c.Let.Value)
			if err != nil {
				return nil, err
			}
			m.Members[c.Let.Name] = v
			m.Env.Define(c.Let.Name, v, false)
		}
	}
	m.loaded.Store(true)
	return m, nil
}

func (in *Interp) makeFunc(d *ast.FuncDecl, m *Module, recv any) *Function {
	name := d.Name
	switch t := recv.(type) {
	case *StructType:
		name = t.Name + "." + d.Name
	case *EnumType:
		name = t.Name + "." + d.Name
	}
	f := &Function{
		Name: name, Params: d.Params, Ret: d.Ret, Fallible: d.Fallible,
		Body: d.Body, ExprBody: d.ExprBody, Requires: d.Requires, Ensures: d.Ensures,
		Env: m.Env, Mod: m, Pos: d.Pos, HasSelf: d.HasSelf, RecvType: recv,
	}
	if len(d.TypeParams) > 0 {
		f.TypeParams = map[string]bool{}
		for _, tp := range d.TypeParams {
			f.TypeParams[tp] = true
		}
	}
	if in.opt.UseVM() {
		if p, ok := in.precompiled(d); ok {
			f.Proto = p
		} else {
			// a body the compiler does not support stays on the interpreter
			var err error
			if f.Proto, err = compileFunc(name, d.HasSelf, d.Params, d.Body, d.ExprBody); err != nil {
				in.vmFallback(name, err)
			}
		}
	}
	return f
}

// ---------- statements ----------

func (th *Thread) execBlock(env *Env, b *ast.Block) error {
	inner := NewEnv(env)
	for _, s := range b.Stmts {
		if err := th.exec(inner, s); err != nil {
			return err
		}
	}
	return nil
}

// blockValue runs a block and returns the value of its final expression statement.
func (th *Thread) blockValue(env *Env, b *ast.Block) (Value, error) {
	inner := NewEnv(env)
	for i, s := range b.Stmts {
		if i == len(b.Stmts)-1 {
			if es, ok := s.(*ast.ExprStmt); ok {
				if err := th.step(es.Pos); err != nil {
					return nil, err
				}
				return th.eval(inner, es.X)
			}
		}
		if err := th.exec(inner, s); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (th *Thread) exec(env *Env, s ast.Stmt) error {
	if err := th.step(s.P()); err != nil {
		return err
	}
	switch s := s.(type) {
	case *ast.ExprStmt:
		_, err := th.eval(env, s.X)
		return err
	case *ast.Let:
		v, err := th.eval(env, s.Value)
		if err != nil {
			return err
		}
		if err := th.letCheck(s, v); err != nil {
			return err
		}
		env.Define(s.Name, v, s.Mutable)
		return nil
	case *ast.Assign:
		return th.assign(env, s)
	case *ast.Return:
		var v Value
		if s.Value != nil {
			var err error
			if v, err = th.eval(env, s.Value); err != nil {
				return err
			}
		}
		return &returnSig{val: v}
	case *ast.Break:
		return &breakSig{}
	case *ast.Continue:
		return &continueSig{}
	case *ast.While:
		for {
			if err := th.step(s.Pos); err != nil {
				return err
			}
			c, err := th.eval(env, s.Cond)
			if err != nil {
				return err
			}
			b, ok := c.(bool)
			if !ok {
				return th.panicAt(s.Cond.P(), PType, "conditions must be Bool (no truthiness)", "while condition is %s, not Bool", TypeName(c))
			}
			if !b {
				return nil
			}
			if err := th.execBlock(env, s.Body); err != nil {
				if _, ok := err.(*breakSig); ok {
					return nil
				}
				if _, ok := err.(*continueSig); ok {
					continue
				}
				return err
			}
		}
	case *ast.For:
		return th.execFor(env, s)
	case *ast.Fail:
		v, err := th.eval(env, s.Value)
		if err != nil {
			return err
		}
		return th.failWith(s, v)
	case *ast.Assert:
		return th.execAssert(env, s)
	case *ast.Defer:
		fnv, pos, named, err := th.evalCallParts(env, s.Call)
		if err != nil {
			return err
		}
		callPos := s.Call.Pos
		fr := th.top()
		fr.defers = append(fr.defers, func() error {
			_, err := th.callValue(fnv, pos, named, callPos)
			return err
		})
		return nil
	}
	return th.panicAt(s.P(), PInternal, "", "unknown statement %T", s)
}

func (th *Thread) execFor(env *Env, s *ast.For) error {
	it, err := th.eval(env, s.Iter)
	if err != nil {
		return err
	}
	body := func(k, v Value) (stop bool, err error) {
		if err := th.step(s.Pos); err != nil {
			return true, err
		}
		inner := NewEnv(env)
		if s.Key != "" {
			inner.Define(s.Key, k, false)
		}
		inner.Define(s.Val, v, false)
		err = th.execBlock(inner, s.Body)
		if err != nil {
			if _, ok := err.(*breakSig); ok {
				return true, nil
			}
			if _, ok := err.(*continueSig); ok {
				return false, nil
			}
			return true, err
		}
		return false, nil
	}
	switch c := it.(type) {
	case *RangeVal:
		var i int64
		for n := c.Lo; n < c.End(); n++ {
			if stop, err := body(i, n); stop || err != nil {
				return err
			}
			i++
		}
		return nil
	case *List:
		for i, v := range c.Snapshot() {
			if stop, err := body(int64(i), v); stop || err != nil {
				return err
			}
		}
		return nil
	case *Map:
		ks, vs := c.Items()
		for i := range ks {
			var stop bool
			var err error
			if s.Key != "" {
				stop, err = body(ks[i], vs[i])
			} else {
				stop, err = body(nil, ks[i])
			}
			if stop || err != nil {
				return err
			}
		}
		return nil
	case string:
		i := int64(0)
		for _, r := range c {
			if stop, err := body(i, string(r)); stop || err != nil {
				return err
			}
			i++
		}
		return nil
	case *Chan:
		i := int64(0)
		for v := range c.ch {
			if stop, err := body(i, v); stop || err != nil {
				return err
			}
			i++
		}
		return nil
	}
	return th.panicAt(s.Iter.P(), PType, "iterate over List, Map, Str, Range (a..b) or Chan", "cannot iterate over %s", TypeName(it))
}

func (th *Thread) execAssert(env *Env, s *ast.Assert) error {
	var ok bool
	values := map[string]string{}
	cond := s.Cond
	for {
		p, isParen := cond.(*ast.Paren)
		if !isParen {
			break
		}
		cond = p.X
	}
	if b := assertBinary(s); b != nil {
		x, err := th.eval(env, b.X)
		if err != nil {
			return err
		}
		y, err := th.eval(env, b.Y)
		if err != nil {
			return err
		}
		if ok, err = th.assertBinaryValues(b, x, y, values); err != nil {
			return err
		}
	} else {
		v, err := th.eval(env, s.Cond)
		if err != nil {
			return err
		}
		if ok, err = th.assertCond(s, v); err != nil {
			return err
		}
		if !ok {
			th.collectIdents(env, s.Cond, values)
		}
	}
	if ok {
		return nil
	}
	var msg Value
	if s.Msg != nil {
		m, err := th.eval(env, s.Msg)
		if err != nil {
			return err
		}
		msg = m
	}
	return th.assertPanic(s, values, msg)
}

// assertBinary returns the comparison of an assert whose operands are
// reported on failure (nil for other conditions).
func assertBinary(s *ast.Assert) *ast.Binary {
	cond := s.Cond
	for {
		p, isParen := cond.(*ast.Paren)
		if !isParen {
			break
		}
		cond = p.X
	}
	if b, isBin := cond.(*ast.Binary); isBin && b.Op != "and" && b.Op != "or" && b.Op != "??" {
		return b
	}
	return nil
}

func (th *Thread) assertBinaryValues(b *ast.Binary, x, y Value, values map[string]string) (bool, error) {
	r, err := th.binaryValues(b, x, y)
	if err != nil {
		return false, err
	}
	ok, _ := r.(bool)
	values[printer.Expr(b.X)] = Repr(x)
	values[printer.Expr(b.Y)] = Repr(y)
	return ok, nil
}

func (th *Thread) assertCond(s *ast.Assert, v Value) (bool, error) {
	b, isBool := v.(bool)
	if !isBool {
		return false, th.panicAt(s.Pos, PType, "", "assert condition is %s, not Bool", TypeName(v))
	}
	return b, nil
}

// assertPanic builds the failed-assertion panic (msg is the evaluated
// message, or nil when the assert has none).
func (th *Thread) assertPanic(s *ast.Assert, values map[string]string, msg Value) error {
	text := "assertion failed: " + printer.Expr(s.Cond)
	if s.Msg != nil {
		text += " (" + Str(msg) + ")"
	}
	for k, v := range values {
		if k == v {
			delete(values, k)
		}
	}
	p := th.panicAt(s.Pos, PAssert, "", "%s", text)
	p.Values = values
	return p
}

// collectIdents records the current values of the identifiers in e.
func (th *Thread) collectIdents(env *Env, e ast.Expr, out map[string]string) {
	collectIdentValues(e, env.Get, out)
}

// collectIdentValues records the values (looked up with get) of the
// identifiers and ident.field selectors in e.
func collectIdentValues(e ast.Expr, get func(string) (Value, bool), out map[string]string) {
	walkExpr(e, func(x ast.Expr) {
		switch x := x.(type) {
		case *ast.Ident:
			if v, ok := get(x.Name); ok {
				switch v.(type) {
				case *Builtin, *Function, *Module, *StructType, *EnumType:
					return
				}
				out[x.Name] = Repr(v)
			}
		case *ast.Selector:
			if id, ok := x.X.(*ast.Ident); ok {
				if v, ok := get(id.Name); ok {
					if s, ok := v.(*Struct); ok {
						if f, ok := s.Field(x.Name); ok {
							out[id.Name+"."+x.Name] = Repr(f)
						}
					}
				}
			}
		}
	})
}

func (th *Thread) assign(env *Env, s *ast.Assign) error {
	val, err := th.eval(env, s.Value)
	if err != nil {
		return err
	}
	compute := func(old Value) (Value, error) { return th.assignCompute(s, old, val) }
	switch t := s.Target.(type) {
	case *ast.Ident:
		var nv Value = val
		if s.Op != "=" {
			old, ok := env.Get(t.Name)
			if !ok {
				return th.panicAt(t.Pos, PName, "", "undefined name '%s'", t.Name)
			}
			if nv, err = compute(old); err != nil {
				return err
			}
		}
		found, mutable := env.Set(t.Name, nv)
		if !found {
			return th.panicAt(t.Pos, PName, "", "undefined name '%s'", t.Name)
		}
		if !mutable {
			return th.panicAt(t.Pos, PType, "declare it with 'var'", "cannot assign to immutable '%s'", t.Name)
		}
		return nil
	case *ast.Selector:
		obj, err := th.eval(env, t.X)
		if err != nil {
			return err
		}
		return th.assignField(s, t, obj, val)
	case *ast.Index:
		obj, err := th.eval(env, t.X)
		if err != nil {
			return err
		}
		key, err := th.eval(env, t.Index)
		if err != nil {
			return err
		}
		return th.assignIndex(s, t, obj, key, val)
	}
	return th.panicAt(s.Pos, PInternal, "", "bad assignment target")
}

// assignCompute returns the value to store for s given the old value.
func (th *Thread) assignCompute(s *ast.Assign, old, val Value) (Value, error) {
	if s.Op == "=" {
		return val, nil
	}
	b := &ast.Binary{Pos: s.Pos, Op: strings.TrimSuffix(s.Op, "=")}
	return th.binaryValues(b, old, val)
}

// assignField performs obj.f (op)= val.
func (th *Thread) assignField(s *ast.Assign, t *ast.Selector, obj, val Value) error {
	compute := func(old Value) (Value, error) { return th.assignCompute(s, old, val) }
	{
		st, ok := obj.(*Struct)
		if !ok {
			return th.panicAt(t.Pos, PType, "", "cannot set field '%s' on %s", t.Name, TypeName(obj))
		}
		old, ok := st.Field(t.Name)
		if !ok {
			return th.panicAt(t.Pos, PName, "", "%s has no field '%s'", st.T.Name, t.Name)
		}
		nv, err := compute(old)
		if err != nil {
			return err
		}
		fi := st.T.Fields[st.T.FieldIdx[t.Name]]
		if !th.typeMatches(nv, fi.Type, st.T.Module, nil) {
			return th.panicAt(t.Pos, PType, "", "field %s.%s has type %s, got %s", st.T.Name, t.Name, printer.Type(fi.Type), TypeName(nv))
		}
		st.SetField(t.Name, nv)
		return nil
	}
}

// assignIndex performs obj[key] (op)= val.
func (th *Thread) assignIndex(s *ast.Assign, t *ast.Index, obj, key, val Value) error {
	var err error
	compute := func(old Value) (Value, error) { return th.assignCompute(s, old, val) }
	{
		switch c := obj.(type) {
		case *List:
			i, ok := key.(int64)
			if !ok {
				return th.panicAt(t.Pos, PType, "", "list index must be Int, got %s", TypeName(key))
			}
			c.mu.RLock()
			n := int64(len(c.items))
			var old Value
			if i >= 0 && i < n {
				old = c.items[i]
			}
			c.mu.RUnlock()
			if i < 0 || i >= n {
				return th.panicAt(t.Pos, PIndex, "use xs.push(v) to append", "index %d out of range for list of length %d", i, n)
			}
			nv, err := compute(old)
			if err != nil {
				return err
			}
			c.mu.Lock()
			if i < int64(len(c.items)) {
				c.items[i] = nv
			}
			c.mu.Unlock()
			return nil
		case *Map:
			if err := th.checkKey(t.Pos, key); err != nil {
				return err
			}
			var nv Value = val
			if s.Op != "=" {
				old, ok := c.Get(key)
				if !ok {
					return th.panicAt(t.Pos, PKey, "initialize the key first, or use m[k] = (m.get(k) ?? 0) + x", "key %s not found", Repr(key))
				}
				if nv, err = compute(old); err != nil {
					return err
				}
			}
			c.Set(key, nv)
			return nil
		}
		return th.panicAt(t.Pos, PType, "", "cannot assign by index to %s", TypeName(obj))
	}
}

func (th *Thread) checkKey(pos ast.Pos, k Value) error {
	switch k.(type) {
	case int64, string, bool, float64:
		return nil
	}
	return th.panicAt(pos, PType, "map keys must be Int, Str, Bool or Float", "invalid map key of type %s", TypeName(k))
}

func (in *Interp) newError(msg, code string, data Value) *Struct {
	return &Struct{T: in.errType, F: []Value{msg, code, data}}
}

// walkExpr visits e and its sub-expressions (not entering function literals).
func walkExpr(e ast.Expr, f func(ast.Expr)) {
	if e == nil {
		return
	}
	f(e)
	switch e := e.(type) {
	case *ast.Unary:
		walkExpr(e.X, f)
	case *ast.Binary:
		walkExpr(e.X, f)
		walkExpr(e.Y, f)
	case *ast.Paren:
		walkExpr(e.X, f)
	case *ast.Call:
		walkExpr(e.Fn, f)
		for _, a := range e.Args {
			walkExpr(a.Value, f)
		}
	case *ast.Index:
		walkExpr(e.X, f)
		walkExpr(e.Index, f)
	case *ast.Selector:
		walkExpr(e.X, f)
	case *ast.ListLit:
		for _, x := range e.Elems {
			walkExpr(x, f)
		}
	case *ast.Range:
		walkExpr(e.Lo, f)
		walkExpr(e.Hi, f)
	case *ast.Try:
		walkExpr(e.X, f)
	case *ast.Catch:
		walkExpr(e.X, f)
	}
}

// failWith raises the failure of a `fail` statement.
func (th *Thread) failWith(s *ast.Fail, v Value) error {
	switch e := v.(type) {
	case string:
		return &Failure{Err: th.in.newError(e, "", nil), Trace: th.trace()}
	case *Struct:
		if e.T == th.in.errType {
			return &Failure{Err: e, Trace: th.trace()}
		}
	}
	return th.panicAt(s.Pos, PType, `write fail error("message", code: "E_CODE")`, "fail requires an Error or Str, got %s", TypeName(v))
}

// letCheck validates the annotated type of a let/var declaration.
func (th *Thread) letCheck(s *ast.Let, v Value) error {
	if s.Type != nil && !th.typeMatches(v, s.Type, th.mod(), nil) {
		return th.panicAt(s.Pos, PType, "", "cannot assign %s to '%s' of type %s", TypeName(v), s.Name, printer.Type(s.Type))
	}
	return nil
}

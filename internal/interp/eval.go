package interp

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

func (th *Thread) eval(env *Env, e ast.Expr) (Value, error) {
	switch e := e.(type) {
	case *ast.IntLit:
		return e.Value, nil
	case *ast.FloatLit:
		return e.Value, nil
	case *ast.BoolLit:
		return e.Value, nil
	case *ast.NilLit:
		return nil, nil
	case *ast.StrLit:
		return th.evalStr(env, e)
	case *ast.Ident:
		v, ok := env.Get(e.Name)
		if !ok {
			return nil, th.panicAt(e.Pos, PName, "", "undefined name '%s'", e.Name)
		}
		return v, nil
	case *ast.Paren:
		return th.eval(env, e.X)
	case *ast.ListLit:
		items := make([]Value, len(e.Elems))
		for i, x := range e.Elems {
			v, err := th.eval(env, x)
			if err != nil {
				return nil, err
			}
			items[i] = v
		}
		return NewList(items), nil
	case *ast.MapLit:
		m := NewMap()
		for _, en := range e.Entries {
			k, err := th.eval(env, en.Key)
			if err != nil {
				return nil, err
			}
			if err := th.checkKey(en.Key.P(), k); err != nil {
				return nil, err
			}
			v, err := th.eval(env, en.Value)
			if err != nil {
				return nil, err
			}
			m.Set(k, v)
		}
		return m, nil
	case *ast.StructLit:
		return th.evalStructLit(env, e)
	case *ast.Unary:
		x, err := th.eval(env, e.X)
		if err != nil {
			return nil, err
		}
		return th.unary(e, x)
	case *ast.Binary:
		return th.evalBinary(env, e)
	case *ast.Range:
		lo, err := th.eval(env, e.Lo)
		if err != nil {
			return nil, err
		}
		hi, err := th.eval(env, e.Hi)
		if err != nil {
			return nil, err
		}
		return th.makeRange(e, lo, hi)
	case *ast.Selector:
		x, err := th.eval(env, e.X)
		if err != nil {
			return nil, err
		}
		return th.selector(e, x)
	case *ast.Index:
		return th.evalIndex(env, e)
	case *ast.Call:
		return th.evalCall(env, e)
	case *ast.FuncLit:
		return th.makeLambda(env, e), nil
	case *ast.If:
		return th.evalIf(env, e)
	case *ast.Match:
		return th.evalMatch(env, e)
	case *ast.Block:
		return th.blockValue(env, e)
	case *ast.Try:
		th.tryDepth++
		v, err := th.eval(env, e.X)
		th.tryDepth--
		return v, err
	case *ast.Catch:
		th.tryDepth++
		v, err := th.eval(env, e.X)
		th.tryDepth--
		if f, ok := err.(*Failure); ok {
			inner := NewEnv(env)
			if e.Name != "" {
				inner.Define(e.Name, f.Err, false)
			}
			return th.blockValue(inner, e.Body)
		}
		return v, err
	case *ast.Spawn:
		return th.evalSpawn(env, e)
	}
	return nil, th.panicAt(e.P(), PInternal, "", "unknown expression %T", e)
}

func (th *Thread) evalStr(env *Env, e *ast.StrLit) (Value, error) {
	if len(e.Parts) == 1 && e.Parts[0].Expr == nil {
		return e.Parts[0].Lit, nil
	}
	var b strings.Builder
	for _, p := range e.Parts {
		if p.Expr == nil {
			b.WriteString(p.Lit)
			continue
		}
		v, err := th.eval(env, p.Expr)
		if err != nil {
			return nil, err
		}
		s, err := th.formatPart(p, v)
		if err != nil {
			return nil, err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// formatPart renders one interpolated value, applying its format spec.
func (th *Thread) formatPart(p ast.StrPart, v Value) (string, error) {
	if p.Format == "" {
		return Str(v), nil
	}
	s, ferr := formatSpec(v, p.Format)
	if ferr != "" {
		return "", th.panicAt(p.Expr.P(), PType, "format spec: [[fill]<|>|^][0][width][.prec][f|e|x|X|b|o|%]", "%s", ferr)
	}
	return s, nil
}

func (th *Thread) makeRange(e *ast.Range, lo, hi Value) (Value, error) {
	l, ok1 := lo.(int64)
	h, ok2 := hi.(int64)
	if !ok1 || !ok2 {
		return nil, th.panicAt(e.Pos, PType, "", "range bounds must be Int, got %s..%s", TypeName(lo), TypeName(hi))
	}
	return &RangeVal{Lo: l, Hi: h, Inclusive: e.Inclusive}, nil
}

func (th *Thread) evalStructLit(env *Env, e *ast.StructLit) (Value, error) {
	tv, err := th.eval(env, e.Type)
	if err != nil {
		return nil, err
	}
	st, err := th.structTypeOf(e, tv)
	if err != nil {
		return nil, err
	}
	vals := make([]Value, len(st.Fields))
	set := make([]bool, len(st.Fields))
	for _, f := range e.Fields {
		i, err := th.structFieldIdx(st, f)
		if err != nil {
			return nil, err
		}
		v, err := th.eval(env, f.Value)
		if err != nil {
			return nil, err
		}
		if err := th.structFieldCheck(st, i, f, v); err != nil {
			return nil, err
		}
		vals[i], set[i] = v, true
	}
	return th.finishStruct(e, st, vals, set)
}

func (th *Thread) structTypeOf(e *ast.StructLit, tv Value) (*StructType, error) {
	st, ok := tv.(*StructType)
	if !ok {
		return nil, th.panicAt(e.Pos, PType, "", "%s is not a struct type", printer.Expr(e.Type))
	}
	return st, nil
}

func (th *Thread) structFieldIdx(st *StructType, f ast.FieldInit) (int, error) {
	i, ok := st.FieldIdx[f.Name]
	if !ok {
		return 0, th.panicAt(f.Pos, PName, suggestHint(f.Name, fieldNames(st)), "%s has no field '%s'", st.Name, f.Name)
	}
	return i, nil
}

func (th *Thread) structFieldCheck(st *StructType, i int, f ast.FieldInit, v Value) error {
	if !th.typeMatches(v, st.Fields[i].Type, st.Module, nil) {
		return th.panicAt(f.Pos, PType, "", "field %s.%s has type %s, got %s", st.Name, f.Name, printer.Type(st.Fields[i].Type), TypeName(v))
	}
	return nil
}

// finishStruct fills defaults and checks missing fields.
func (th *Thread) finishStruct(e *ast.StructLit, st *StructType, vals []Value, set []bool) (Value, error) {
	for i, fi := range st.Fields {
		if set[i] {
			continue
		}
		if fi.Default == nil {
			if fi.Type != nil && fi.Type.Optional {
				continue
			}
			return nil, th.panicAt(e.Pos, PArgs, "", "missing field '%s' in %s literal", fi.Name, st.Name)
		}
		v, err := th.in.evalDetached(st.Module, st.Module.Env, fi.Default)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return &Struct{T: st, F: vals}, nil
}

func fieldNames(st *StructType) []string {
	var out []string
	for _, f := range st.Fields {
		out = append(out, f.Name)
	}
	return out
}

func suggestHint(name string, candidates []string) string {
	if s := diag.Suggest(name, candidates); s != "" {
		return fmt.Sprintf("did you mean '%s'?", s)
	}
	return ""
}

func (th *Thread) evalIf(env *Env, e *ast.If) (Value, error) {
	c, err := th.eval(env, e.Cond)
	if err != nil {
		return nil, err
	}
	b, err := th.ifCond(e, c)
	if err != nil {
		return nil, err
	}
	if b {
		return th.blockValue(env, e.Then)
	}
	if e.Else != nil {
		return th.eval(env, e.Else)
	}
	return nil, nil
}

func (th *Thread) ifCond(e *ast.If, c Value) (bool, error) {
	b, ok := c.(bool)
	if !ok {
		return false, th.panicAt(e.Cond.P(), PType, "conditions must be Bool (no truthiness); e.g. x != nil, not xs.is_empty()", "if condition is %s, not Bool", TypeName(c))
	}
	return b, nil
}

func (th *Thread) guardBool(arm *ast.MatchArm, g Value) (bool, error) {
	gb, isBool := g.(bool)
	if !isBool {
		return false, th.panicAt(arm.Guard.P(), PType, "", "match guard is %s, not Bool", TypeName(g))
	}
	return gb, nil
}

func (th *Thread) noMatch(e *ast.Match, v Value) error {
	return th.panicAt(e.Pos, PMatch, "add a '_ => ...' arm", "no match arm matches %s", Repr(v))
}

func (th *Thread) evalMatch(env *Env, e *ast.Match) (Value, error) {
	v, err := th.eval(env, e.Subject)
	if err != nil {
		return nil, err
	}
	for _, arm := range e.Arms {
		for _, pat := range arm.Patterns {
			binds := map[string]Value{}
			ok, err := th.matchPattern(env, pat, v, binds)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			inner := NewEnv(env)
			for k, bv := range binds {
				inner.Define(k, bv, false)
			}
			if arm.Guard != nil {
				g, err := th.eval(inner, arm.Guard)
				if err != nil {
					return nil, err
				}
				gb, err := th.guardBool(arm, g)
				if err != nil {
					return nil, err
				}
				if !gb {
					continue
				}
			}
			return th.eval(inner, arm.Body)
		}
	}
	return nil, th.noMatch(e, v)
}

func (th *Thread) matchPattern(env *Env, p ast.Pattern, v Value, binds map[string]Value) (bool, error) {
	switch p := p.(type) {
	case *ast.WildcardPat:
		return true, nil
	case *ast.IdentPat:
		if ev, ok := v.(*Enum); ok {
			if vi, ok := ev.V.Enum.ByName[p.Name]; ok {
				return ev.V == vi, nil
			}
		}
		binds[p.Name] = v
		return true, nil
	case *ast.LitPat:
		lv, err := th.eval(env, p.Value)
		if err != nil {
			return false, err
		}
		return Equal(lv, v), nil
	case *ast.RangePat:
		lo, err := th.eval(env, p.Lo)
		if err != nil {
			return false, err
		}
		hi, err := th.eval(env, p.Hi)
		if err != nil {
			return false, err
		}
		c1, ok1 := compare(v, lo)
		c2, ok2 := compare(v, hi)
		if !ok1 || !ok2 {
			return false, nil
		}
		if p.Inclusive {
			return c1 >= 0 && c2 <= 0, nil
		}
		return c1 >= 0 && c2 < 0, nil
	case *ast.VariantPat:
		ev, ok := v.(*Enum)
		if !ok {
			return false, nil
		}
		if p.Enum != "" && ev.V.Enum.Name != p.Enum {
			return false, nil
		}
		if ev.V.Name != p.Variant {
			return false, nil
		}
		if !p.HasArgs {
			return true, nil
		}
		if len(p.Args) != len(ev.F) {
			return false, th.panicAt(p.Pos, PArgs, "", "variant %s has %d fields, pattern has %d", p.Variant, len(ev.F), len(p.Args))
		}
		for i, sp := range p.Args {
			ok, err := th.matchPattern(env, sp, ev.F[i], binds)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	}
	return false, nil
}

func (th *Thread) evalIndex(env *Env, e *ast.Index) (Value, error) {
	x, err := th.eval(env, e.X)
	if err != nil {
		return nil, err
	}
	idx, err := th.eval(env, e.Index)
	if err != nil {
		return nil, err
	}
	return th.indexValue(e, x, idx)
}

// indexValue computes x[idx] (element, key lookup or slice).
func (th *Thread) indexValue(e *ast.Index, x, idx Value) (Value, error) {
	switch c := x.(type) {
	case *List:
		if r, ok := idx.(*RangeVal); ok {
			items := c.Snapshot()
			lo, hi, perr := th.sliceBounds(e.Pos, r, int64(len(items)))
			if perr != nil {
				return nil, perr
			}
			out := make([]Value, hi-lo)
			copy(out, items[lo:hi])
			return NewList(out), nil
		}
		i, ok := idx.(int64)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "", "list index must be Int, got %s", TypeName(idx))
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		if i < 0 || i >= int64(len(c.items)) {
			hint := "check the length first, or use xs.first() / xs.last() which return nil"
			if i < 0 {
				hint = "negative indexes are not allowed; use xs[xs.len() - 1] or xs.last()"
			}
			p := th.panicAt(e.Pos, PIndex, hint, "index %d out of range for list of length %d", i, len(c.items))
			return nil, p
		}
		return c.items[i], nil
	case *Map:
		v, ok := c.Get(idx)
		if !ok {
			return nil, th.panicAt(e.Pos, PKey, "use m.get(k) ?? default, or check m.has(k)", "key %s not found in map", Repr(idx))
		}
		return v, nil
	case string:
		runes := []rune(c)
		if r, ok := idx.(*RangeVal); ok {
			lo, hi, perr := th.sliceBounds(e.Pos, r, int64(len(runes)))
			if perr != nil {
				return nil, perr
			}
			return string(runes[lo:hi]), nil
		}
		i, ok := idx.(int64)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "", "string index must be Int, got %s", TypeName(idx))
		}
		if i < 0 || i >= int64(len(runes)) {
			return nil, th.panicAt(e.Pos, PIndex, "", "index %d out of range for string of length %d", i, len(runes))
		}
		return string(runes[i]), nil
	}
	return nil, th.panicAt(e.Pos, PType, "", "cannot index %s", TypeName(x))
}

func (th *Thread) sliceBounds(pos ast.Pos, r *RangeVal, n int64) (int64, int64, error) {
	lo, hi := r.Lo, r.End()
	if lo < 0 || hi > n || lo > hi {
		return 0, 0, th.panicAt(pos, PIndex, "", "slice %s out of range for length %d", Repr(r), n)
	}
	return lo, hi, nil
}

// ---------- selectors and methods ----------

func (th *Thread) selector(e *ast.Selector, x Value) (Value, error) {
	name := e.Name
	switch v := x.(type) {
	case *Module:
		if m, ok := v.Members[name]; ok {
			return m, nil
		}
		var names []string
		for k := range v.Members {
			names = append(names, k)
		}
		return nil, th.panicAt(e.Pos, PName, suggestHint(name, names), "module %s has no member '%s'", v.Name, name)
	case *Struct:
		if f, ok := v.Field(name); ok {
			return f, nil
		}
		if m, ok := v.T.Methods[name]; ok {
			return &BoundMethod{Recv: v, Fn: m}, nil
		}
		cands := fieldNames(v.T)
		for k := range v.T.Methods {
			cands = append(cands, k)
		}
		return nil, th.panicAt(e.Pos, PName, suggestHint(name, cands), "%s has no field or method '%s'", v.T.Name, name)
	case *Enum:
		if m, ok := v.V.Enum.Methods[name]; ok {
			return &BoundMethod{Recv: v, Fn: m}, nil
		}
		return nil, th.panicAt(e.Pos, PName, "use match to read variant fields", "%s has no method '%s'", v.V.Enum.Name, name)
	case *StructType:
		if m, ok := v.Methods[name]; ok {
			return m, nil
		}
		return nil, th.panicAt(e.Pos, PName, "", "type %s has no method '%s'", v.Name, name)
	case *PyHandle:
		return th.pyMethod(v, name, e.Pos)
	case *EnumType:
		if vi, ok := v.ByName[name]; ok {
			if len(vi.Fields) == 0 {
				return &Enum{V: vi}, nil
			}
			return vi, nil
		}
		if m, ok := v.Methods[name]; ok {
			return m, nil
		}
		var names []string
		for _, vi := range v.Variants {
			names = append(names, vi.Name)
		}
		return nil, th.panicAt(e.Pos, PName, suggestHint(name, names), "enum %s has no variant '%s'", v.Name, name)
	}
	kind := TypeName(x)
	if impls, ok := methodImpls[kind]; ok {
		if tmpl := builtinMethod(kind, name); tmpl != nil {
			b := *tmpl
			b.Recv = x
			return &b, nil
		}
		var names []string
		for k := range impls {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, th.panicAt(e.Pos, PName, suggestHint(name, names), "%s has no method '%s'", kind, name)
	}
	if x == nil {
		return nil, th.panicAt(e.Pos, PNil, "check for nil first (x != nil) or use ??", "cannot access '%s' on nil", name)
	}
	return nil, th.panicAt(e.Pos, PType, "", "%s has no field or method '%s'", kind, name)
}

var (
	methodTmplOnce sync.Once
	methodTmpl     map[string]map[string]*Builtin
)

// builtinMethod returns the template (without receiver) of a method of a
// builtin type, or nil.
func builtinMethod(kind, name string) *Builtin {
	methodTmplOnce.Do(func() {
		methodTmpl = map[string]map[string]*Builtin{}
		core := sig.Get("core")
		for k, impls := range methodImpls {
			methodTmpl[k] = map[string]*Builtin{}
			for n, fn := range impls {
				methodTmpl[k][n] = &Builtin{Name: k + "." + n, Decl: core.Methods[k][n], Fn: fn, Mod: "core"}
			}
		}
	})
	return methodTmpl[kind][name]
}

// ---------- calls ----------

type namedArg struct {
	name string
	val  Value
}

func (th *Thread) evalCallParts(env *Env, c *ast.Call) (Value, []Value, []namedArg, error) {
	fnv, err := th.eval(env, c.Fn)
	if err != nil {
		return nil, nil, nil, err
	}
	var pos []Value
	var named []namedArg
	for _, a := range c.Args {
		v, err := th.eval(env, a.Value)
		if err != nil {
			return nil, nil, nil, err
		}
		if a.Name == "" {
			pos = append(pos, v)
		} else {
			named = append(named, namedArg{a.Name, v})
		}
	}
	return fnv, pos, named, nil
}

func (th *Thread) evalCall(env *Env, c *ast.Call) (Value, error) {
	fnv, pos, named, err := th.evalCallParts(env, c)
	if err != nil {
		return nil, err
	}
	th.top().pos = c.Pos
	v, err := th.callValue(fnv, pos, named, c.Pos)
	return th.finishCall(c, v, err)
}

// finishCall turns an unhandled failure into a panic and gives a
// position to panics raised by builtins.
func (th *Thread) finishCall(c *ast.Call, v Value, err error) (Value, error) {
	if err != nil {
		switch e := err.(type) {
		case *Failure:
			if th.tryDepth == 0 {
				p := th.panicAt(c.Pos, PUnhandled, "handle it: try "+printer.Expr(c)+"  or  "+printer.Expr(c)+" catch e { ... }", "unhandled failure from %s: %s", printer.Expr(c.Fn), e.Error())
				if code := e.Code(); code != "" {
					p.Values = map[string]string{"error.code": code}
				}
				return nil, p
			}
		case *Panic:
			if e.Line == 0 {
				e.File, e.Line, e.Col = th.file(), c.Pos.Line, c.Pos.Col
				e.Trace = th.trace()
			}
		}
	}
	return v, err
}

func (th *Thread) callValue(fnv Value, pos []Value, named []namedArg, at ast.Pos) (Value, error) {
	switch f := fnv.(type) {
	case *Function:
		return th.callFunction(f, nil, pos, named)
	case *BoundMethod:
		return th.callFunction(f.Fn, f.Recv, pos, named)
	case *Builtin:
		return th.callBuiltin(f, pos, named)
	case *VariantInfo:
		params := make([]*ast.Param, len(f.Fields))
		for i, fi := range f.Fields {
			params[i] = &ast.Param{Name: fi.Name, Type: fi.Type, Default: fi.Default}
		}
		vals, err := th.bindArgs(f.Enum.Name+"."+f.Name, params, pos, named, f.Enum.Module.Env)
		if err != nil {
			return nil, err
		}
		for i, fi := range f.Fields {
			if !th.typeMatches(vals[i], fi.Type, f.Enum.Module, nil) {
				return nil, th.panicAt(at, PType, "", "field %s of %s.%s has type %s, got %s", fi.Name, f.Enum.Name, f.Name, printer.Type(fi.Type), TypeName(vals[i]))
			}
		}
		return &Enum{V: f, F: vals}, nil
	case *StructType:
		return nil, th.panicAt(at, PType, fmt.Sprintf("write %s{field: value, ...}", f.Name), "a struct type is not callable")
	}
	return nil, th.panicAt(at, PType, "", "%s is not callable", TypeName(fnv))
}

// bindArgs maps positional and named arguments onto params.
func (th *Thread) bindArgs(fname string, params []*ast.Param, pos []Value, named []namedArg, defEnv *Env) ([]Value, error) {
	vals := make([]Value, len(params))
	set := make([]bool, len(params))
	pi := 0
	for i, p := range params {
		if p.Variadic {
			rest := []Value{}
			if pi < len(pos) {
				rest = append(rest, pos[pi:]...)
				pi = len(pos)
			}
			vals[i], set[i] = NewList(rest), true
			continue
		}
		if pi < len(pos) {
			vals[i], set[i] = pos[pi], true
			pi++
		}
	}
	if pi < len(pos) {
		return nil, &Panic{Code: PArgs, Message: fmt.Sprintf("%s takes %d arguments, got %d", fname, len(params), len(pos)+len(named))}
	}
	for _, a := range named {
		found := false
		for i, p := range params {
			if p.Name == a.name {
				if set[i] {
					return nil, &Panic{Code: PArgs, Message: fmt.Sprintf("argument '%s' of %s given twice", a.name, fname)}
				}
				vals[i], set[i], found = a.val, true, true
				break
			}
		}
		if !found {
			var names []string
			for _, p := range params {
				names = append(names, p.Name)
			}
			return nil, &Panic{Code: PArgs, Message: fmt.Sprintf("%s has no parameter '%s'", fname, a.name), Hint: suggestHint(a.name, names)}
		}
	}
	for i, p := range params {
		if set[i] {
			continue
		}
		if p.Default == nil {
			return nil, &Panic{Code: PArgs, Message: fmt.Sprintf("missing argument '%s' in call to %s", p.Name, fname)}
		}
		v, err := th.eval(defEnv, p.Default)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func (th *Thread) callBuiltin(b *Builtin, pos []Value, named []namedArg) (Value, error) {
	for _, eff := range b.Decl.Uses {
		if !th.in.opt.Allow[eff] {
			return nil, &Panic{Code: PPermission, Message: fmt.Sprintf("capability '%s' not granted (needed by %s)", eff, b.Name), Hint: "run with --allow " + eff}
		}
	}
	vals, err := th.bindArgs(b.Name, b.Decl.Params, pos, named, th.in.universe)
	if err != nil {
		return nil, err
	}
	mod := b.TypeMod
	if mod == nil {
		mod = th.in.std[b.Mod]
	}
	for i, p := range b.Decl.Params {
		if p.Type != nil && !p.Variadic && !nilDefault(p, vals[i]) && !th.typeMatches(vals[i], p.Type, mod, nil) {
			return nil, &Panic{Code: PType, Message: fmt.Sprintf("argument '%s' of %s must be %s, got %s", p.Name, b.Name, printer.Type(p.Type), TypeName(vals[i]))}
		}
	}
	return b.Fn(th, b.Recv, vals)
}

func (th *Thread) callFunction(f *Function, self Value, pos []Value, named []namedArg) (result Value, err error) {
	var vals []Value
	if f.Proto != nil && len(named) == 0 && len(pos) == len(f.Params) && !hasVariadic(f.Params) {
		vals = pos // exactly the positional arguments: nothing to bind
	} else if vals, err = th.bindArgs(f.Name, f.Params, pos, named, f.Mod.Env); err != nil {
		return nil, err
	}
	for i, p := range f.Params {
		if p.Type != nil && !nilDefault(p, vals[i]) && !th.typeMatches(vals[i], p.Type, f.Mod, f.TypeParams) {
			return nil, &Panic{Code: PType, Message: fmt.Sprintf("argument '%s' of %s must be %s, got %s", p.Name, f.Name, printer.Type(p.Type), TypeName(vals[i]))}
		}
	}
	// the environment holds the parameters for interpreted bodies and for
	// contracts (compiled bodies keep them in slots)
	var env *Env
	if f.Proto == nil || len(f.Requires) > 0 || len(f.Ensures) > 0 {
		env = NewEnv(f.Env)
		if f.HasSelf {
			env.Define("self", self, false)
		}
		for i, p := range f.Params {
			env.Define(p.Name, vals[i], false)
		}
	}
	fr := &frame{name: f.Name, mod: f.Mod, pos: f.Pos}
	th.frames = append(th.frames, fr)
	saveTry := th.tryDepth
	th.tryDepth = 0
	defer func() {
		th.tryDepth = saveTry
		th.frames = th.frames[:len(th.frames)-1]
	}()

	for _, r := range f.Requires {
		if err := th.checkContract(env, r, "precondition"); err != nil {
			return nil, err
		}
	}
	if f.Proto != nil {
		args := vals
		if f.HasSelf {
			args = append([]Value{self}, vals...)
		}
		result, err = th.runProto(f, f.Proto, args)
		th.tryDepth = 0
	} else if f.ExprBody != nil {
		result, err = th.eval(env, f.ExprBody)
	} else if f.Body != nil {
		err = th.execBlock(env, f.Body)
		if rs, ok := err.(*returnSig); ok {
			result, err = rs.val, nil
		}
	}
	for i := len(fr.defers) - 1; i >= 0; i-- {
		if derr := fr.defers[i](); derr != nil && err == nil {
			err = derr
		}
	}
	if err != nil {
		switch e := err.(type) {
		case *Failure:
			if !f.Fallible {
				return nil, th.panicAt(fr.pos, PUnhandled, "declare the function fallible with -> !T, or handle the failure", "failure escaped non-fallible function %s: %s", f.Name, e.Error())
			}
		case *breakSig, *continueSig:
			return nil, th.panicAt(fr.pos, PInternal, "", "break/continue outside loop")
		}
		return nil, err
	}
	if f.Ret != nil && !th.typeMatches(result, f.Ret, f.Mod, f.TypeParams) {
		hint := ""
		if result == nil && !f.Ret.Optional {
			hint = "a return statement is missing, or declare the result optional (T?)"
		}
		return nil, th.panicAt(fr.pos, PType, hint, "%s returned %s, expected %s", f.Name, TypeName(result), printer.Type(f.Ret))
	}
	if len(f.Ensures) > 0 {
		env.Define("result", result, false)
		for _, en := range f.Ensures {
			if err := th.checkContract(env, en, "postcondition"); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (th *Thread) checkContract(env *Env, e ast.Expr, kind string) error {
	v, err := th.eval(env, e)
	if err != nil {
		return err
	}
	if b, ok := v.(bool); ok && b {
		return nil
	}
	values := map[string]string{}
	th.collectIdents(env, e, values)
	p := th.panicAt(e.P(), PContract, "", "%s failed in %s: %s", kind, th.top().name, printer.Expr(e))
	p.Values = values
	return p
}

var lambdaFallible sync.Map // *ast.FuncLit -> bool

func (th *Thread) makeLambda(env *Env, e *ast.FuncLit) *Function {
	fallible := e.Fallible
	if !fallible {
		if v, ok := lambdaFallible.Load(e); ok {
			fallible = v.(bool)
		} else {
			fallible = ast.ContainsTryOrFail(e)
			lambdaFallible.Store(e, fallible)
		}
	}
	return &Function{
		Name: "<lambda>", Params: e.Params, Ret: e.Ret, Fallible: fallible,
		Body: e.Body, ExprBody: e.ExprBody, Env: env, Mod: th.mod(), Pos: e.Pos,
	}
}

func (th *Thread) evalSpawn(env *Env, e *ast.Spawn) (Value, error) {
	fnv, pos, named, err := th.evalCallParts(env, e.Call)
	if err != nil {
		return nil, err
	}
	return th.spawnCall(fnv, pos, named, e.Call.Pos), nil
}

// spawnCall runs a call on a new goroutine and returns its Task.
func (th *Thread) spawnCall(fnv Value, pos []Value, named []namedArg, callPos ast.Pos) *Task {
	t := &Task{done: make(chan struct{})}
	mod := th.mod()
	go func() {
		nth := th.in.newThread(mod)
		defer func() {
			if r := recover(); r != nil {
				t.err = &Panic{Code: PInternal, Message: fmt.Sprint("internal error in task: ", r)}
			}
			nth.flushSteps()
			close(t.done)
		}()
		t.val, t.err = nth.callValue(fnv, pos, named, callPos)
	}()
	return t
}

func hasVariadic(params []*ast.Param) bool {
	for _, p := range params {
		if p.Variadic {
			return true
		}
	}
	return false
}

// nilDefault reports whether v is nil for a parameter whose default is nil
// (such a parameter is implicitly optional).
func nilDefault(p *ast.Param, v Value) bool {
	if v != nil {
		return false
	}
	_, ok := p.Default.(*ast.NilLit)
	return ok
}

package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

// ---------- statements ----------

// block checks a block and returns the type of its final expression statement.
func (c *Checker) block(fc *fnCtx, sc *scope, b *ast.Block) *Type {
	inner := newScope(sc, c)
	result := tVoid
	for i, s := range b.Stmts {
		t := c.stmt(fc, inner, s, i == len(b.Stmts)-1)
		if i == len(b.Stmts)-1 {
			result = t
		}
		// `if x == nil { return }` narrows x for the rest of the block
		if es, ok := s.(*ast.ExprStmt); ok {
			if ife, ok := es.X.(*ast.If); ok && ife.Else == nil && terminates(ife.Then.Stmts) {
				c.applyNarrow(fc, inner, nonNilWhen(ife.Cond, false))
			}
		}
		if i < len(b.Stmts)-1 && terminates([]ast.Stmt{s}) {
			c.warnf("W0905", b.Stmts[i+1].P(), "remove it", "unreachable code")
		}
	}
	inner.close()
	return result
}

func (c *Checker) stmt(fc *fnCtx, sc *scope, s ast.Stmt, last bool) *Type {
	switch s := s.(type) {
	case *ast.Let:
		var want *Type
		if s.Type != nil {
			want = c.resolveType(s.Type, fc.tps)
		}
		t := c.expr(fc, sc, s.Value, want)
		if t.K == KVoid {
			c.errorf("E0301", s.Value.P(), "", "%s has no value", printer.Expr(s.Value))
			t = tAny
		}
		if want != nil {
			if !assignable(t, want) {
				c.mismatch(s.Value.P(), t, want, fmt.Sprintf("in declaration of '%s'", s.Name))
			}
			t = want
		} else if t.K == KNil {
			c.errorf("E0301", s.Value.P(), fmt.Sprintf("annotate the type: %s %s: T? = nil", letWord(s.Mutable), s.Name), "cannot infer the type of '%s' from nil", s.Name)
			t = tAny
		}
		c.checkShadow(sc, s.Name, s.Pos)
		sc.declare(s.Name, t, s.Mutable, s.Pos, "local")
		if v := sc.vars[s.Name]; v != nil {
			v.declKw = letWord(s.Mutable)
		}
	case *ast.Assign:
		c.assign(fc, sc, s)
	case *ast.ExprStmt:
		t := c.expr(fc, sc, s.X, nil)
		switch x := s.X.(type) {
		case *ast.Binary:
			hint := ""
			if x.Op == "==" {
				hint = "to assign use '='"
			}
			c.warnf("W0907", s.Pos, hint, "result of '%s' is not used", x.Op)
		case *ast.Ident, *ast.IntLit, *ast.StrLit, *ast.FloatLit, *ast.BoolLit, *ast.Selector, *ast.Index, *ast.ListLit, *ast.MapLit:
			if !last {
				c.warnf("W0907", s.Pos, "", "expression value is not used")
			}
		}
		return t
	case *ast.Return:
		c.ret(fc, sc, s)
	case *ast.Break, *ast.Continue:
		if fc.loop == 0 {
			c.errorf("E0901", s.P(), "", "break/continue outside of a loop")
		}
	case *ast.For:
		c.forStmt(fc, sc, s)
	case *ast.While:
		c.cond(fc, sc, s.Cond, "while")
		body := newScope(sc, c)
		c.applyNarrow(fc, body, nonNilWhen(s.Cond, true))
		fc.loop++
		c.block(fc, body, s.Body)
		fc.loop--
	case *ast.Fail:
		if !fc.fallible && !fc.isTest {
			c.errorf("E0403", s.Pos, c.fallibleHint(fc), "'fail' in %s, which is not declared fallible", fc.name)
		}
		t := c.expr(fc, sc, s.Value, nil)
		if !isUnknown(t) && t.K != KStr && !(t.K == KStruct && t.Struct == c.core.structs["Error"]) {
			c.errorf("E0405", s.Value.P(), `write fail error("message", code: "E_CODE")`, "fail needs an Error or a Str, got %s", t)
		}
	case *ast.Assert:
		c.cond(fc, sc, s.Cond, "assert")
		if s.Msg != nil {
			c.expr(fc, sc, s.Msg, nil)
		}
	case *ast.Defer:
		c.expr(fc, sc, s.Call, nil)
	}
	return tVoid
}

func letWord(mut bool) string {
	if mut {
		return "var"
	}
	return "let"
}

func (c *Checker) fallibleHint(fc *fnCtx) string {
	if fc.isLambda {
		return "declare the lambda fallible: fn(x) -> !T { ... }"
	}
	if fc.fi != nil {
		ret := ""
		if fc.fi.ret.K != KVoid {
			ret = fc.fi.ret.String()
		}
		return fmt.Sprintf("declare it fallible: fn %s(...) -> !%s, or handle the error with catch", fc.fi.decl.Name, ret)
	}
	return "handle the error with catch"
}

func (c *Checker) ret(fc *fnCtx, sc *scope, s *ast.Return) {
	if fc.moduleInit {
		c.errorf("E0308", s.Pos, "", "return outside of a function")
		return
	}
	if fc.inferRet {
		if s.Value == nil {
			fc.rets = append(fc.rets, tVoid)
		} else {
			fc.rets = append(fc.rets, c.expr(fc, sc, s.Value, nil))
		}
		return
	}
	if s.Value == nil {
		if fc.ret.K != KVoid {
			c.errorf("E0308", s.Pos, "", "%s must return a %s value", fc.name, fc.ret)
		}
		return
	}
	t := c.expr(fc, sc, s.Value, fc.ret)
	if fc.ret.K == KVoid {
		if !fc.isTest {
			c.errorf("E0308", s.Pos, fmt.Sprintf("declare the result type: -> %s", t), "%s has no result type but returns a value", fc.name)
		}
		return
	}
	if !assignable(t, fc.ret) {
		c.mismatch(s.Value.P(), t, fc.ret, "in return of "+fc.name)
	}
}

func (c *Checker) mismatch(pos ast.Pos, got, want *Type, where string) {
	hint := ""
	switch {
	case got.K == KOpt && assignable(got.Elem, want):
		hint = "the value may be nil: check it (if x != nil) or use x ?? default"
	case got.K == KInt && want.K == KFloat:
		hint = "no implicit conversions: use float(x)"
	case got.K == KFloat && want.K == KInt:
		hint = "no implicit conversions: use int(x) (truncates) or math.round(x)"
	case got.K == KStr && (want.K == KInt || want.K == KFloat):
		hint = "parse it: try parse_int(s) / try parse_float(s)"
	case want.K == KStr:
		hint = "convert with str(x) or interpolate \"${x}\""
	case got.K == KFn && want.K == KFn && got.Fallible && !want.Fallible:
		hint = "the function can fail but a non-fallible function is expected"
	}
	c.errorf("E0301", pos, hint, "type mismatch %s: expected %s, got %s", where, want, got)
}

func (c *Checker) assign(fc *fnCtx, sc *scope, s *ast.Assign) {
	var target *Type
	switch t := s.Target.(type) {
	case *ast.Ident:
		v := sc.lookup(t.Name)
		if v == nil {
			if c.cur.member(t.Name) != nil || c.core.member(t.Name) != nil {
				c.errorf("E0202", t.Pos, "module-level names are constants; keep state in local variables", "cannot assign to '%s'", t.Name)
			} else {
				c.errorf("E0201", t.Pos, didYouMean(t.Name, c.visibleNames(sc)), "undefined name '%s'", t.Name)
			}
			c.expr(fc, sc, s.Value, nil)
			return
		}
		if !v.mutable {
			hint := fmt.Sprintf("declare it with 'var %s = ...'", t.Name)
			if v.kind == "param" {
				hint = fmt.Sprintf("parameters are immutable; copy it first: var %s2 = %s", t.Name, t.Name)
			}
			c.errorf("E0202", t.Pos, hint, "cannot assign to immutable '%s'", t.Name)
			if v.kind == "local" && v.declKw == "let" {
				c.fix(v.pos, 3, "var", true)
			}
		}
		target = v.typ
		if s.Op != "=" {
			v.used = true
		}
	case *ast.Selector:
		xt := c.expr(fc, sc, t.X, nil)
		target = c.fieldType(fc, sc, t, xt, true)
	case *ast.Index:
		xt := c.expr(fc, sc, t.X, nil)
		switch xt.K {
		case KList:
			it := c.expr(fc, sc, t.Index, tInt)
			if !isUnknown(it) && it.K != KInt {
				c.errorf("E0301", t.Index.P(), "", "list index must be Int, got %s", it)
			}
			target = xt.Elem
		case KMap:
			kt := c.expr(fc, sc, t.Index, xt.Key)
			if !assignable(kt, xt.Key) {
				c.mismatch(t.Index.P(), kt, xt.Key, "for map key")
			}
			target = xt.Val
		case KStr:
			c.errorf("E0301", t.Pos, "build a new string instead", "strings are immutable")
			return
		case KOpt:
			c.errorf("E0310", t.Pos, nilHint(t.X), "cannot index %s: the value may be nil", xt)
			return
		default:
			c.expr(fc, sc, t.Index, nil)
			if !isUnknown(xt) {
				c.errorf("E0301", t.Pos, "", "cannot assign by index to %s", xt)
			}
			target = tAny
		}
	}
	vt := c.expr(fc, sc, s.Value, target)
	if target == nil {
		return
	}
	if s.Op == "=" {
		if !assignable(vt, target) {
			c.mismatch(s.Value.P(), vt, target, "in assignment")
		}
		return
	}
	res := c.arith(s.Pos, strings.TrimSuffix(s.Op, "="), target, vt)
	if !assignable(res, target) {
		c.mismatch(s.Pos, res, target, "in assignment")
	}
}

func (c *Checker) forStmt(fc *fnCtx, sc *scope, s *ast.For) {
	it := c.expr(fc, sc, s.Iter, nil)
	var kt, vt *Type
	switch it.K {
	case KList:
		kt, vt = tInt, it.Elem
	case KMap:
		if s.Key != "" {
			kt, vt = it.Key, it.Val
		} else {
			vt = it.Key
		}
	case KRange:
		kt, vt = tInt, tInt
	case KStr:
		kt, vt = tInt, tStr
	case KChan:
		kt, vt = tInt, it.Elem
	case KAny, KParam:
		kt, vt = tAny, tAny
	case KOpt:
		c.errorf("E0310", s.Iter.P(), nilHint(s.Iter), "cannot iterate over %s: the value may be nil", it)
		kt, vt = tAny, tAny
	default:
		c.errorf("E0301", s.Iter.P(), "iterate over List, Map, Str, Range (a..b) or Chan", "cannot iterate over %s", it)
		kt, vt = tAny, tAny
	}
	body := newScope(sc, c)
	if s.Key != "" {
		c.checkShadow(sc, s.Key, s.Pos)
		body.declare(s.Key, kt, false, s.Pos, "local")
	}
	c.checkShadow(sc, s.Val, s.Pos)
	body.declare(s.Val, vt, false, s.Pos, "local")
	fc.loop++
	c.block(fc, body, s.Body)
	fc.loop--
	body.close()
}

// ---------- expressions ----------

func (c *Checker) expr(fc *fnCtx, sc *scope, e ast.Expr, want *Type) *Type {
	t := c.expr1(fc, sc, e, want)
	if t == nil {
		return tAny
	}
	return t
}

func (c *Checker) expr1(fc *fnCtx, sc *scope, e ast.Expr, want *Type) *Type {
	switch e := e.(type) {
	case *ast.IntLit:
		return tInt
	case *ast.FloatLit:
		return tFloat
	case *ast.BoolLit:
		return tBool
	case *ast.NilLit:
		return tNil
	case *ast.StrLit:
		for _, p := range e.Parts {
			if p.Expr != nil {
				t := c.expr(fc, sc, p.Expr, nil)
				if t.K == KVoid {
					c.errorf("E0301", p.Expr.P(), "", "interpolated expression has no value")
				}
			}
		}
		return tStr
	case *ast.Ident:
		return c.ident(fc, sc, e)
	case *ast.Paren:
		return c.expr(fc, sc, e.X, want)
	case *ast.ListLit:
		var elemWant *Type
		if want != nil && want.K == KList {
			elemWant = want.Elem
		}
		var et *Type
		for _, x := range e.Elems {
			t := c.expr(fc, sc, x, elemWant)
			if elemWant != nil && !assignable(t, elemWant) {
				c.mismatch(x.P(), t, elemWant, "for list element")
			}
			if et == nil {
				et = t
			} else {
				et = join(et, t)
			}
		}
		if elemWant != nil {
			return listOf(elemWant)
		}
		if et == nil {
			et = tAny
		}
		return listOf(et)
	case *ast.MapLit:
		var kw, vw *Type
		if want != nil && want.K == KMap {
			kw, vw = want.Key, want.Val
		}
		var kt, vt *Type
		for _, en := range e.Entries {
			k := c.expr(fc, sc, en.Key, kw)
			if !isUnknown(k) && k.K != KStr && k.K != KInt && k.K != KBool && k.K != KFloat {
				c.errorf("E0301", en.Key.P(), "map keys must be Int, Str, Bool or Float", "invalid map key type %s", k)
			}
			v := c.expr(fc, sc, en.Value, vw)
			if vw != nil && !assignable(v, vw) {
				c.mismatch(en.Value.P(), v, vw, "for map value")
			}
			kt, vt = join(kt, k), join(vt, v)
		}
		if kw != nil {
			return mapOf(kw, vw)
		}
		if kt == nil {
			kt, vt = tAny, tAny
		}
		return mapOf(kt, vt)
	case *ast.StructLit:
		return c.structLit(fc, sc, e)
	case *ast.Unary:
		t := c.expr(fc, sc, e.X, nil)
		if e.Op == "not" {
			if !isUnknown(t) && t.K != KBool {
				c.errorf("E0303", e.Pos, "'not' needs a Bool (no truthiness): e.g. not xs.is_empty(), x == nil", "cannot apply 'not' to %s", t)
			}
			return tBool
		}
		if t.K == KOpt {
			c.errorf("E0310", e.Pos, nilHint(e.X), "cannot negate %s: the value may be nil", t)
			return tAny
		}
		if !isUnknown(t) && t.K != KInt && t.K != KFloat {
			c.errorf("E0302", e.Pos, "", "cannot negate %s", t)
		}
		return t
	case *ast.Binary:
		return c.binary(fc, sc, e)
	case *ast.Range:
		for _, x := range []ast.Expr{e.Lo, e.Hi} {
			t := c.expr(fc, sc, x, tInt)
			if !isUnknown(t) && t.K != KInt {
				c.errorf("E0301", x.P(), "", "range bounds must be Int, got %s", t)
			}
		}
		return tRange
	case *ast.Selector:
		xt := c.expr(fc, sc, e.X, nil)
		return c.fieldType(fc, sc, e, xt, false)
	case *ast.Index:
		return c.index(fc, sc, e)
	case *ast.Call:
		return c.call(fc, sc, e, false)
	case *ast.FuncLit:
		return c.lambda(fc, sc, e, want)
	case *ast.If:
		c.cond(fc, sc, e.Cond, "if")
		then := newScope(sc, c)
		c.applyNarrow(fc, then, nonNilWhen(e.Cond, true))
		tt := c.block(fc, then, e.Then)
		if terminates(e.Then.Stmts) {
			tt = nil
		}
		if e.Else == nil {
			return tVoid
		}
		els := newScope(sc, c)
		c.applyNarrow(fc, els, nonNilWhen(e.Cond, false))
		var et *Type
		if b, ok := e.Else.(*ast.Block); ok {
			et = c.block(fc, els, b)
			if terminates(b.Stmts) {
				et = nil
			}
		} else {
			et = c.expr(fc, els, e.Else, want)
			if exprTerminates(e.Else) {
				et = nil
			}
		}
		r := join(tt, et)
		if r == nil {
			return tVoid
		}
		return r
	case *ast.Match:
		return c.match(fc, sc, e, want)
	case *ast.Block:
		return c.block(fc, sc, e)
	case *ast.Try:
		if !fc.fallible && !fc.isTest {
			c.errorf("E0402", e.Pos, c.fallibleHint(fc), "'try' in %s, which is not declared fallible (try propagates the error to the caller)", fc.name)
		}
		fc.try++
		before := c.fallibleCalls
		t := c.expr(fc, sc, e.X, want)
		fc.try--
		if c.fallibleCalls == before {
			c.warnf("W0404", e.Pos, "remove 'try'", "nothing in this expression can fail")
			c.fix(e.Pos, 4, "", true)
		}
		return t
	case *ast.Catch:
		fc.try++
		before := c.fallibleCalls
		t := c.expr(fc, sc, e.X, want)
		fc.try--
		if c.fallibleCalls == before {
			c.warnf("W0404", e.Pos, "remove 'catch'", "nothing in this expression can fail")
		}
		inner := newScope(sc, c)
		if e.Name != "" {
			c.checkShadow(sc, e.Name, e.Pos)
			inner.declare(e.Name, &Type{K: KStruct, Struct: c.core.structs["Error"]}, false, e.Pos, "param")
		}
		bt := c.block(fc, inner, e.Body)
		if terminates(e.Body.Stmts) {
			return t
		}
		if t.K != KVoid && bt.K != KVoid && !assignable(bt, t) && !assignable(t, bt) {
			c.errorf("E0301", e.Body.Pos, "the catch block must produce the same type as the expression, or return/fail", "catch block produces %s, expected %s", bt, t)
		}
		return join(t, bt)
	case *ast.Spawn:
		fc.try++ // a failure of the spawned call is delivered by task.wait()
		t := c.call(fc, sc, e.Call, true)
		fc.try--
		if t.K == KVoid {
			t = tNil
		}
		return taskOf(t)
	}
	return tAny
}

func nilHint(e ast.Expr) string {
	s := printer.Expr(e)
	return fmt.Sprintf("check it first (if %s != nil { ... }) or give a default (%s ?? value)", s, s)
}

func (c *Checker) ident(fc *fnCtx, sc *scope, e *ast.Ident) *Type {
	if v := sc.lookup(e.Name); v != nil {
		v.used = true
		if nt := sc.narrowed(e.Name); nt != nil {
			return nt
		}
		return v.typ
	}
	if t := c.cur.member(e.Name); t != nil {
		return t
	}
	if im, ok := c.cur.imports[e.Name]; ok {
		c.cur.usedImp[e.Name] = true
		return &Type{K: KModule, Mod: im}
	}
	if t := c.core.member(e.Name); t != nil {
		return t
	}
	if e.Name == "self" {
		c.errorf("E0903", e.Pos, "", "'self' is only available in methods declared with a self parameter")
		return tAny
	}
	if e.Name == "result" {
		c.errorf("E0201", e.Pos, "", "'result' is only available in ensures clauses")
		return tAny
	}
	if sig.Get(e.Name) != nil && e.Name != "core" {
		c.errorf("E0208", e.Pos, fmt.Sprintf("add at the top of the file: import \"%s\"", e.Name), "module '%s' is not imported", e.Name)
		c.fix(ast.Pos{Line: 1, Col: 1}, 0, fmt.Sprintf("import \"%s\"\n", e.Name), true)
		return tAny
	}
	hint := didYouMean(e.Name, c.visibleNames(sc))
	c.errorf("E0201", e.Pos, hint, "undefined name '%s'", e.Name)
	if s := suggestion(hint); s != "" {
		c.fix(e.Pos, len([]rune(e.Name)), s, false)
	}
	return tAny
}

func (c *Checker) structLit(fc *fnCtx, sc *scope, e *ast.StructLit) *Type {
	tt := c.expr(fc, sc, e.Type, nil)
	if tt.K != KTypeVal || tt.Elem.K != KStruct {
		if !isUnknown(tt) {
			c.errorf("E0301", e.Pos, "", "%s is not a struct type", printer.Expr(e.Type))
		}
		for _, f := range e.Fields {
			c.expr(fc, sc, f.Value, nil)
		}
		return tAny
	}
	si := tt.Elem.Struct
	seen := map[string]bool{}
	var names []string
	for _, f := range si.fields {
		names = append(names, f.name)
	}
	for _, f := range e.Fields {
		fi := si.field(f.Name)
		if fi == nil {
			c.errorf("E0601", f.Pos, didYouMean(f.Name, names), "%s has no field '%s' (fields: %s)", si.name, f.Name, strings.Join(names, ", "))
			c.expr(fc, sc, f.Value, nil)
			continue
		}
		if seen[f.Name] {
			c.errorf("E0601", f.Pos, "", "field '%s' given twice", f.Name)
		}
		seen[f.Name] = true
		vt := c.expr(fc, sc, f.Value, fi.typ)
		if !assignable(vt, fi.typ) {
			c.mismatch(f.Value.P(), vt, fi.typ, fmt.Sprintf("for field %s.%s", si.name, f.Name))
		}
	}
	var missing []string
	for _, f := range si.fields {
		if !seen[f.name] && !f.hasDefault {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		c.errorf("E0602", e.Pos, "add: "+strings.Join(missing, ": ..., ")+": ...", "missing field(s) in %s literal: %s", si.name, strings.Join(missing, ", "))
	}
	return tt.Elem
}

// fieldType types x.name (not called). assign=true for assignment targets.
func (c *Checker) fieldType(fc *fnCtx, sc *scope, e *ast.Selector, xt *Type, assign bool) *Type {
	if key := narrowKey(e); key != "" && !assign {
		if nt := sc.narrowed(key); nt != nil {
			c.fieldType1(fc, sc, e, xt, assign) // still validate the base
			return nt
		}
	}
	return c.fieldType1(fc, sc, e, xt, assign)
}

func (c *Checker) fieldType1(fc *fnCtx, sc *scope, e *ast.Selector, xt *Type, assign bool) *Type {
	name := e.Name
	switch xt.K {
	case KAny, KParam:
		return tAny
	case KModule:
		m := xt.Mod
		if t := m.member(name); t != nil {
			if assign {
				c.errorf("E0202", e.Pos, "", "cannot assign to module member %s.%s", m.name, name)
			}
			return t
		}
		c.errorf("E0205", e.Pos, didYouMean(name, m.memberNames()), "module %s has no member '%s'", m.name, name)
		c.fixSuggestion(e.Pos, name)
		return tAny
	case KStruct:
		si := xt.Struct
		if f := si.field(name); f != nil {
			return f.typ
		}
		if m, ok := si.methods[name]; ok {
			if assign {
				c.errorf("E0202", e.Pos, "", "cannot assign to method %s", name)
			}
			return boundType(m, nil)
		}
		c.errorf("E0204", e.Pos, didYouMean(name, structMembers(si)), "%s has no field or method '%s'", si.name, name)
		c.fixSuggestion(e.Pos, name)
		return tAny
	case KEnum:
		if m, ok := xt.Enum.methods[name]; ok {
			return boundType(m, nil)
		}
		c.errorf("E0204", e.Pos, "read variant fields with match", "%s has no method '%s'", xt.Enum.name, name)
		return tAny
	case KTypeVal:
		switch xt.Elem.K {
		case KStruct:
			if m, ok := xt.Elem.Struct.methods[name]; ok {
				if m.hasSelf {
					c.errorf("E0204", e.Pos, fmt.Sprintf("call it on a value: x.%s(...)", name), "%s.%s needs a receiver (it takes self)", xt.Elem.Struct.name, name)
				}
				return m.fnType()
			}
			c.errorf("E0204", e.Pos, didYouMean(name, keysF(xt.Elem.Struct.methods)), "type %s has no static method '%s'", xt.Elem.Struct.name, name)
		case KEnum:
			ei := xt.Elem.Enum
			if v := ei.variant(name); v != nil {
				if len(v.fields) == 0 {
					return xt.Elem
				}
				return c.variantFn(ei, v)
			}
			if m, ok := ei.methods[name]; ok {
				return m.fnType()
			}
			var names []string
			for _, v := range ei.variants {
				names = append(names, v.name)
			}
			c.errorf("E0603", e.Pos, didYouMean(name, names), "enum %s has no variant '%s' (variants: %s)", ei.name, name, strings.Join(names, ", "))
		}
		return tAny
	case KOpt, KNil:
		c.errorf("E0310", e.Pos, nilHint(e.X), "cannot access '.%s' on %s: the value may be nil", name, xt)
		return tAny
	}
	if tn := builtinName(xt); tn != "" {
		if m, ok := c.methods[tn][name]; ok {
			if assign {
				c.errorf("E0202", e.Pos, "", "cannot assign to method %s", name)
			}
			return boundType(m, recvBindings(xt))
		}
		hint := didYouMean(name, keysF(c.methods[tn]))
		if hint == "" {
			hint = fmt.Sprintf("%s methods: %s", tn, strings.Join(sortedF(c.methods[tn]), ", "))
		}
		c.errorf("E0204", e.Pos, hint, "%s has no method '%s'", tn, name)
		c.fixSuggestion(e.Pos, name)
		return tAny
	}
	c.errorf("E0204", e.Pos, "", "%s has no field or method '%s'", xt, name)
	return tAny
}

func structMembers(si *structInfo) []string {
	var out []string
	for _, f := range si.fields {
		out = append(out, f.name)
	}
	return append(out, keysF(si.methods)...)
}

func keysF(m map[string]*funcInfo) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedF(m map[string]*funcInfo) []string {
	out := keysF(m)
	sort.Strings(out)
	return out
}

func builtinName(t *Type) string {
	switch t.K {
	case KStr:
		return "Str"
	case KList:
		return "List"
	case KMap:
		return "Map"
	case KRange:
		return "Range"
	case KChan:
		return "Chan"
	case KTask:
		return "Task"
	}
	return ""
}

func recvBindings(t *Type) map[string]*Type {
	b := map[string]*Type{}
	switch t.K {
	case KList, KChan, KTask:
		b["T"] = t.Elem
	case KMap:
		b["K"], b["V"] = t.Key, t.Val
	}
	return b
}

func boundType(m *funcInfo, b map[string]*Type) *Type {
	t := m.fnType()
	if b != nil {
		t = subst(t, b)
		t.Fn = m
	}
	return t
}

func (c *Checker) variantFn(ei *enumInfo, v *variantInfo) *Type {
	t := &Type{K: KFn, Ret: &Type{K: KEnum, Enum: ei}}
	for _, f := range v.fields {
		t.Params = append(t.Params, f.typ)
	}
	return t
}

func (c *Checker) index(fc *fnCtx, sc *scope, e *ast.Index) *Type {
	xt := c.expr(fc, sc, e.X, nil)
	_, isRange := e.Index.(*ast.Range)
	switch xt.K {
	case KList, KStr:
		it := c.expr(fc, sc, e.Index, nil)
		if isRange || it.K == KRange {
			return xt
		}
		if !isUnknown(it) && it.K != KInt {
			c.errorf("E0301", e.Index.P(), "", "index must be Int or a range a..b, got %s", it)
		}
		if xt.K == KStr {
			return tStr
		}
		return xt.Elem
	case KMap:
		kt := c.expr(fc, sc, e.Index, xt.Key)
		if !assignable(kt, xt.Key) {
			c.mismatch(e.Index.P(), kt, xt.Key, "for map key")
		}
		return xt.Val
	case KOpt, KNil:
		c.expr(fc, sc, e.Index, nil)
		c.errorf("E0310", e.Pos, nilHint(e.X), "cannot index %s: the value may be nil", xt)
		return tAny
	case KAny, KParam:
		c.expr(fc, sc, e.Index, nil)
		return tAny
	}
	c.expr(fc, sc, e.Index, nil)
	c.errorf("E0301", e.Pos, "", "cannot index %s", xt)
	return tAny
}

// ---------- operators ----------

func (c *Checker) binary(fc *fnCtx, sc *scope, e *ast.Binary) *Type {
	switch e.Op {
	case "and", "or":
		c.cond(fc, sc, e.X, "'"+e.Op+"' operand")
		right := newScope(sc, c)
		c.applyNarrow(fc, right, nonNilWhen(e.X, e.Op == "and"))
		c.cond(fc, right, e.Y, "'"+e.Op+"' operand")
		return tBool
	case "??":
		xt := c.expr(fc, sc, e.X, nil)
		var inner *Type
		switch xt.K {
		case KOpt:
			inner = xt.Elem
		case KNil:
			inner = tAny
		case KAny, KParam:
			inner = tAny
		default:
			c.warnf("W0302", e.Pos, "remove '?? ...'", "left side of '??' is %s and is never nil", xt)
			inner = xt
		}
		yt := c.expr(fc, sc, e.Y, inner)
		if !assignable(yt, inner) && !isUnknown(inner) {
			if yt.K == KOpt && assignable(yt.Elem, inner) {
				return yt
			}
			c.mismatch(e.Y.P(), yt, inner, "on the right of '??'")
		}
		if isUnknown(inner) {
			return yt
		}
		return inner
	}
	xt := c.expr(fc, sc, e.X, nil)
	yt := c.expr(fc, sc, e.Y, xt)
	switch e.Op {
	case "==", "!=":
		if xt.K == KNil || yt.K == KNil || isUnknown(xt) || isUnknown(yt) {
			return tBool
		}
		a, b := stripOpt(xt), stripOpt(yt)
		if (a.K == KInt && b.K == KFloat) || (a.K == KFloat && b.K == KInt) {
			c.errorf("E0302", e.Pos, "no implicit conversions: compare float(n) with the Float", "cannot compare %s with %s", xt, yt)
		} else if !assignable(a, b) && !assignable(b, a) {
			c.errorf("E0302", e.Pos, "the comparison is always false", "cannot compare %s with %s", xt, yt)
		}
		return tBool
	case "<", "<=", ">", ">=":
		if xt.K == KOpt || yt.K == KOpt {
			c.errorf("E0310", e.Pos, "check for nil first or use ?? default", "cannot order %s and %s: a value may be nil", xt, yt)
			return tBool
		}
		if isUnknown(xt) || isUnknown(yt) {
			return tBool
		}
		if xt.K != yt.K || (xt.K != KInt && xt.K != KFloat && xt.K != KStr && xt.K != KList) {
			hint := ""
			if (xt.K == KInt && yt.K == KFloat) || (xt.K == KFloat && yt.K == KInt) {
				hint = "no implicit conversions: use float(x)"
			}
			c.errorf("E0302", e.Pos, hint, "cannot order %s and %s", xt, yt)
		}
		return tBool
	case "in":
		switch yt.K {
		case KList:
			if !assignable(xt, yt.Elem) {
				c.mismatch(e.X.P(), xt, yt.Elem, "for 'in'")
			}
		case KMap:
			if !assignable(xt, yt.Key) {
				c.mismatch(e.X.P(), xt, yt.Key, "for 'in' (map key)")
			}
		case KStr:
			if !assignable(xt, tStr) {
				c.mismatch(e.X.P(), xt, tStr, "for 'in' on Str")
			}
		case KRange:
			if !assignable(xt, tInt) {
				c.mismatch(e.X.P(), xt, tInt, "for 'in' on Range")
			}
		case KAny, KParam:
		default:
			c.errorf("E0302", e.Pos, "", "'in' is not defined for %s", yt)
		}
		return tBool
	}
	return c.arith(e.Pos, e.Op, xt, yt)
}

func stripOpt(t *Type) *Type {
	if t.K == KOpt {
		return t.Elem
	}
	return t
}

func (c *Checker) arith(pos ast.Pos, op string, xt, yt *Type) *Type {
	if xt.K == KOpt || yt.K == KOpt || xt.K == KNil || yt.K == KNil {
		c.errorf("E0310", pos, "check for nil first or use ?? default", "operator '%s' on %s and %s: a value may be nil", op, xt, yt)
		return tAny
	}
	if isUnknown(xt) && isUnknown(yt) {
		return tAny
	}
	if isUnknown(xt) {
		return yt
	}
	if isUnknown(yt) {
		return xt
	}
	switch {
	case xt.K == KInt && yt.K == KInt, xt.K == KFloat && yt.K == KFloat:
		return xt
	case op == "+" && xt.K == KStr && yt.K == KStr:
		return tStr
	case op == "+" && xt.K == KList && yt.K == KList:
		return listOf(join(xt.Elem, yt.Elem))
	}
	hint := ""
	switch {
	case (xt.K == KInt && yt.K == KFloat) || (xt.K == KFloat && yt.K == KInt):
		hint = "no implicit conversions: use float(n) or int(x)"
	case xt.K == KStr || yt.K == KStr:
		hint = "build strings with interpolation: \"${a}${b}\""
	}
	c.errorf("E0302", pos, hint, "operator '%s' is not defined for %s and %s", op, xt, yt)
	return tAny
}

// ---------- lambdas ----------

func (c *Checker) lambda(fc *fnCtx, sc *scope, e *ast.FuncLit, want *Type) *Type {
	lf := &fnCtx{name: "lambda", parent: fc, isLambda: true, uses: fc.uses, used: fc.used, tps: fc.tps, isTest: fc.isTest}
	lf.fallible = e.Fallible || ast.ContainsTryOrFail(e)
	if e.Ret != nil {
		lf.ret = c.resolveType(e.Ret, fc.tps)
	} else if e.Body != nil {
		lf.inferRet = true
		lf.ret = tAny
	}
	ls := newScope(sc, c)
	t := &Type{K: KFn, Fallible: lf.fallible}
	for i, p := range e.Params {
		var pt *Type
		if p.Type != nil {
			pt = c.resolveType(p.Type, fc.tps)
		} else if want != nil && want.K == KFn && i < len(want.Params) {
			pt = want.Params[i]
		} else {
			pt = tAny
		}
		c.checkShadow(sc, p.Name, p.Pos)
		ls.declare(p.Name, pt, false, p.Pos, "param")
		t.Params = append(t.Params, pt)
	}
	if want != nil && want.K == KFn && len(want.Params) != len(e.Params) && len(want.Params) > 0 {
		c.errorf("E0305", e.Pos, "", "function expected with %d parameter(s), lambda has %d", len(want.Params), len(e.Params))
	}
	if e.ExprBody != nil {
		var rw *Type
		if lf.ret != nil {
			rw = lf.ret
		} else if want != nil && want.K == KFn && want.Ret != nil && want.Ret.K != KVoid {
			rw = want.Ret
		}
		lf.ret = tAny
		bt := c.expr(lf, ls, e.ExprBody, rw)
		if e.Ret != nil {
			if !assignable(bt, lf.ret) {
				c.mismatch(e.ExprBody.P(), bt, lf.ret, "in lambda result")
			}
			t.Ret = c.resolveType(e.Ret, fc.tps)
		} else {
			t.Ret = bt
		}
	} else {
		c.block(lf, ls, e.Body)
		if lf.inferRet {
			var r *Type
			for _, rt := range lf.rets {
				r = join(r, rt)
			}
			if r == nil {
				r = tVoid
			}
			t.Ret = r
		} else {
			t.Ret = lf.ret
			if lf.ret.K != KVoid && !terminates(e.Body.Stmts) {
				c.errorf("E0307", e.Body.End, "add a return statement", "lambda must return %s on every path", lf.ret)
			}
		}
	}
	ls.close()
	return t
}

// ---------- calls ----------

type callee struct {
	name     string
	params   []paramInfo
	named    bool // parameter names are known (P5 rule applies)
	ret      *Type
	fallible bool
	uses     []string
	bind     map[string]*Type
	std      bool
	unknown  bool
}

func (c *Checker) resolveCallee(fc *fnCtx, sc *scope, fnExpr ast.Expr) *callee {
	var ft *Type
	var bind map[string]*Type
	switch f := fnExpr.(type) {
	case *ast.Selector:
		xt := c.expr(fc, sc, f.X, nil)
		ft = c.fieldType(fc, sc, f, xt, false)
		if tn := builtinName(xt); tn != "" {
			bind = recvBindings(xt)
		}
		if xt.K == KTypeVal && xt.Elem.K == KEnum {
			if v := xt.Elem.Enum.variant(f.Name); v != nil && len(v.fields) > 0 {
				cl := &callee{name: xt.Elem.Enum.name + "." + v.name, named: true, ret: ft.Ret}
				for _, fd := range v.fields {
					cl.params = append(cl.params, paramInfo{name: fd.name, typ: fd.typ, hasDefault: fd.hasDefault})
				}
				return cl
			}
		}
	default:
		ft = c.expr(fc, sc, fnExpr, nil)
	}
	switch ft.K {
	case KFn:
		if fi := ft.Fn; fi != nil {
			cl := &callee{name: fi.name, named: true, ret: fi.ret, fallible: fi.fallible, uses: fi.uses, bind: map[string]*Type{}, std: fi.std}
			for k, v := range bind {
				cl.bind[k] = v
			}
			cl.params = fi.params
			return cl
		}
		cl := &callee{name: printer.Expr(fnExpr), ret: ft.Ret, fallible: ft.Fallible}
		for _, p := range ft.Params {
			cl.params = append(cl.params, paramInfo{typ: p})
		}
		if cl.ret == nil {
			cl.ret = tAny
		}
		return cl
	case KAny, KParam:
		return &callee{name: printer.Expr(fnExpr), unknown: true, ret: tAny}
	case KTypeVal:
		if ft.Elem.K == KStruct {
			c.errorf("E0304", fnExpr.P(), fmt.Sprintf("write %s{field: value, ...}", printer.Expr(fnExpr)), "a struct is built with a literal, not called")
		} else if ft.Elem.K == KEnum {
			c.errorf("E0304", fnExpr.P(), fmt.Sprintf("write %s.Variant(...)", printer.Expr(fnExpr)), "an enum type is not callable")
		}
		return &callee{unknown: true, ret: tAny}
	case KOpt:
		c.errorf("E0310", fnExpr.P(), nilHint(fnExpr), "cannot call %s: the value may be nil", ft)
		return &callee{unknown: true, ret: tAny}
	case KEnum:
		c.errorf("E0304", fnExpr.P(), "this variant has no fields: drop the parentheses", "%s is not callable", printer.Expr(fnExpr))
		return &callee{unknown: true, ret: tAny}
	}
	c.errorf("E0304", fnExpr.P(), "", "%s is not callable (it is %s)", printer.Expr(fnExpr), ft)
	return &callee{unknown: true, ret: tAny}
}

// call checks a call; inSpawn relaxes the unhandled-failure rule.
func (c *Checker) call(fc *fnCtx, sc *scope, e *ast.Call, inSpawn bool) *Type {
	cl := c.resolveCallee(fc, sc, e.Fn)
	if cl.unknown {
		for _, a := range e.Args {
			c.expr(fc, sc, a.Value, nil)
		}
		return tAny
	}
	bind := cl.bind
	if bind == nil {
		bind = map[string]*Type{}
	}
	vals := make([]ast.Expr, len(cl.params))
	given := make([]bool, len(cl.params))
	var variadic []ast.Expr
	pi := 0
	fallibleArg := false
	for ai, a := range e.Args {
		if a.Name != "" {
			continue
		}
		if pi < len(cl.params) && cl.params[pi].variadic {
			variadic = append(variadic, a.Value)
			continue
		}
		if pi >= len(cl.params) {
			c.errorf("E0305", a.Pos, fmt.Sprintf("signature: %s", c.sigText(cl)), "too many arguments to %s: expected %d", cl.name, len(cl.params))
			c.expr(fc, sc, a.Value, nil)
			continue
		}
		if ai > 0 && cl.named && cl.params[pi].name != "" {
			c.errorf("E0306", a.Pos, fmt.Sprintf("write %s: %s", cl.params[pi].name, printer.Expr(a.Value)),
				"argument %d of %s must be named (only the first argument is positional)", ai+1, cl.name)
			c.fix(exprStart(a.Value), 0, cl.params[pi].name+": ", true)
		}
		vals[pi], given[pi] = a.Value, true
		pi++
	}
	for _, a := range e.Args {
		if a.Name == "" {
			continue
		}
		found := -1
		for i, p := range cl.params {
			if p.name == a.Name {
				found = i
			}
		}
		if found < 0 {
			var names []string
			for _, p := range cl.params {
				names = append(names, p.name)
			}
			c.errorf("E0305", a.Pos, didYouMean(a.Name, names)+" signature: "+c.sigText(cl), "%s has no parameter '%s'", cl.name, a.Name)
			c.expr(fc, sc, a.Value, nil)
			continue
		}
		if given[found] {
			c.errorf("E0305", a.Pos, "", "argument '%s' of %s is given twice", a.Name, cl.name)
			continue
		}
		vals[found], given[found] = a.Value, true
	}
	for i, p := range cl.params {
		if p.variadic {
			for _, v := range variadic {
				c.expr(fc, sc, v, nil)
			}
			continue
		}
		if !given[i] {
			if !p.hasDefault {
				what := fmt.Sprintf("'%s'", p.name)
				if p.name == "" {
					what = fmt.Sprintf("%d", i+1)
				}
				c.errorf("E0305", e.Pos, "signature: "+c.sigText(cl), "missing argument %s in call to %s", what, cl.name)
			}
			continue
		}
		want := subst(p.typ, bind)
		if p.typ != nil && p.typ.K == KParam {
			want = nil
			if b, ok := bind[p.typ.Name]; ok {
				want = b
			}
		}
		at := c.expr(fc, sc, vals[i], want)
		unify(p.typ, at, bind)
		exp := subst(p.typ, bind)
		if at.K == KFn && at.Fallible && cl.std {
			fallibleArg = true
			at2 := *at
			at2.Fallible = false
			at = &at2
		}
		if at.K == KNil && p.hasDefault {
			continue
		}
		if at.K == KVoid {
			c.errorf("E0301", vals[i].P(), "", "argument '%s' of %s has no value", p.name, cl.name)
			continue
		}
		if !assignable(at, exp) {
			name := p.name
			if name == "" {
				name = fmt.Sprint(i + 1)
			}
			c.mismatch(vals[i].P(), at, exp, fmt.Sprintf("for argument '%s' of %s", name, cl.name))
		}
	}
	fallible := cl.fallible || fallibleArg
	if fallible {
		c.fallibleCalls++
		if fc.try == 0 && !inSpawn {
			call := printer.Expr(e)
			hint := fmt.Sprintf("propagate: try %s  |  handle: %s catch e { ... }", call, call)
			if !fc.fallible && !fc.isTest {
				hint = fmt.Sprintf("handle it: %s catch e { ... }  (or declare %s fallible with -> !T and use try)", call, fc.name)
			}
			c.errorf("E0401", e.Pos, hint, "%s can fail; the error must be handled", cl.name)
			if fc.fallible || fc.isTest {
				c.fix(exprStart(e), 0, "try ", false)
			}
		}
	}
	if len(cl.uses) > 0 {
		c.useEffects(fc, cl.uses, e.Pos)
	}
	r := subst(cl.ret, bind)
	if r == nil {
		return tAny
	}
	// std calls whose result depends on a lambda: List.map etc. already bound U
	return r
}

func (c *Checker) sigText(cl *callee) string {
	var parts []string
	for _, p := range cl.params {
		s := p.name
		if s != "" {
			s += ": "
		}
		s += p.typ.String()
		if p.hasDefault {
			s += " = ..."
		}
		parts = append(parts, s)
	}
	return cl.name + "(" + strings.Join(parts, ", ") + ")"
}

// ---------- match ----------

func (c *Checker) match(fc *fnCtx, sc *scope, e *ast.Match, want *Type) *Type {
	st := c.expr(fc, sc, e.Subject, nil)
	var result *Type
	catchAll := false
	covered := map[string]bool{}
	coveredBool := map[bool]bool{}
	for _, arm := range e.Arms {
		if catchAll {
			c.warnf("W0702", arm.Pos, "remove it or move it before the catch-all arm", "unreachable match arm")
		}
		as := newScope(sc, c)
		armAll := false
		for _, p := range arm.Patterns {
			if c.pattern(fc, as, p, st, covered, coveredBool, arm.Guard == nil) {
				armAll = true
			}
		}
		if arm.Guard != nil {
			c.cond(fc, as, arm.Guard, "match guard")
		} else if armAll {
			catchAll = true
		}
		var bt *Type
		if b, ok := arm.Body.(*ast.Block); ok {
			bt = c.block(fc, as, b)
			if terminates(b.Stmts) {
				bt = nil
			}
		} else {
			bt = c.expr(fc, as, arm.Body, want)
			if exprTerminates(arm.Body) {
				bt = nil
			}
		}
		as.close()
		result = join(result, bt)
	}
	if !catchAll {
		switch {
		case st.K == KEnum:
			var missing []string
			for _, v := range st.Enum.variants {
				if !covered[v.name] {
					missing = append(missing, st.Enum.name+"."+v.name)
				}
			}
			if len(missing) > 0 {
				c.errorf("E0701", e.Pos, "add arms for them, or a final '_ => ...' arm", "match on %s is not exhaustive: missing %s", st.Enum.name, strings.Join(missing, ", "))
			}
		case st.K == KBool:
			if !coveredBool[true] || !coveredBool[false] {
				c.errorf("E0701", e.Pos, "cover true and false, or add '_ => ...'", "match on Bool is not exhaustive")
			}
		default:
			c.errorf("E0701", e.Pos, "add a final '_ => ...' arm", "match on %s is not exhaustive", st)
		}
	}
	if result == nil {
		return tVoid
	}
	return result
}

// pattern checks p against subject type st; returns true if p matches everything.
func (c *Checker) pattern(fc *fnCtx, as *scope, p ast.Pattern, st *Type, covered map[string]bool, coveredBool map[bool]bool, unguarded bool) bool {
	switch p := p.(type) {
	case *ast.WildcardPat:
		return true
	case *ast.IdentPat:
		if st.K == KEnum {
			if v := st.Enum.variant(p.Name); v != nil {
				if len(v.fields) > 0 {
					c.errorf("E0604", p.Pos, fmt.Sprintf("write %s(%s)", p.Name, underscores(len(v.fields))), "variant %s has %d field(s)", p.Name, len(v.fields))
				}
				if unguarded {
					covered[p.Name] = true
				}
				return false
			}
		}
		if len(p.Name) > 0 && p.Name[0] >= 'A' && p.Name[0] <= 'Z' && st.K == KEnum {
			var names []string
			for _, v := range st.Enum.variants {
				names = append(names, v.name)
			}
			c.errorf("E0603", p.Pos, didYouMean(p.Name, names), "%s has no variant '%s'", st.Enum.name, p.Name)
			return false
		}
		c.checkShadow(as.parent, p.Name, p.Pos)
		as.declare(p.Name, st, false, p.Pos, "param")
		return true
	case *ast.LitPat:
		lt := c.expr(fc, as, p.Value, st)
		if lt.K == KNil {
			if st.K != KOpt && !isUnknown(st) {
				c.errorf("E0301", p.Pos, "", "nil pattern on %s, which is never nil", st)
			}
			return false
		}
		if !assignable(lt, stripOpt(st)) {
			c.mismatch(p.Pos, lt, st, "in pattern")
		}
		if b, ok := p.Value.(*ast.BoolLit); ok && unguarded {
			coveredBool[b.Value] = true
		}
		return false
	case *ast.RangePat:
		for _, x := range []ast.Expr{p.Lo, p.Hi} {
			lt := c.expr(fc, as, x, st)
			if !assignable(lt, st) {
				c.mismatch(x.P(), lt, st, "in range pattern")
			}
		}
		return false
	case *ast.VariantPat:
		if isUnknown(st) {
			for _, a := range p.Args {
				c.pattern(fc, as, a, tAny, map[string]bool{}, map[bool]bool{}, false)
			}
			return false
		}
		if st.K != KEnum {
			c.errorf("E0301", p.Pos, "", "variant pattern on %s, which is not an enum", st)
			return false
		}
		ei := st.Enum
		if p.Enum != "" && p.Enum != ei.name {
			c.errorf("E0603", p.Pos, "", "pattern is for enum %s but the value is %s", p.Enum, ei.name)
			return false
		}
		v := ei.variant(p.Variant)
		if v == nil {
			var names []string
			for _, vv := range ei.variants {
				names = append(names, vv.name)
			}
			c.errorf("E0603", p.Pos, didYouMean(p.Variant, names), "%s has no variant '%s'", ei.name, p.Variant)
			return false
		}
		if p.HasArgs && len(p.Args) != len(v.fields) {
			c.errorf("E0604", p.Pos, fmt.Sprintf("write %s(%s)", p.Variant, underscores(len(v.fields))), "variant %s has %d field(s), pattern has %d", p.Variant, len(v.fields), len(p.Args))
			return false
		}
		all := true
		for i, a := range p.Args {
			if !c.pattern(fc, as, a, v.fields[i].typ, map[string]bool{}, map[bool]bool{}, false) {
				all = false
			}
		}
		if all && unguarded {
			covered[v.name] = true
		}
		return false
	}
	return false
}

func underscores(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "_"
	}
	return strings.Join(parts, ", ")
}

// fixSuggestion turns the did-you-mean hint of the last diagnostic into a fix.
func (c *Checker) fixSuggestion(pos ast.Pos, name string) {
	if len(c.diags) == 0 {
		return
	}
	if s := suggestion(c.diags[len(c.diags)-1].Hint); s != "" {
		c.fix(pos, len([]rune(name)), s, false)
	}
}

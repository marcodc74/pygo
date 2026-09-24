package check

import (
	"fmt"

	"github.com/marcodc74/pygo/internal/ast"
)

type varInfo struct {
	name    string
	typ     *Type
	mutable bool
	used    bool
	pos     ast.Pos
	kind    string // "local", "param"
}

type scope struct {
	vars   map[string]*varInfo
	order  []string
	parent *scope
	narrow map[string]*Type // "x" / "u.email" -> non-optional type
	c      *Checker
}

func newScope(parent *scope, c *Checker) *scope {
	return &scope{vars: map[string]*varInfo{}, parent: parent, narrow: map[string]*Type{}, c: c}
}

func (s *scope) lookup(name string) *varInfo {
	for x := s; x != nil; x = x.parent {
		if v, ok := x.vars[name]; ok {
			return v
		}
	}
	return nil
}

func (s *scope) narrowed(key string) *Type {
	for x := s; x != nil; x = x.parent {
		if t, ok := x.narrow[key]; ok {
			return t
		}
		// a narrowing only holds below the scope where the variable is declared
		if _, declared := x.vars[key]; declared {
			return nil
		}
	}
	return nil
}

func (s *scope) declare(name string, t *Type, mutable bool, pos ast.Pos, kind string) {
	if name == "_" {
		return
	}
	s.vars[name] = &varInfo{name: name, typ: t, mutable: mutable, pos: pos, kind: kind}
	s.order = append(s.order, name)
}

// close reports unused local variables.
func (s *scope) close() {
	for _, n := range s.order {
		v := s.vars[n]
		if !v.used && v.kind == "local" && n[0] != '_' {
			s.c.warnf("W0201", v.pos, fmt.Sprintf("remove it, or name it _%s", n), "variable '%s' is never used", n)
		}
	}
}

// visibleNames lists every name visible from s (for suggestions).
func (c *Checker) visibleNames(s *scope) []string {
	var out []string
	for x := s; x != nil; x = x.parent {
		for k := range x.vars {
			out = append(out, k)
		}
	}
	out = append(out, c.cur.memberNames()...)
	for k := range c.cur.imports {
		out = append(out, k)
	}
	out = append(out, c.core.memberNames()...)
	return out
}

// checkShadow reports a declaration that hides another name (P4: no shadowing).
func (c *Checker) checkShadow(s *scope, name string, pos ast.Pos) bool {
	if name == "_" {
		return false
	}
	if v := s.lookup(name); v != nil {
		c.errorf("E0206", pos, fmt.Sprintf("choose a new name (e.g. %s2); names are unique within a function", name), "'%s' shadows the %s declared at line %d", name, kindWord(v.kind), v.pos.Line)
		return true
	}
	if c.cur.member(name) != nil || c.cur.imports[name] != nil {
		c.errorf("E0206", pos, "choose a name that differs from module-level declarations", "'%s' shadows a module-level name", name)
		return true
	}
	if c.core.member(name) != nil {
		c.errorf("E0206", pos, "choose another name", "'%s' shadows the builtin '%s'", name, name)
		return true
	}
	return false
}

func kindWord(k string) string {
	if k == "param" {
		return "parameter"
	}
	return "variable"
}

// narrowKey returns "x" or "x.f.g" for expressions that can be nil-narrowed.
func narrowKey(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.Selector:
		if k := narrowKey(e.X); k != "" {
			return k + "." + e.Name
		}
	case *ast.Paren:
		return narrowKey(e.X)
	}
	return ""
}

// nilTest recognizes "k == nil" / "k != nil" (either side).
func nilTest(e ast.Expr) (key string, notNil bool, ok bool) {
	for {
		p, isParen := e.(*ast.Paren)
		if !isParen {
			break
		}
		e = p.X
	}
	b, isBin := e.(*ast.Binary)
	if !isBin || (b.Op != "==" && b.Op != "!=") {
		return "", false, false
	}
	var other ast.Expr
	if _, isNil := b.Y.(*ast.NilLit); isNil {
		other = b.X
	} else if _, isNil := b.X.(*ast.NilLit); isNil {
		other = b.Y
	} else {
		return "", false, false
	}
	k := narrowKey(other)
	return k, b.Op == "!=", k != ""
}

// nonNilWhen returns the keys known to be non-nil when cond evaluates to want.
func nonNilWhen(cond ast.Expr, want bool) []string {
	for {
		p, isParen := cond.(*ast.Paren)
		if !isParen {
			break
		}
		cond = p.X
	}
	if k, notNil, ok := nilTest(cond); ok {
		if notNil == want {
			return []string{k}
		}
		return nil
	}
	if b, ok := cond.(*ast.Binary); ok {
		if (b.Op == "and" && want) || (b.Op == "or" && !want) {
			return append(nonNilWhen(b.X, want), nonNilWhen(b.Y, want)...)
		}
	}
	if u, ok := cond.(*ast.Unary); ok && u.Op == "not" {
		return nonNilWhen(u.X, !want)
	}
	return nil
}

// applyNarrow marks keys as non-nil in scope s.
func (c *Checker) applyNarrow(fc *fnCtx, s *scope, keys []string) {
	for _, k := range keys {
		t := c.keyType(fc, s, k)
		if t != nil && t.K == KOpt {
			s.narrow[k] = t.Elem
		}
	}
}

// keyType computes the (un-narrowed) type of a narrowing key.
func (c *Checker) keyType(fc *fnCtx, s *scope, key string) *Type {
	parts := splitDots(key)
	v := s.lookup(parts[0])
	if v == nil {
		return nil
	}
	t := v.typ
	prefix := parts[0]
	for _, p := range parts[1:] {
		if nt := s.narrowed(prefix); nt != nil {
			t = nt
		}
		if t == nil || t.K != KStruct {
			return nil
		}
		f := t.Struct.field(p)
		if f == nil {
			return nil
		}
		t = f.typ
		prefix += "." + p
	}
	return t
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

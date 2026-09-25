// Package printer renders an AST back to canonical Pygo source.
//
// There is exactly one canonical layout (4-space indentation, one statement
// per line, fixed spacing), so `pygo fmt` output is a stable normal form.
// Ordinary "//" comments are not preserved; "///" doc comments are.
package printer

import (
	"strconv"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
)

type pr struct {
	b      strings.Builder
	indent int
}

func (p *pr) w(s ...string) {
	for _, x := range s {
		p.b.WriteString(x)
	}
}

func (p *pr) nl() {
	p.b.WriteByte('\n')
	p.b.WriteString(strings.Repeat("    ", p.indent))
}

func (p *pr) doc(d string) {
	if d == "" {
		return
	}
	for _, line := range strings.Split(d, "\n") {
		if line == "" {
			p.w("///")
		} else {
			p.w("/// ", line)
		}
		p.nl()
	}
}

// File renders a whole file.
func File(f *ast.File) string {
	p := &pr{}
	var prev ast.Decl
	for _, d := range f.Decls {
		if prev != nil {
			_, pi := prev.(*ast.ImportDecl)
			_, ci := d.(*ast.ImportDecl)
			_, pc := prev.(*ast.ConstDecl)
			_, cc := d.(*ast.ConstDecl)
			if (pi && ci) || (pc && cc) {
				p.w("\n")
			} else {
				p.w("\n\n")
			}
		}
		p.decl(d)
		prev = d
	}
	if len(f.Decls) > 0 {
		p.w("\n")
	}
	return p.b.String()
}

// Decl renders one declaration.
func Decl(d ast.Decl) string {
	p := &pr{}
	p.decl(d)
	return p.b.String()
}

// Expr renders an expression on one line.
func Expr(e ast.Expr) string {
	p := &pr{}
	p.expr(e)
	return p.b.String()
}

// Pattern renders a match pattern.
func Pattern(pt ast.Pattern) string {
	p := &pr{}
	p.pattern(pt)
	return p.b.String()
}

// Type renders a type annotation.
func Type(t *ast.TypeExpr) string {
	if t == nil {
		return "Nil"
	}
	p := &pr{}
	p.typ(t)
	return p.b.String()
}

// Signature renders a function header without body, e.g.
// "fn get(url: Str, timeout_ms: Int = 30000) -> !Response uses net".
func Signature(fd *ast.FuncDecl) string {
	p := &pr{}
	p.sig(fd)
	return p.b.String()
}

func (p *pr) decl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.ImportDecl:
		p.w("import ", strconv.Quote(d.Path))
		if d.Alias != "" {
			p.w(" as ", d.Alias)
		}
	case *ast.ConstDecl:
		p.doc(d.Doc)
		p.stmt(d.Let)
	case *ast.FuncDecl:
		p.funcDecl(d)
	case *ast.StructDecl:
		p.doc(d.Doc)
		p.w("struct ", d.Name, " {")
		p.fields(d.Fields)
		p.w("}")
	case *ast.EnumDecl:
		p.doc(d.Doc)
		p.w("enum ", d.Name, " {")
		p.indent++
		for _, v := range d.Variants {
			p.nl()
			p.doc(v.Doc)
			p.w(v.Name)
			if len(v.Fields) > 0 {
				p.w("(")
				for i, f := range v.Fields {
					if i > 0 {
						p.w(", ")
					}
					p.field(f)
				}
				p.w(")")
			}
		}
		p.indent--
		if len(d.Variants) > 0 {
			p.nl()
		}
		p.w("}")
	case *ast.ImplDecl:
		p.w("impl ", d.Type, " {")
		p.indent++
		for i, m := range d.Methods {
			if i > 0 {
				p.w("\n")
			}
			p.nl()
			p.funcDecl(m)
		}
		p.indent--
		p.nl()
		p.w("}")
	case *ast.TestDecl:
		p.w("test ", strconv.Quote(d.Name), " ")
		p.block(d.Body)
	case *ast.ExternDecl:
		p.doc(d.Doc)
		p.w("extern ", d.Lang, " ", strconv.Quote(d.Module))
		if d.Alias != "" {
			p.w(" as ", d.Alias)
		}
		p.w(" {")
		p.indent++
		for _, t := range d.Types {
			p.nl()
			p.doc(t.Doc)
			p.w("type ", t.Name)
			if len(t.Methods) > 0 {
				p.w(" {")
				p.indent++
				for _, m := range t.Methods {
					p.nl()
					p.doc(m.Doc)
					p.sig(m)
				}
				p.indent--
				p.nl()
				p.w("}")
			}
		}
		for _, f := range d.Funcs {
			p.nl()
			p.doc(f.Doc)
			p.sig(f)
		}
		p.indent--
		p.nl()
		p.w("}")
	}
}

func (p *pr) fields(fs []*ast.Field) {
	p.indent++
	for _, f := range fs {
		p.nl()
		p.doc(f.Doc)
		p.field(f)
	}
	p.indent--
	if len(fs) > 0 {
		p.nl()
	}
}

func (p *pr) field(f *ast.Field) {
	p.w(f.Name, ": ")
	p.typ(f.Type)
	if f.Default != nil {
		p.w(" = ")
		p.expr(f.Default)
	}
}

func (p *pr) sig(d *ast.FuncDecl) {
	p.w("fn ", d.Name)
	if len(d.TypeParams) > 0 {
		p.w("[", strings.Join(d.TypeParams, ", "), "]")
	}
	p.w("(")
	n := 0
	if d.HasSelf {
		p.w("self")
		n++
	}
	for _, prm := range d.Params {
		if n > 0 {
			p.w(", ")
		}
		p.param(prm)
		n++
	}
	p.w(")")
	p.ret(d.Ret, d.Fallible)
	if len(d.Uses) > 0 {
		p.w(" uses ", strings.Join(d.Uses, ", "))
	}
}

func (p *pr) funcDecl(d *ast.FuncDecl) {
	p.doc(d.Doc)
	p.sig(d)
	p.indent++
	for _, r := range d.Requires {
		p.nl()
		p.w("requires ")
		p.expr(r)
	}
	for _, e := range d.Ensures {
		p.nl()
		p.w("ensures ")
		p.expr(e)
	}
	p.indent--
	contracts := len(d.Requires)+len(d.Ensures) > 0
	if d.ExprBody != nil {
		if contracts {
			p.nl()
		} else {
			p.w(" ")
		}
		p.w("=> ")
		p.expr(d.ExprBody)
		return
	}
	if contracts {
		p.nl()
	} else {
		p.w(" ")
	}
	p.block(d.Body)
}

func (p *pr) param(prm *ast.Param) {
	p.w(prm.Name)
	if prm.Type != nil {
		p.w(": ")
		if prm.Variadic {
			p.w("...")
		}
		p.typ(prm.Type)
	}
	if prm.Default != nil {
		p.w(" = ")
		p.expr(prm.Default)
	}
}

func (p *pr) ret(t *ast.TypeExpr, fallible bool) {
	if t == nil && !fallible {
		return
	}
	p.w(" -> ")
	if fallible {
		p.w("!")
	}
	if t != nil {
		p.typ(t)
	}
}

func (p *pr) typ(t *ast.TypeExpr) {
	if t == nil {
		return
	}
	if t.Name == "fn" {
		p.w("fn(")
		for i, a := range t.Args {
			if i > 0 {
				p.w(", ")
			}
			p.typ(a)
		}
		p.w(")")
		p.ret(t.Ret, t.Fallible)
	} else {
		if t.Name == "Nil" {
			p.w("nil")
		} else {
			p.w(t.Name)
		}
		if len(t.Args) > 0 {
			p.w("[")
			for i, a := range t.Args {
				if i > 0 {
					p.w(", ")
				}
				p.typ(a)
			}
			p.w("]")
		}
	}
	if t.Optional {
		p.w("?")
	}
}

func (p *pr) block(b *ast.Block) {
	if b == nil || len(b.Stmts) == 0 {
		p.w("{}")
		return
	}
	p.w("{")
	p.indent++
	for _, s := range b.Stmts {
		p.nl()
		p.stmt(s)
	}
	p.indent--
	p.nl()
	p.w("}")
}

func (p *pr) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.Let:
		if s.Mutable {
			p.w("var ")
		} else {
			p.w("let ")
		}
		p.w(s.Name)
		if s.Type != nil {
			p.w(": ")
			p.typ(s.Type)
		}
		p.w(" = ")
		p.expr(s.Value)
	case *ast.Assign:
		p.expr(s.Target)
		p.w(" ", s.Op, " ")
		p.expr(s.Value)
	case *ast.ExprStmt:
		p.expr(s.X)
	case *ast.Return:
		p.w("return")
		if s.Value != nil {
			p.w(" ")
			p.expr(s.Value)
		}
	case *ast.Break:
		p.w("break")
	case *ast.Continue:
		p.w("continue")
	case *ast.For:
		p.w("for ")
		if s.Key != "" {
			p.w(s.Key, ", ")
		}
		p.w(s.Val, " in ")
		p.expr(s.Iter)
		p.w(" ")
		p.block(s.Body)
	case *ast.While:
		p.w("while ")
		p.expr(s.Cond)
		p.w(" ")
		p.block(s.Body)
	case *ast.Fail:
		p.w("fail ")
		p.expr(s.Value)
	case *ast.Assert:
		p.w("assert ")
		p.expr(s.Cond)
		if s.Msg != nil {
			p.w(", ")
			p.expr(s.Msg)
		}
	case *ast.Defer:
		p.w("defer ")
		p.expr(s.Call)
	}
}

func escapeLit(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '$':
			if i+1 < len(rs) && rs[i+1] == '{' {
				b.WriteString(`\$`)
			} else {
				b.WriteByte('$')
			}
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case 0:
			b.WriteString(`\0`)
		default:
			if r < 0x20 {
				b.WriteString(`\u{` + strconv.FormatInt(int64(r), 16) + `}`)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func (p *pr) args(args []ast.Arg) {
	for i, a := range args {
		if i > 0 {
			p.w(", ")
		}
		if a.Name != "" {
			p.w(a.Name, ": ")
		}
		p.expr(a.Value)
	}
}

func (p *pr) expr(e ast.Expr) {
	switch e := e.(type) {
	case nil:
	case *ast.Ident:
		p.w(e.Name)
	case *ast.IntLit:
		p.w(strconv.FormatInt(e.Value, 10))
	case *ast.FloatLit:
		s := strconv.FormatFloat(e.Value, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eIN") {
			s += ".0"
		}
		p.w(s)
	case *ast.BoolLit:
		p.w(strconv.FormatBool(e.Value))
	case *ast.NilLit:
		p.w("nil")
	case *ast.StrLit:
		p.w(`"`)
		for _, part := range e.Parts {
			if part.Expr == nil {
				p.w(escapeLit(part.Lit))
				continue
			}
			p.w("${")
			p.expr(part.Expr)
			if part.Format != "" {
				p.w(":", part.Format)
			}
			p.w("}")
		}
		p.w(`"`)
	case *ast.ListLit:
		p.w("[")
		for i, x := range e.Elems {
			if i > 0 {
				p.w(", ")
			}
			p.expr(x)
		}
		p.w("]")
	case *ast.MapLit:
		p.w("{")
		for i, en := range e.Entries {
			if i > 0 {
				p.w(", ")
			}
			p.expr(en.Key)
			p.w(": ")
			p.expr(en.Value)
		}
		p.w("}")
	case *ast.StructLit:
		p.expr(e.Type)
		p.w("{")
		for i, f := range e.Fields {
			if i > 0 {
				p.w(", ")
			}
			p.w(f.Name, ": ")
			p.expr(f.Value)
		}
		p.w("}")
	case *ast.Unary:
		if e.Op == "not" {
			p.w("not ")
		} else {
			p.w(e.Op)
		}
		p.expr(e.X)
	case *ast.Binary:
		p.expr(e.X)
		p.w(" ", e.Op, " ")
		p.expr(e.Y)
	case *ast.Paren:
		p.w("(")
		p.expr(e.X)
		p.w(")")
	case *ast.Call:
		p.expr(e.Fn)
		p.w("(")
		p.args(e.Args)
		p.w(")")
	case *ast.Index:
		p.expr(e.X)
		p.w("[")
		p.expr(e.Index)
		p.w("]")
	case *ast.Selector:
		p.expr(e.X)
		p.w(".", e.Name)
	case *ast.Range:
		p.expr(e.Lo)
		if e.Inclusive {
			p.w("..=")
		} else {
			p.w("..")
		}
		p.expr(e.Hi)
	case *ast.FuncLit:
		p.w("fn(")
		for i, prm := range e.Params {
			if i > 0 {
				p.w(", ")
			}
			p.param(prm)
		}
		p.w(")")
		p.ret(e.Ret, e.Fallible)
		if e.ExprBody != nil {
			p.w(" => ")
			p.expr(e.ExprBody)
		} else {
			p.w(" ")
			p.block(e.Body)
		}
	case *ast.If:
		p.w("if ")
		p.expr(e.Cond)
		p.w(" ")
		p.block(e.Then)
		if e.Else != nil {
			p.w(" else ")
			if b, ok := e.Else.(*ast.Block); ok {
				p.block(b)
			} else {
				p.expr(e.Else)
			}
		}
	case *ast.Match:
		p.w("match ")
		p.expr(e.Subject)
		p.w(" {")
		p.indent++
		for _, a := range e.Arms {
			p.nl()
			for i, pat := range a.Patterns {
				if i > 0 {
					p.w(" | ")
				}
				p.pattern(pat)
			}
			if a.Guard != nil {
				p.w(" if ")
				p.expr(a.Guard)
			}
			p.w(" => ")
			if b, ok := a.Body.(*ast.Block); ok {
				p.block(b)
			} else {
				p.expr(a.Body)
			}
		}
		p.indent--
		p.nl()
		p.w("}")
	case *ast.Block:
		p.block(e)
	case *ast.Try:
		p.w("try ")
		p.expr(e.X)
	case *ast.Catch:
		p.expr(e.X)
		p.w(" catch ")
		if e.Name != "" {
			p.w(e.Name, " ")
		}
		p.block(e.Body)
	case *ast.Spawn:
		p.w("spawn ")
		p.expr(e.Call)
	default:
		p.w("<?>")
	}
}

func (p *pr) pattern(pt ast.Pattern) {
	switch pt := pt.(type) {
	case *ast.WildcardPat:
		p.w("_")
	case *ast.IdentPat:
		p.w(pt.Name)
	case *ast.LitPat:
		p.expr(pt.Value)
	case *ast.RangePat:
		p.expr(pt.Lo)
		if pt.Inclusive {
			p.w("..=")
		} else {
			p.w("..")
		}
		p.expr(pt.Hi)
	case *ast.VariantPat:
		if pt.Enum != "" {
			p.w(pt.Enum, ".")
		}
		p.w(pt.Variant)
		if pt.HasArgs {
			p.w("(")
			for i, a := range pt.Args {
				if i > 0 {
					p.w(", ")
				}
				p.pattern(a)
			}
			p.w(")")
		}
	}
}

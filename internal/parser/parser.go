// Package parser builds an AST from Pygo tokens.
//
// The parser reports every error it can find in one pass: after an error it
// skips to the next top-level declaration (a keyword in column 1) and goes on.
package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/lexer"
)

type Parser struct {
	file    string
	toks    []lexer.Token
	i       int
	noBrace bool
	Diags   diag.List
}

type bailout struct{}

// ParseFile parses a whole source file.
func ParseFile(file, src string) (*ast.File, []diag.Diagnostic) {
	lx := lexer.New(file, src)
	toks := lx.Tokenize()
	p := &Parser{file: file, toks: toks}
	p.Diags.File = file
	p.Diags.Merge(lx.Diags.Items)
	f := &ast.File{Name: file}
	for {
		p.skipNewlines()
		if p.tok().Kind == lexer.EOF {
			break
		}
		if d := p.declOrRecover(); d != nil {
			f.Decls = append(f.Decls, d)
		}
	}
	return f, p.Diags.Items
}

// ParseExpr parses a single expression (used for interpolations and tools).
func ParseExpr(file, src string, pos diag.Pos) (ast.Expr, []diag.Diagnostic) {
	lx := lexer.NewAt(file, src, pos)
	toks := lx.Tokenize()
	p := &Parser{file: file, toks: toks}
	p.Diags.File = file
	p.Diags.Merge(lx.Diags.Items)
	var e ast.Expr
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bailout); !ok {
					panic(r)
				}
			}
		}()
		e = p.expr()
		p.skipNewlines()
		if p.tok().Kind != lexer.EOF {
			p.errorf("E0111", p.tok().Pos, "", "unexpected %s after expression", p.tok())
		}
	}()
	return e, p.Diags.Items
}

// ---------- token helpers ----------

func (p *Parser) tok() lexer.Token { return p.toks[p.i] }

func (p *Parser) peekTok(n int) lexer.Token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *Parser) next() lexer.Token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *Parser) prevEnd() int {
	if p.i == 0 {
		return 0
	}
	return p.toks[p.i-1].End
}

// is reports whether the current token is the operator or keyword s.
func (p *Parser) is(s string) bool {
	t := p.tok()
	return (t.Kind == lexer.OP || t.Kind == lexer.KEYWORD) && t.Text == s
}

func (p *Parser) accept(s string) bool {
	if p.is(s) {
		p.next()
		return true
	}
	return false
}

func (p *Parser) expect(s string) lexer.Token {
	if !p.is(s) {
		hint := ""
		if p.tok().Kind == lexer.NEWLINE && (s == "{" || s == "=>") {
			hint = fmt.Sprintf("put '%s' on the same line", s)
		}
		p.errorf("E0110", p.tok().Pos, hint, "expected '%s', found %s", s, p.tok())
	}
	return p.next()
}

func (p *Parser) ident() lexer.Token {
	if p.tok().Kind != lexer.IDENT {
		hint := ""
		if p.tok().Kind == lexer.KEYWORD {
			hint = fmt.Sprintf("'%s' is a reserved keyword; choose another name", p.tok().Text)
		}
		p.errorf("E0110", p.tok().Pos, hint, "expected identifier, found %s", p.tok())
	}
	return p.next()
}

func (p *Parser) skipNewlines() {
	for p.tok().Kind == lexer.NEWLINE {
		p.next()
	}
}

func (p *Parser) errorf(code string, pos diag.Pos, hint, format string, args ...any) {
	p.Diags.Errorf(code, pos, hint, format, args...)
	panic(bailout{})
}

// endStmt requires a statement terminator (newline) or a closing brace.
func (p *Parser) endStmt() {
	if p.tok().Kind == lexer.NEWLINE {
		p.skipNewlines()
		return
	}
	if p.is("}") || p.tok().Kind == lexer.EOF {
		return
	}
	p.errorf("E0112", p.tok().Pos, "put each statement on its own line", "unexpected %s at end of statement", p.tok())
}

// ---------- declarations ----------

var declKeywords = map[string]bool{"fn": true, "struct": true, "enum": true, "impl": true, "import": true, "test": true, "let": true, "var": true, "extern": true}

func (p *Parser) declOrRecover() (d ast.Decl) {
	start := p.i
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
			d = nil
			if p.i == start {
				p.next()
			}
			for p.tok().Kind != lexer.EOF {
				t := p.tok()
				if t.Kind == lexer.KEYWORD && declKeywords[t.Text] && t.Pos.Col == 1 {
					break
				}
				p.next()
			}
		}
	}()
	d = p.decl()
	if p.tok().Kind != lexer.EOF && p.tok().Kind != lexer.NEWLINE {
		p.errorf("E0112", p.tok().Pos, "put each declaration on its own line", "unexpected %s after declaration", p.tok())
	}
	return d
}

func (p *Parser) declStart() int {
	t := p.tok()
	if t.DocOff >= 0 {
		return t.DocOff
	}
	return t.Off
}

func (p *Parser) decl() ast.Decl {
	t := p.tok()
	start := p.declStart()
	switch {
	case p.is("import"):
		p.next()
		if p.tok().Kind != lexer.STRING {
			p.errorf("E0110", p.tok().Pos, `write import "json" or import "./path/module"`, "expected module path string")
		}
		st := p.next()
		path := st.Parts[0].Text
		d := &ast.ImportDecl{Pos: t.Pos, Path: path, Start: start}
		if p.accept("as") {
			d.Alias = p.ident().Text
		}
		d.End = p.prevEnd()
		return d
	case p.is("fn"):
		return p.funcDecl("")
	case p.is("extern"):
		return p.externDecl()
	case p.is("struct"):
		return p.structDecl()
	case p.is("enum"):
		return p.enumDecl()
	case p.is("impl"):
		p.next()
		name := p.ident().Text
		d := &ast.ImplDecl{Pos: t.Pos, Type: name, Start: start}
		p.expect("{")
		for {
			p.skipNewlines()
			if p.is("}") {
				break
			}
			if !p.is("fn") {
				p.errorf("E0110", p.tok().Pos, "impl blocks contain only fn declarations", "expected 'fn', found %s", p.tok())
			}
			m := p.funcDecl(name)
			d.Methods = append(d.Methods, m)
			p.endStmt()
		}
		p.expect("}")
		d.End = p.prevEnd()
		return d
	case p.is("test"):
		p.next()
		if p.tok().Kind != lexer.STRING {
			p.errorf("E0110", p.tok().Pos, `write test "description" { ... }`, "expected test name string")
		}
		name := p.next().Parts[0].Text
		body := p.block()
		return &ast.TestDecl{Pos: t.Pos, Name: name, Body: body, Start: start, End: p.prevEnd()}
	case p.is("let"):
		doc := t.Doc
		l := p.letStmt()
		return &ast.ConstDecl{Pos: t.Pos, Doc: doc, Let: l, Start: start, End: p.prevEnd()}
	case p.is("var"):
		p.errorf("E0209", t.Pos, "use 'let' for a module-level constant, or pass state explicitly", "module-level 'var' is not allowed (no global mutable state)")
	}
	p.errorf("E0110", t.Pos, "top-level declarations are: import, extern, fn, struct, enum, impl, test, let", "expected declaration, found %s", t)
	return nil
}

func (p *Parser) funcDecl(recv string) *ast.FuncDecl {
	return p.funcDeclMode(recv, false)
}

// funcDeclMode parses a function; sigOnly=true parses a body-less signature
// (declarations inside extern blocks).
func (p *Parser) funcDeclMode(recv string, sigOnly bool) *ast.FuncDecl {
	t := p.expect("fn")
	start := t.Off
	if t.DocOff >= 0 {
		start = t.DocOff
	}
	d := &ast.FuncDecl{Pos: t.Pos, Doc: t.Doc, Recv: recv, Start: start}
	d.Name = p.ident().Text
	if p.accept("[") {
		for {
			d.TypeParams = append(d.TypeParams, p.ident().Text)
			if !p.accept(",") {
				break
			}
		}
		p.expect("]")
	}
	p.expect("(")
	if recv != "" && p.tok().Kind == lexer.IDENT && p.tok().Text == "self" {
		p.next()
		d.HasSelf = true
		if p.is(":") {
			p.errorf("E0110", p.tok().Pos, "write just 'self'", "'self' has no type annotation")
		}
		if !p.is(")") {
			p.expect(",")
		}
	}
	d.Params = p.params(true)
	p.expect(")")
	d.Ret, d.Fallible = p.retType()
	if p.accept("uses") {
		for {
			d.Uses = append(d.Uses, p.ident().Text)
			if !p.accept(",") {
				break
			}
		}
	}
	for {
		save := p.i
		p.skipNewlines()
		if p.accept("requires") {
			d.Requires = append(d.Requires, p.expr())
			continue
		}
		if p.accept("ensures") {
			d.Ensures = append(d.Ensures, p.expr())
			continue
		}
		if p.is("{") || p.is("=>") {
			break
		}
		p.i = save
		break
	}
	if sigOnly {
		if p.is("{") || p.is("=>") {
			p.errorf("E0110", p.tok().Pos, "extern functions are implemented by the foreign library: remove the body", "extern function '%s' cannot have a body", d.Name)
		}
		d.End = p.prevEnd()
		return d
	}
	if p.accept("=>") {
		d.ExprBody = p.expr()
	} else if p.is("{") {
		d.Body = p.block()
	} else {
		p.errorf("E0110", p.tok().Pos, "a function body starts with '{' or '=> expr'", "expected function body, found %s", p.tok())
	}
	d.End = p.prevEnd()
	return d
}

// params parses a parameter list; typed=true requires type annotations.
func (p *Parser) params(typed bool) []*ast.Param {
	var ps []*ast.Param
	for !p.is(")") {
		nt := p.ident()
		prm := &ast.Param{Pos: nt.Pos, Name: nt.Text}
		if p.accept(":") {
			if p.is("..") && p.peekTok(1).Kind == lexer.OP && p.peekTok(1).Text == "." {
				p.next()
				p.next()
				prm.Variadic = true
			}
			prm.Type = p.typeExpr()
		} else if typed {
			p.errorf("E0113", nt.Pos, fmt.Sprintf("write '%s: Type'", nt.Text), "parameter '%s' needs a type annotation", nt.Text)
		}
		if p.accept("=") {
			prm.Default = p.expr()
		}
		ps = append(ps, prm)
		if !p.accept(",") {
			break
		}
	}
	return ps
}

func (p *Parser) retType() (*ast.TypeExpr, bool) {
	if !p.accept("->") {
		return nil, false
	}
	fallible := p.accept("!")
	if p.tok().Kind == lexer.IDENT || p.is("fn") || p.is("nil") {
		return p.typeExpr(), fallible
	}
	if !fallible {
		p.errorf("E0110", p.tok().Pos, "", "expected return type after '->'")
	}
	return nil, true
}

func (p *Parser) typeExpr() *ast.TypeExpr {
	t := p.tok()
	var te *ast.TypeExpr
	if p.accept("fn") {
		te = &ast.TypeExpr{Pos: t.Pos, Name: "fn"}
		p.expect("(")
		for !p.is(")") {
			te.Args = append(te.Args, p.typeExpr())
			if !p.accept(",") {
				break
			}
		}
		p.expect(")")
		te.Ret, te.Fallible = p.retType()
	} else if p.accept("nil") {
		te = &ast.TypeExpr{Pos: t.Pos, Name: "Nil"}
	} else {
		name := p.ident().Text
		if p.is(".") {
			p.next()
			name += "." + p.ident().Text
		}
		te = &ast.TypeExpr{Pos: t.Pos, Name: name}
		if p.accept("[") {
			for {
				te.Args = append(te.Args, p.typeExpr())
				if !p.accept(",") {
					break
				}
			}
			p.expect("]")
		}
	}
	if p.accept("?") {
		te.Optional = true
	}
	return te
}

func (p *Parser) fieldList(closer string) []*ast.Field {
	var fs []*ast.Field
	for {
		p.skipNewlines()
		if p.is(closer) {
			break
		}
		nt := p.ident()
		f := &ast.Field{Pos: nt.Pos, Name: nt.Text, Doc: nt.Doc}
		p.expect(":")
		f.Type = p.typeExpr()
		if p.accept("=") {
			f.Default = p.expr()
		}
		fs = append(fs, f)
		if p.accept(",") {
			continue
		}
		if p.tok().Kind != lexer.NEWLINE && !p.is(closer) {
			p.errorf("E0110", p.tok().Pos, "separate fields with ',' or newlines", "expected ',' or '%s', found %s", closer, p.tok())
		}
	}
	return fs
}

func (p *Parser) structDecl() *ast.StructDecl {
	t := p.expect("struct")
	start := t.Off
	if t.DocOff >= 0 {
		start = t.DocOff
	}
	d := &ast.StructDecl{Pos: t.Pos, Doc: t.Doc, Start: start}
	d.Name = p.ident().Text
	p.expect("{")
	d.Fields = p.fieldList("}")
	p.expect("}")
	d.End = p.prevEnd()
	return d
}

func (p *Parser) enumDecl() *ast.EnumDecl {
	t := p.expect("enum")
	start := t.Off
	if t.DocOff >= 0 {
		start = t.DocOff
	}
	d := &ast.EnumDecl{Pos: t.Pos, Doc: t.Doc, Start: start}
	d.Name = p.ident().Text
	p.expect("{")
	for {
		p.skipNewlines()
		if p.is("}") {
			break
		}
		nt := p.ident()
		v := &ast.Variant{Pos: nt.Pos, Name: nt.Text, Doc: nt.Doc}
		if p.accept("(") {
			v.Fields = p.fieldList(")")
			p.expect(")")
		}
		d.Variants = append(d.Variants, v)
		if p.accept(",") {
			continue
		}
		if p.tok().Kind != lexer.NEWLINE && !p.is("}") {
			p.errorf("E0110", p.tok().Pos, "separate variants with ',' or newlines", "expected ',' or '}', found %s", p.tok())
		}
	}
	p.expect("}")
	d.End = p.prevEnd()
	return d
}

// ---------- statements ----------

func (p *Parser) block() *ast.Block {
	t := p.expect("{")
	b := &ast.Block{Pos: t.Pos}
	for {
		p.skipNewlines()
		if p.is("}") || p.tok().Kind == lexer.EOF {
			break
		}
		b.Stmts = append(b.Stmts, p.stmt())
		p.endStmt()
	}
	b.End = p.tok().Pos
	p.expect("}")
	return b
}

// blockNoBrace parses a block with struct/map literals re-enabled inside.
func (p *Parser) blockInner() *ast.Block {
	save := p.noBrace
	p.noBrace = false
	b := p.block()
	p.noBrace = save
	return b
}

func (p *Parser) letStmt() *ast.Let {
	t := p.next() // let / var
	nt := p.ident()
	l := &ast.Let{Pos: t.Pos, Name: nt.Text, Mutable: t.Text == "var"}
	if p.accept(":") {
		l.Type = p.typeExpr()
	}
	if !p.is("=") {
		p.errorf("E0110", p.tok().Pos, fmt.Sprintf("write '%s %s = value'", t.Text, nt.Text), "variables must be initialized")
	}
	p.next()
	l.Value = p.expr()
	return l
}

var assignOps = map[string]bool{"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true}

func (p *Parser) stmt() ast.Stmt {
	t := p.tok()
	switch {
	case p.is("let"), p.is("var"):
		return p.letStmt()
	case p.is("return"):
		p.next()
		r := &ast.Return{Pos: t.Pos}
		if p.tok().Kind != lexer.NEWLINE && !p.is("}") {
			r.Value = p.expr()
		}
		return r
	case p.is("break"):
		p.next()
		return &ast.Break{Pos: t.Pos}
	case p.is("continue"):
		p.next()
		return &ast.Continue{Pos: t.Pos}
	case p.is("for"):
		p.next()
		f := &ast.For{Pos: t.Pos}
		f.Val = p.ident().Text
		if p.accept(",") {
			f.Key = f.Val
			f.Val = p.ident().Text
		}
		p.expect("in")
		f.Iter = p.condExpr()
		f.Body = p.blockInner()
		return f
	case p.is("while"):
		p.next()
		w := &ast.While{Pos: t.Pos, Cond: p.condExpr()}
		w.Body = p.blockInner()
		return w
	case p.is("fail"):
		p.next()
		return &ast.Fail{Pos: t.Pos, Value: p.expr()}
	case p.is("assert"):
		p.next()
		a := &ast.Assert{Pos: t.Pos, Cond: p.expr()}
		if p.accept(",") {
			a.Msg = p.expr()
		}
		return a
	case p.is("defer"):
		p.next()
		e := p.expr()
		c, ok := e.(*ast.Call)
		if !ok {
			p.errorf("E0114", t.Pos, "write 'defer f(args)'", "defer requires a function call")
		}
		return &ast.Defer{Pos: t.Pos, Call: c}
	}
	e := p.expr()
	if p.tok().Kind == lexer.OP && assignOps[p.tok().Text] {
		op := p.next()
		switch e.(type) {
		case *ast.Ident, *ast.Index, *ast.Selector:
		default:
			p.errorf("E0115", op.Pos, "assign to a variable, an index a[i] or a field x.f", "invalid assignment target")
		}
		return &ast.Assign{Pos: op.Pos, Target: e, Op: op.Text, Value: p.expr()}
	}
	return &ast.ExprStmt{Pos: t.Pos, X: e}
}

// condExpr parses an expression where '{' starts a block, not a literal.
func (p *Parser) condExpr() ast.Expr {
	save := p.noBrace
	p.noBrace = true
	e := p.expr()
	p.noBrace = save
	return e
}

// ---------- expressions ----------

func (p *Parser) expr() ast.Expr { return p.coalesce() }

func isBareBinary(e ast.Expr) bool {
	_, ok := e.(*ast.Binary)
	return ok
}

func (p *Parser) coalesce() ast.Expr {
	x := p.or()
	if p.is("??") {
		op := p.next()
		y := p.coalesce()
		if isBareBinary(x) || (isBareBinary(y) && y.(*ast.Binary).Op != "??") {
			p.errorf("E0116", op.Pos, "add parentheses: (a ?? b) + c or a ?? (b + c)", "'??' mixed with other operators requires parentheses")
		}
		return &ast.Binary{Pos: op.Pos, Op: "??", X: x, Y: y}
	}
	return x
}

func (p *Parser) or() ast.Expr {
	x := p.and()
	for p.is("or") {
		op := p.next()
		y := p.and()
		for _, e := range []ast.Expr{x, y} {
			if b, ok := e.(*ast.Binary); ok && b.Op == "and" {
				p.errorf("E0116", op.Pos, "add parentheses: (a and b) or c", "mixing 'and' and 'or' requires parentheses")
			}
		}
		x = &ast.Binary{Pos: op.Pos, Op: "or", X: x, Y: y}
	}
	return x
}

func (p *Parser) and() ast.Expr {
	x := p.not()
	for p.is("and") {
		op := p.next()
		y := p.not()
		x = &ast.Binary{Pos: op.Pos, Op: "and", X: x, Y: y}
	}
	return x
}

func (p *Parser) not() ast.Expr {
	if p.is("not") {
		t := p.next()
		return &ast.Unary{Pos: t.Pos, Op: "not", X: p.not()}
	}
	return p.comparison()
}

var cmpOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true, "in": true}

func (p *Parser) isCmp() bool {
	t := p.tok()
	return (t.Kind == lexer.OP || t.Kind == lexer.KEYWORD) && cmpOps[t.Text]
}

func (p *Parser) comparison() ast.Expr {
	x := p.rangeExpr()
	if p.isCmp() {
		op := p.next()
		y := p.rangeExpr()
		x = &ast.Binary{Pos: op.Pos, Op: op.Text, X: x, Y: y}
		if p.isCmp() {
			p.errorf("E0116", p.tok().Pos, "write 'a < b and b < c'", "comparisons cannot be chained")
		}
	}
	return x
}

func (p *Parser) rangeExpr() ast.Expr {
	x := p.additive()
	if p.is("..") || p.is("..=") {
		op := p.next()
		y := p.additive()
		return &ast.Range{Pos: op.Pos, Lo: x, Hi: y, Inclusive: op.Text == "..="}
	}
	return x
}

func (p *Parser) additive() ast.Expr {
	x := p.multiplicative()
	for p.is("+") || p.is("-") {
		op := p.next()
		x = &ast.Binary{Pos: op.Pos, Op: op.Text, X: x, Y: p.multiplicative()}
	}
	return x
}

func (p *Parser) multiplicative() ast.Expr {
	x := p.unary()
	for p.is("*") || p.is("/") || p.is("%") {
		op := p.next()
		x = &ast.Binary{Pos: op.Pos, Op: op.Text, X: x, Y: p.unary()}
	}
	return x
}

func (p *Parser) unary() ast.Expr {
	t := p.tok()
	switch {
	case p.is("-"):
		p.next()
		x := p.unary()
		// fold negative literals so that -9223372036854775808 works
		switch l := x.(type) {
		case *ast.IntLit:
			return &ast.IntLit{Pos: t.Pos, Value: -l.Value}
		case *ast.FloatLit:
			return &ast.FloatLit{Pos: t.Pos, Value: -l.Value}
		}
		return &ast.Unary{Pos: t.Pos, Op: "-", X: x}
	case p.is("try"):
		p.next()
		return &ast.Try{Pos: t.Pos, X: p.unary()}
	case p.is("spawn"):
		p.next()
		x := p.postfix()
		c, ok := x.(*ast.Call)
		if !ok {
			p.errorf("E0114", t.Pos, "write 'spawn f(args)'", "spawn requires a function call")
		}
		return &ast.Spawn{Pos: t.Pos, Call: c}
	}
	return p.postfix()
}

func isTypeName(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name != "" && e.Name[0] >= 'A' && e.Name[0] <= 'Z'
	case *ast.Selector:
		if _, ok := e.X.(*ast.Ident); ok {
			return e.Name != "" && e.Name[0] >= 'A' && e.Name[0] <= 'Z'
		}
	}
	return false
}

func (p *Parser) postfix() ast.Expr {
	x := p.primary()
	for {
		t := p.tok()
		switch {
		case p.is("("):
			p.next()
			save := p.noBrace
			p.noBrace = false
			c := &ast.Call{Pos: t.Pos, Fn: x}
			for !p.is(")") {
				a := ast.Arg{Pos: p.tok().Pos}
				if p.tok().Kind == lexer.IDENT && p.peekTok(1).Kind == lexer.OP && p.peekTok(1).Text == ":" {
					a.Name = p.next().Text
					p.next()
				}
				a.Value = p.expr()
				c.Args = append(c.Args, a)
				if !p.accept(",") {
					break
				}
			}
			p.noBrace = save
			p.expect(")")
			x = c
		case p.is("["):
			p.next()
			save := p.noBrace
			p.noBrace = false
			idx := p.expr()
			p.noBrace = save
			p.expect("]")
			x = &ast.Index{Pos: t.Pos, X: x, Index: idx}
		case p.is("."):
			p.next()
			nt := p.tok()
			if nt.Kind != lexer.IDENT && nt.Kind != lexer.KEYWORD {
				p.errorf("E0110", nt.Pos, "", "expected field or method name after '.'")
			}
			p.next()
			x = &ast.Selector{Pos: nt.Pos, X: x, Name: nt.Text}
		case p.is("{") && !p.noBrace && isTypeName(x):
			x = p.structLit(x)
		case p.is("catch"):
			p.next()
			c := &ast.Catch{Pos: t.Pos, X: x}
			if p.tok().Kind == lexer.IDENT {
				c.Name = p.next().Text
			}
			c.Body = p.blockInner()
			return c
		default:
			return x
		}
	}
}

func (p *Parser) structLit(typ ast.Expr) ast.Expr {
	t := p.expect("{")
	s := &ast.StructLit{Pos: t.Pos, Type: typ}
	for {
		p.skipNewlines()
		if p.is("}") {
			break
		}
		nt := p.ident()
		p.expect(":")
		s.Fields = append(s.Fields, ast.FieldInit{Pos: nt.Pos, Name: nt.Text, Value: p.expr()})
		if !p.accept(",") && p.tok().Kind != lexer.NEWLINE && !p.is("}") {
			p.errorf("E0110", p.tok().Pos, "separate fields with ','", "expected ',' or '}', found %s", p.tok())
		}
	}
	p.expect("}")
	return s
}

func (p *Parser) primary() ast.Expr {
	t := p.tok()
	switch t.Kind {
	case lexer.INT:
		p.next()
		v, err := parseInt(t.Text)
		if err != nil {
			p.errorf("E0117", t.Pos, "Int is a 64-bit signed integer", "invalid integer literal %s", t.Text)
		}
		return &ast.IntLit{Pos: t.Pos, Value: v}
	case lexer.FLOAT:
		p.next()
		v, err := strconv.ParseFloat(strings.ReplaceAll(t.Text, "_", ""), 64)
		if err != nil {
			p.errorf("E0117", t.Pos, "", "invalid float literal %s", t.Text)
		}
		return &ast.FloatLit{Pos: t.Pos, Value: v}
	case lexer.STRING:
		p.next()
		return p.strLit(t)
	case lexer.IDENT:
		p.next()
		return &ast.Ident{Pos: t.Pos, Name: t.Text}
	}
	switch {
	case p.is("true"), p.is("false"):
		p.next()
		return &ast.BoolLit{Pos: t.Pos, Value: t.Text == "true"}
	case p.is("nil"):
		p.next()
		return &ast.NilLit{Pos: t.Pos}
	case p.is("("):
		p.next()
		save := p.noBrace
		p.noBrace = false
		x := p.expr()
		p.noBrace = save
		p.expect(")")
		return &ast.Paren{Pos: t.Pos, X: x}
	case p.is("["):
		p.next()
		save := p.noBrace
		p.noBrace = false
		l := &ast.ListLit{Pos: t.Pos}
		for !p.is("]") {
			l.Elems = append(l.Elems, p.expr())
			if !p.accept(",") {
				break
			}
		}
		p.noBrace = save
		p.expect("]")
		return l
	case p.is("{") && !p.noBrace:
		return p.mapLit()
	case p.is("fn"):
		return p.funcLit()
	case p.is("if"):
		return p.ifExpr()
	case p.is("match"):
		return p.matchExpr()
	}
	hint := ""
	if t.Kind == lexer.NEWLINE {
		hint = "an operator at the end of a line continues the expression; the line ended too early"
	}
	p.errorf("E0111", t.Pos, hint, "expected expression, found %s", t)
	return nil
}

func parseInt(s string) (int64, error) {
	s = strings.ReplaceAll(s, "_", "")
	return strconv.ParseInt(s, 0, 64)
}

func (p *Parser) strLit(t lexer.Token) ast.Expr {
	s := &ast.StrLit{Pos: t.Pos}
	for _, part := range t.Parts {
		if !part.IsExpr {
			s.Parts = append(s.Parts, ast.StrPart{Lit: part.Text})
			continue
		}
		e, ds := ParseExpr(p.file, part.Text, part.Pos)
		p.Diags.Merge(ds)
		if e == nil {
			e = &ast.NilLit{Pos: part.Pos}
		}
		s.Parts = append(s.Parts, ast.StrPart{Expr: e, Format: part.Format})
	}
	return s
}

func (p *Parser) mapLit() ast.Expr {
	t := p.expect("{")
	m := &ast.MapLit{Pos: t.Pos}
	for {
		p.skipNewlines()
		if p.is("}") {
			break
		}
		k := p.expr()
		p.expect(":")
		v := p.expr()
		m.Entries = append(m.Entries, ast.MapEntry{Key: k, Value: v})
		if !p.accept(",") && p.tok().Kind != lexer.NEWLINE && !p.is("}") {
			p.errorf("E0110", p.tok().Pos, "separate entries with ','", "expected ',' or '}', found %s", p.tok())
		}
	}
	p.expect("}")
	return m
}

func (p *Parser) funcLit() ast.Expr {
	t := p.expect("fn")
	f := &ast.FuncLit{Pos: t.Pos}
	p.expect("(")
	f.Params = p.params(false)
	p.expect(")")
	f.Ret, f.Fallible = p.retType()
	if p.accept("=>") {
		save := p.noBrace
		p.noBrace = false
		f.ExprBody = p.expr()
		p.noBrace = save
	} else {
		f.Body = p.blockInner()
	}
	return f
}

func (p *Parser) ifExpr() ast.Expr {
	t := p.expect("if")
	n := &ast.If{Pos: t.Pos, Cond: p.condExpr()}
	n.Then = p.blockInner()
	if p.is("else") {
		p.next()
		if p.is("if") {
			n.Else = p.ifExpr()
		} else {
			n.Else = p.blockInner()
		}
	} else if p.tok().Kind == lexer.NEWLINE && p.peekTok(1).Kind == lexer.KEYWORD && p.peekTok(1).Text == "else" {
		p.errorf("E0118", p.peekTok(1).Pos, "write '} else {' on one line", "'else' must be on the same line as the closing '}'")
	}
	return n
}

func (p *Parser) matchExpr() ast.Expr {
	t := p.expect("match")
	m := &ast.Match{Pos: t.Pos, Subject: p.condExpr()}
	save := p.noBrace
	p.noBrace = false
	p.expect("{")
	for {
		p.skipNewlines()
		if p.is("}") {
			break
		}
		arm := &ast.MatchArm{Pos: p.tok().Pos}
		for {
			arm.Patterns = append(arm.Patterns, p.pattern())
			if !p.accept("|") {
				break
			}
		}
		if p.accept("if") {
			arm.Guard = p.condExpr()
		}
		p.expect("=>")
		if p.is("{") {
			arm.Body = p.block()
		} else {
			arm.Body = p.expr()
		}
		m.Arms = append(m.Arms, arm)
		if !p.accept(",") && p.tok().Kind != lexer.NEWLINE && !p.is("}") {
			p.errorf("E0110", p.tok().Pos, "put each match arm on its own line", "expected newline or '}' after match arm, found %s", p.tok())
		}
	}
	p.expect("}")
	p.noBrace = save
	return m
}

func (p *Parser) pattern() ast.Pattern {
	t := p.tok()
	if t.Kind == lexer.IDENT && t.Text == "_" {
		p.next()
		return &ast.WildcardPat{Pos: t.Pos}
	}
	if t.Kind == lexer.IDENT {
		p.next()
		vp := &ast.VariantPat{Pos: t.Pos, Variant: t.Text}
		qualified := false
		if p.accept(".") {
			vp.Enum = t.Text
			vp.Variant = p.ident().Text
			qualified = true
		}
		if p.accept("(") {
			vp.HasArgs = true
			for !p.is(")") {
				vp.Args = append(vp.Args, p.pattern())
				if !p.accept(",") {
					break
				}
			}
			p.expect(")")
			return vp
		}
		if qualified {
			return vp
		}
		return &ast.IdentPat{Pos: t.Pos, Name: t.Text}
	}
	lit := p.litPatternValue()
	if p.is("..") || p.is("..=") {
		op := p.next()
		hi := p.litPatternValue()
		return &ast.RangePat{Pos: t.Pos, Lo: lit, Hi: hi, Inclusive: op.Text == "..="}
	}
	return &ast.LitPat{Pos: t.Pos, Value: lit}
}

func (p *Parser) litPatternValue() ast.Expr {
	t := p.tok()
	switch {
	case t.Kind == lexer.INT, t.Kind == lexer.FLOAT, p.is("-"):
		return p.unary()
	case t.Kind == lexer.STRING:
		p.next()
		s := p.strLit(t).(*ast.StrLit)
		for _, part := range s.Parts {
			if part.Expr != nil {
				p.errorf("E0119", t.Pos, "", "string patterns cannot contain interpolation")
			}
		}
		return s
	case p.is("true"), p.is("false"), p.is("nil"):
		return p.primary()
	}
	p.errorf("E0119", t.Pos, "patterns are: _, name, literal, lo..=hi, Enum.Variant(p, ...)", "invalid pattern %s", t)
	return nil
}

// externDecl parses: extern python "module" [as name] { fn ... ; type T { fn ... } }
func (p *Parser) externDecl() *ast.ExternDecl {
	t := p.expect("extern")
	start := t.Off
	if t.DocOff >= 0 {
		start = t.DocOff
	}
	d := &ast.ExternDecl{Pos: t.Pos, Doc: t.Doc, Start: start}
	lang := p.ident()
	if lang.Text != "python" {
		p.errorf("E0120", lang.Pos, `write extern python "module" { ... }`, "unsupported foreign language '%s' (supported: python)", lang.Text)
	}
	d.Lang = lang.Text
	if p.tok().Kind != lexer.STRING || len(p.tok().Parts) != 1 || p.tok().Parts[0].IsExpr {
		p.errorf("E0110", p.tok().Pos, `write extern python "statistics" { ... }`, "expected the foreign module name as a string")
	}
	d.Module = p.next().Parts[0].Text
	if p.accept("as") {
		d.Alias = p.ident().Text
	}
	p.expect("{")
	for {
		p.skipNewlines()
		if p.is("}") {
			break
		}
		switch {
		case p.is("fn"):
			d.Funcs = append(d.Funcs, p.funcDeclMode("", true))
		case p.tok().Kind == lexer.IDENT && p.tok().Text == "type":
			tt := p.next()
			nt := p.ident()
			et := &ast.ExternType{Pos: nt.Pos, Name: nt.Text, Doc: tt.Doc}
			if p.accept("{") {
				for {
					p.skipNewlines()
					if p.is("}") {
						break
					}
					if !p.is("fn") {
						p.errorf("E0110", p.tok().Pos, "type blocks inside extern contain only fn signatures", "expected 'fn', found %s", p.tok())
					}
					et.Methods = append(et.Methods, p.funcDeclMode(nt.Text, true))
					p.endStmt()
				}
				p.expect("}")
			}
			d.Types = append(d.Types, et)
		default:
			p.errorf("E0110", p.tok().Pos, "extern blocks contain fn signatures and type declarations", "expected 'fn' or 'type', found %s", p.tok())
		}
		p.endStmt()
	}
	p.expect("}")
	d.End = p.prevEnd()
	return d
}

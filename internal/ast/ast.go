// Package ast defines the syntax tree of Pygo programs.
package ast

import (
	"github.com/marcodc74/pygo/internal/diag"
)

type Pos = diag.Pos

type Node interface{ P() Pos }

type Expr interface {
	Node
	expr()
}

type Stmt interface {
	Node
	stmt()
}

type Decl interface {
	Node
	decl()
	Span() (start, end int)
}

// ---------- types ----------

// TypeExpr is a type annotation: Name[Args], T?, fn(A) -> !B.
type TypeExpr struct {
	Pos      Pos
	Name     string      // "Int", "List", "http.Request", "fn" for function types
	Args     []*TypeExpr // generic arguments, or fn parameter types
	Optional bool        // T?
	Ret      *TypeExpr   // fn types only (nil = no result)
	Fallible bool        // fn types only
}

// ---------- expressions ----------

type Ident struct {
	Pos  Pos
	Name string
}

type IntLit struct {
	Pos   Pos
	Value int64
}

type FloatLit struct {
	Pos   Pos
	Value float64
}

type BoolLit struct {
	Pos   Pos
	Value bool
}

type NilLit struct{ Pos Pos }

// StrPart is literal text or an interpolated expression.
type StrPart struct {
	Lit    string
	Expr   Expr // nil for literal parts
	Format string
}

type StrLit struct {
	Pos   Pos
	Parts []StrPart
}

type ListLit struct {
	Pos   Pos
	Elems []Expr
}

type MapEntry struct {
	Key   Expr
	Value Expr
}

type MapLit struct {
	Pos     Pos
	Entries []MapEntry
}

type FieldInit struct {
	Pos   Pos
	Name  string
	Value Expr
}

// StructLit is Name{field: value, ...}; Type is an Ident or a Selector.
type StructLit struct {
	Pos    Pos
	Type   Expr
	Fields []FieldInit
}

type Unary struct {
	Pos Pos
	Op  string // "-", "not"
	X   Expr
}

type Binary struct {
	Pos  Pos
	Op   string
	X, Y Expr
}

type Arg struct {
	Pos   Pos
	Name  string // "" for positional
	Value Expr
}

type Call struct {
	Pos  Pos
	Fn   Expr
	Args []Arg
}

type Index struct {
	Pos   Pos
	X     Expr
	Index Expr // a *Range means slicing
}

type Selector struct {
	Pos  Pos
	X    Expr
	Name string
}

type Range struct {
	Pos       Pos
	Lo, Hi    Expr
	Inclusive bool
}

type FuncLit struct {
	Pos      Pos
	Params   []*Param
	Ret      *TypeExpr
	Fallible bool
	Body     *Block
	ExprBody Expr
}

type If struct {
	Pos  Pos
	Cond Expr
	Then *Block
	Else Expr // *Block, *If or nil
}

type MatchArm struct {
	Pos      Pos
	Patterns []Pattern
	Guard    Expr
	Body     Expr // *Block or any expression
}

type Match struct {
	Pos     Pos
	Subject Expr
	Arms    []*MatchArm
}

type Block struct {
	Pos   Pos
	Stmts []Stmt
	End   Pos
}

type Try struct {
	Pos Pos
	X   Expr
}

type Catch struct {
	Pos  Pos
	X    Expr
	Name string // "" if the error is not bound
	Body *Block
}

type Spawn struct {
	Pos  Pos
	Call *Call
}

type Paren struct {
	Pos Pos
	X   Expr
}

// ---------- patterns ----------

type Pattern interface {
	Node
	pattern()
}

type WildcardPat struct{ Pos Pos }

// IdentPat binds a name, or matches a field-less enum variant with that name.
type IdentPat struct {
	Pos  Pos
	Name string
}

type LitPat struct {
	Pos   Pos
	Value Expr // literal expression (possibly negated number)
}

type RangePat struct {
	Pos       Pos
	Lo, Hi    Expr
	Inclusive bool
}

// VariantPat matches Enum.Variant(p1, p2) or Variant(p1, p2).
type VariantPat struct {
	Pos     Pos
	Enum    string // "" if unqualified
	Variant string
	Args    []Pattern
	HasArgs bool
}

// ---------- statements ----------

type Let struct {
	Pos     Pos
	Name    string
	Mutable bool
	Type    *TypeExpr
	Value   Expr
}

type Assign struct {
	Pos    Pos
	Target Expr
	Op     string // "=", "+=", ...
	Value  Expr
}

type ExprStmt struct {
	Pos Pos
	X   Expr
}

type Return struct {
	Pos   Pos
	Value Expr
}

type Break struct{ Pos Pos }
type Continue struct{ Pos Pos }

type For struct {
	Pos  Pos
	Key  string // index / key name, "" if absent
	Val  string
	Iter Expr
	Body *Block
}

type While struct {
	Pos  Pos
	Cond Expr
	Body *Block
}

type Fail struct {
	Pos   Pos
	Value Expr
}

type Assert struct {
	Pos  Pos
	Cond Expr
	Msg  Expr
}

type Defer struct {
	Pos  Pos
	Call *Call
}

// ---------- declarations ----------

type Param struct {
	Pos      Pos
	Name     string
	Type     *TypeExpr
	Default  Expr
	Variadic bool
}

type FuncDecl struct {
	Pos        Pos
	Name       string
	Doc        string
	TypeParams []string
	Params     []*Param
	Ret        *TypeExpr
	Fallible   bool
	Uses       []string
	Requires   []Expr
	Ensures    []Expr
	Body       *Block
	ExprBody   Expr
	Recv       string // impl type name for methods
	HasSelf    bool
	Start, End int
}

type Field struct {
	Pos     Pos
	Name    string
	Type    *TypeExpr
	Default Expr
	Doc     string
}

type StructDecl struct {
	Pos        Pos
	Name       string
	Doc        string
	Fields     []*Field
	Start, End int
}

type Variant struct {
	Pos    Pos
	Name   string
	Fields []*Field
	Doc    string
}

type EnumDecl struct {
	Pos        Pos
	Name       string
	Doc        string
	Variants   []*Variant
	Start, End int
}

type ImplDecl struct {
	Pos        Pos
	Type       string
	Methods    []*FuncDecl
	Start, End int
}

type ImportDecl struct {
	Pos        Pos
	Path       string
	Alias      string
	Start, End int
}

type TestDecl struct {
	Pos        Pos
	Name       string
	Body       *Block
	Start, End int
}

type ConstDecl struct {
	Pos        Pos
	Doc        string
	Let        *Let
	Start, End int
}

// ExternType is an opaque type of a foreign library (a handle to an object
// that stays in the foreign runtime), with its methods.
type ExternType struct {
	Pos     Pos
	Name    string
	Doc     string
	Methods []*FuncDecl
}

// ExternDecl declares typed bindings to a foreign library:
//
//	extern python "statistics" as st { fn mean(data: List[Float]) -> !Float }
type ExternDecl struct {
	Pos        Pos
	Doc        string
	Lang       string // "python"
	Module     string // foreign module path, e.g. "os.path"
	Alias      string // binding name ("" = last path segment)
	Funcs      []*FuncDecl
	Types      []*ExternType
	Start, End int
}

// Name returns the binding name of the extern module.
func (d *ExternDecl) Name() string {
	if d.Alias != "" {
		return d.Alias
	}
	name := d.Module
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' || name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

type File struct {
	Name  string
	Decls []Decl
}

// ---------- boilerplate ----------

func (n *Ident) P() Pos     { return n.Pos }
func (n *IntLit) P() Pos    { return n.Pos }
func (n *FloatLit) P() Pos  { return n.Pos }
func (n *BoolLit) P() Pos   { return n.Pos }
func (n *NilLit) P() Pos    { return n.Pos }
func (n *StrLit) P() Pos    { return n.Pos }
func (n *ListLit) P() Pos   { return n.Pos }
func (n *MapLit) P() Pos    { return n.Pos }
func (n *StructLit) P() Pos { return n.Pos }
func (n *Unary) P() Pos     { return n.Pos }
func (n *Binary) P() Pos    { return n.Pos }
func (n *Call) P() Pos      { return n.Pos }
func (n *Index) P() Pos     { return n.Pos }
func (n *Selector) P() Pos  { return n.Pos }
func (n *Range) P() Pos     { return n.Pos }
func (n *FuncLit) P() Pos   { return n.Pos }
func (n *If) P() Pos        { return n.Pos }
func (n *Match) P() Pos     { return n.Pos }
func (n *Block) P() Pos     { return n.Pos }
func (n *Try) P() Pos       { return n.Pos }
func (n *Catch) P() Pos     { return n.Pos }
func (n *Spawn) P() Pos     { return n.Pos }
func (n *Paren) P() Pos     { return n.Pos }

func (*Ident) expr()     {}
func (*IntLit) expr()    {}
func (*FloatLit) expr()  {}
func (*BoolLit) expr()   {}
func (*NilLit) expr()    {}
func (*StrLit) expr()    {}
func (*ListLit) expr()   {}
func (*MapLit) expr()    {}
func (*StructLit) expr() {}
func (*Unary) expr()     {}
func (*Binary) expr()    {}
func (*Call) expr()      {}
func (*Index) expr()     {}
func (*Selector) expr()  {}
func (*Range) expr()     {}
func (*FuncLit) expr()   {}
func (*If) expr()        {}
func (*Match) expr()     {}
func (*Block) expr()     {}
func (*Try) expr()       {}
func (*Catch) expr()     {}
func (*Spawn) expr()     {}
func (*Paren) expr()     {}

func (n *WildcardPat) P() Pos { return n.Pos }
func (n *IdentPat) P() Pos    { return n.Pos }
func (n *LitPat) P() Pos      { return n.Pos }
func (n *RangePat) P() Pos    { return n.Pos }
func (n *VariantPat) P() Pos  { return n.Pos }
func (*WildcardPat) pattern() {}
func (*IdentPat) pattern()    {}
func (*LitPat) pattern()      {}
func (*RangePat) pattern()    {}
func (*VariantPat) pattern()  {}

func (n *Let) P() Pos      { return n.Pos }
func (n *Assign) P() Pos   { return n.Pos }
func (n *ExprStmt) P() Pos { return n.Pos }
func (n *Return) P() Pos   { return n.Pos }
func (n *Break) P() Pos    { return n.Pos }
func (n *Continue) P() Pos { return n.Pos }
func (n *For) P() Pos      { return n.Pos }
func (n *While) P() Pos    { return n.Pos }
func (n *Fail) P() Pos     { return n.Pos }
func (n *Assert) P() Pos   { return n.Pos }
func (n *Defer) P() Pos    { return n.Pos }

func (*Let) stmt()      {}
func (*Assign) stmt()   {}
func (*ExprStmt) stmt() {}
func (*Return) stmt()   {}
func (*Break) stmt()    {}
func (*Continue) stmt() {}
func (*For) stmt()      {}
func (*While) stmt()    {}
func (*Fail) stmt()     {}
func (*Assert) stmt()   {}
func (*Defer) stmt()    {}

func (n *FuncDecl) P() Pos   { return n.Pos }
func (n *StructDecl) P() Pos { return n.Pos }
func (n *EnumDecl) P() Pos   { return n.Pos }
func (n *ImplDecl) P() Pos   { return n.Pos }
func (n *ImportDecl) P() Pos { return n.Pos }
func (n *TestDecl) P() Pos   { return n.Pos }
func (n *ConstDecl) P() Pos  { return n.Pos }
func (n *ExternDecl) P() Pos { return n.Pos }

func (*FuncDecl) decl()   {}
func (*StructDecl) decl() {}
func (*EnumDecl) decl()   {}
func (*ImplDecl) decl()   {}
func (*ImportDecl) decl() {}
func (*TestDecl) decl()   {}
func (*ConstDecl) decl()  {}
func (*ExternDecl) decl() {}

func (n *FuncDecl) Span() (int, int)   { return n.Start, n.End }
func (n *StructDecl) Span() (int, int) { return n.Start, n.End }
func (n *EnumDecl) Span() (int, int)   { return n.Start, n.End }
func (n *ImplDecl) Span() (int, int)   { return n.Start, n.End }
func (n *ImportDecl) Span() (int, int) { return n.Start, n.End }
func (n *TestDecl) Span() (int, int)   { return n.Start, n.End }
func (n *ConstDecl) Span() (int, int)  { return n.Start, n.End }
func (n *ExternDecl) Span() (int, int) { return n.Start, n.End }

// DeclKey returns the symbol path used by outline/edit ("fn:name",
// "struct:Name", "impl:Name", "test:name", ...).
func DeclKey(d Decl) string {
	switch d := d.(type) {
	case *FuncDecl:
		return "fn:" + d.Name
	case *StructDecl:
		return "struct:" + d.Name
	case *EnumDecl:
		return "enum:" + d.Name
	case *ImplDecl:
		return "impl:" + d.Type
	case *ImportDecl:
		return "import:" + d.Path
	case *TestDecl:
		return "test:" + d.Name
	case *ConstDecl:
		return "let:" + d.Let.Name
	case *ExternDecl:
		return "extern:" + d.Name()
	}
	return "?"
}

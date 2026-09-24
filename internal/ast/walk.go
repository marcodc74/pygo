package ast

// Inspect traverses the tree rooted at n in depth-first order, calling f for
// every node. If f returns false, the children of that node are skipped.
func Inspect(n Node, f func(Node) bool) {
	if n == nil || !f(n) {
		return
	}
	expr := func(e Expr) {
		if e != nil {
			Inspect(e, f)
		}
	}
	block := func(b *Block) {
		if b != nil {
			Inspect(b, f)
		}
	}
	switch n := n.(type) {
	case *StrLit:
		for _, p := range n.Parts {
			expr(p.Expr)
		}
	case *ListLit:
		for _, e := range n.Elems {
			expr(e)
		}
	case *MapLit:
		for _, en := range n.Entries {
			expr(en.Key)
			expr(en.Value)
		}
	case *StructLit:
		expr(n.Type)
		for _, fi := range n.Fields {
			expr(fi.Value)
		}
	case *Unary:
		expr(n.X)
	case *Binary:
		expr(n.X)
		expr(n.Y)
	case *Paren:
		expr(n.X)
	case *Call:
		expr(n.Fn)
		for _, a := range n.Args {
			expr(a.Value)
		}
	case *Index:
		expr(n.X)
		expr(n.Index)
	case *Selector:
		expr(n.X)
	case *Range:
		expr(n.Lo)
		expr(n.Hi)
	case *FuncLit:
		for _, p := range n.Params {
			expr(p.Default)
		}
		block(n.Body)
		expr(n.ExprBody)
	case *If:
		expr(n.Cond)
		block(n.Then)
		expr(n.Else)
	case *Match:
		expr(n.Subject)
		for _, a := range n.Arms {
			for _, p := range a.Patterns {
				Inspect(p, f)
			}
			expr(a.Guard)
			expr(a.Body)
		}
	case *Block:
		for _, s := range n.Stmts {
			Inspect(s, f)
		}
	case *Try:
		expr(n.X)
	case *Catch:
		expr(n.X)
		block(n.Body)
	case *Spawn:
		Inspect(n.Call, f)
	case *LitPat:
		expr(n.Value)
	case *RangePat:
		expr(n.Lo)
		expr(n.Hi)
	case *VariantPat:
		for _, p := range n.Args {
			Inspect(p, f)
		}
	case *Let:
		expr(n.Value)
	case *Assign:
		expr(n.Target)
		expr(n.Value)
	case *ExprStmt:
		expr(n.X)
	case *Return:
		expr(n.Value)
	case *For:
		expr(n.Iter)
		block(n.Body)
	case *While:
		expr(n.Cond)
		block(n.Body)
	case *Fail:
		expr(n.Value)
	case *Assert:
		expr(n.Cond)
		expr(n.Msg)
	case *Defer:
		Inspect(n.Call, f)
	case *FuncDecl:
		for _, p := range n.Params {
			expr(p.Default)
		}
		for _, r := range n.Requires {
			expr(r)
		}
		for _, e := range n.Ensures {
			expr(e)
		}
		block(n.Body)
		expr(n.ExprBody)
	case *StructDecl:
		for _, fl := range n.Fields {
			expr(fl.Default)
		}
	case *ImplDecl:
		for _, m := range n.Methods {
			Inspect(m, f)
		}
	case *TestDecl:
		block(n.Body)
	case *ConstDecl:
		Inspect(n.Let, f)
	}
}

// ContainsTryOrFail reports whether a function literal body uses try or
// fail outside nested function literals (i.e. whether it can fail).
func ContainsTryOrFail(fl *FuncLit) bool {
	found := false
	var body Node = fl.Body
	if fl.ExprBody != nil {
		body = fl.ExprBody
	}
	Inspect(body, func(n Node) bool {
		switch n.(type) {
		case *FuncLit:
			return false
		case *Try, *Fail:
			found = true
		case *Catch:
			// the operand of catch cannot propagate; only its body can
			c := n.(*Catch)
			Inspect(c.Body, func(m Node) bool {
				switch m.(type) {
				case *FuncLit:
					return false
				case *Try, *Fail:
					found = true
				}
				return !found
			})
			return false
		}
		return !found
	})
	return found
}

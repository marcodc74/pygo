package interp

// Compiler from the (checked) syntax tree to VM bytecode.
//
// Every construct is compiled so that the VM performs the same observable
// operations, in the same order, as the tree-walking interpreter: the same
// values, the same panics at the same positions, a step at exactly the
// places where the interpreter counts one. Anything the compiler does not
// support makes compileFunc fail, and that function then runs on the
// interpreter (the engines can call each other freely).

import (
	"fmt"

	"github.com/marcodc74/pygo/internal/ast"
)

type vmCompileError struct {
	pos ast.Pos
	msg string
}

func (e *vmCompileError) Error() string {
	return fmt.Sprintf("%d:%d: bytecode compiler: %s", e.pos.Line, e.pos.Col, e.msg)
}

type vmLocal struct {
	slot           int
	boxed, mutable bool
}

// vmRef is the result of resolving a name: kind 0 local slot, 1 upvalue,
// 2 module-level or builtin name.
type vmRef struct {
	kind           int
	idx            int
	boxed, mutable bool
}

type vmLoop struct {
	depth   int // stack depth inside the loop (restored by break/continue)
	regions int // try/catch regions open when the loop started
	cont    int // continue target
	breaks  []int
}

type fnComp struct {
	p       *Proto
	parent  *fnComp
	scopes  []map[string]*vmLocal
	boxed   map[string]bool // names captured by nested function literals
	upvals  map[string]int
	upMut   []bool
	globals map[string][2]int // name -> (const index, cache index)
	nglob   int
	depth   int // operand stack depth at the current point
	loops   []*vmLoop
	regions []bool // open try (false) and catch (true) regions
	pos     ast.Pos
}

// compileFunc compiles a function body (params in declaration order, with
// "self" first when hasSelf).
func compileFunc(name string, hasSelf bool, params []*ast.Param, body *ast.Block, exprBody ast.Expr) (p *Proto, err error) {
	defer func() {
		if r := recover(); r != nil {
			ce, ok := r.(*vmCompileError)
			if !ok {
				panic(r)
			}
			p, err = nil, ce
		}
	}()
	c := newFnComp(name, nil, body, exprBody)
	c.params(hasSelf, params)
	c.body(body, exprBody)
	return c.finish(), nil
}

func newFnComp(name string, parent *fnComp, body *ast.Block, exprBody ast.Expr) *fnComp {
	c := &fnComp{
		p:       &Proto{Name: name},
		parent:  parent,
		upvals:  map[string]int{},
		globals: map[string][2]int{},
	}
	c.boxed = map[string]bool{}
	if body != nil {
		capturedNames(body, c.boxed)
	}
	if exprBody != nil {
		capturedNames(exprBody, c.boxed)
	}
	return c
}

// capturedNames adds to out the identifiers used inside function literals
// nested in n: locals with these names live in cells shared with closures.
func capturedNames(n ast.Node, out map[string]bool) {
	ast.Inspect(n, func(x ast.Node) bool {
		if fl, ok := x.(*ast.FuncLit); ok {
			ast.Inspect(fl, func(y ast.Node) bool {
				if id, ok := y.(*ast.Ident); ok {
					out[id.Name] = true
				}
				return true
			})
			return false
		}
		return true
	})
}

func (c *fnComp) finish() *Proto {
	c.p.globals = make([]globalCache, c.nglob)
	return c.p
}

func (c *fnComp) fail(pos ast.Pos, format string, args ...any) {
	panic(&vmCompileError{pos: pos, msg: fmt.Sprintf(format, args...)})
}

// ---------- emission ----------

func (c *fnComp) at(pos ast.Pos) { c.pos = pos }

func (c *fnComp) emit(op Opcode, a, b, cc int) int {
	c.p.Code = append(c.p.Code, Instr{Op: op, A: int32(a), B: int32(b), C: int32(cc)})
	c.p.Pos = append(c.p.Pos, c.pos)
	c.depth += stackEffect(op, a, b, cc)
	if c.depth > c.p.MaxStack {
		c.p.MaxStack = c.depth
	}
	return len(c.p.Code) - 1
}

func (c *fnComp) op(op Opcode) int { return c.emit(op, 0, 0, 0) }

// stackEffect is the change of the operand stack depth made by an
// instruction when execution continues with the next one.
func stackEffect(op Opcode, a, b, cc int) int {
	switch op {
	case OpConst, OpNil, OpTrue, OpFalse, OpLoadLocal, OpLoadCell, OpLoadUpval,
		OpLoadGlobal, OpLoadGlobalOpt, OpMatchPat, OpMakeClosure:
		return 1
	case OpPop, OpStoreLocal, OpStoreCell, OpNewCell, OpStoreUpval, OpStoreGlobal,
		OpPanicImmutable, OpBinary, OpJumpIfFalse, OpJumpIfTrue, OpJumpIfNotNil,
		OpMakeRange, OpStructType, OpIndex, OpReturn, OpFail, OpAssignCompute, OpAssertBin:
		return -1
	case OpPopN:
		return -a
	case OpMakeList:
		return 1 - a
	case OpMakeMap:
		return 1 - 2*a
	case OpStrBuild, OpMakeStruct:
		return 1 - b
	case OpCall, OpSpawn:
		return -(a + cc)
	case OpDefer:
		return -(a + cc + 1)
	case OpAssignField:
		return -2
	case OpAssignIndex:
		return -3
	case OpIterNext:
		return 1 + b
	case OpAssertCollect:
		return -b
	case OpAssertRaise:
		return -b
	}
	return 0
}

func (c *fnComp) jump(op Opcode) int { return c.emit(op, -1, 0, 0) }

// patch makes the jump at pc target the next instruction.
func (c *fnComp) patch(pc int) { c.p.Code[pc].A = int32(len(c.p.Code)) }

func (c *fnComp) aux(x any) int {
	c.p.Aux = append(c.p.Aux, x)
	return len(c.p.Aux) - 1
}

func (c *fnComp) konst(v Value) int {
	c.p.Consts = append(c.p.Consts, v)
	return len(c.p.Consts) - 1
}

func (c *fnComp) newSlot(boxed bool) int {
	s := c.p.NumSlots
	c.p.NumSlots++
	c.p.Boxed = append(c.p.Boxed, boxed)
	return s
}

// ---------- scopes and names ----------

func (c *fnComp) pushScope() { c.scopes = append(c.scopes, map[string]*vmLocal{}) }
func (c *fnComp) popScope()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *fnComp) declare(name string, mutable bool) *vmLocal {
	lv := &vmLocal{slot: c.newSlot(c.boxed[name]), boxed: c.boxed[name], mutable: mutable}
	c.scopes[len(c.scopes)-1][name] = lv
	return lv
}

// store pops the top of the stack into a freshly declared local.
func (c *fnComp) initLocal(lv *vmLocal) {
	if lv.boxed {
		c.emit(OpNewCell, lv.slot, 0, 0)
	} else {
		c.emit(OpStoreLocal, lv.slot, 0, 0)
	}
}

func (c *fnComp) resolve(name string) vmRef {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if lv := c.scopes[i][name]; lv != nil {
			return vmRef{kind: 0, idx: lv.slot, boxed: lv.boxed, mutable: lv.mutable}
		}
	}
	if c.parent == nil {
		return vmRef{kind: 2}
	}
	if i, ok := c.upvals[name]; ok {
		return vmRef{kind: 1, idx: i, boxed: true, mutable: c.upMut[i]}
	}
	r := c.parent.resolve(name)
	switch r.kind {
	case 0:
		if !r.boxed {
			c.fail(c.pos, "captured variable '%s' is not boxed", name)
		}
		c.p.Upvals = append(c.p.Upvals, upvalDesc{fromLocal: true, index: r.idx})
	case 1:
		c.p.Upvals = append(c.p.Upvals, upvalDesc{fromLocal: false, index: r.idx})
	default:
		return r
	}
	i := len(c.p.Upvals) - 1
	c.upvals[name] = i
	c.upMut = append(c.upMut, r.mutable)
	return vmRef{kind: 1, idx: i, boxed: true, mutable: r.mutable}
}

func (c *fnComp) global(name string) (int, int) {
	if g, ok := c.globals[name]; ok {
		return g[0], g[1]
	}
	k := c.konst(name)
	g := c.nglob
	c.nglob++
	c.globals[name] = [2]int{k, g}
	return k, g
}

func (c *fnComp) loadRef(r vmRef, name string, opt bool) {
	switch r.kind {
	case 0:
		if r.boxed {
			c.emit(OpLoadCell, r.idx, 0, 0)
		} else {
			c.emit(OpLoadLocal, r.idx, 0, 0)
		}
	case 1:
		c.emit(OpLoadUpval, r.idx, 0, 0)
	default:
		k, g := c.global(name)
		op := OpLoadGlobal
		if opt {
			op = OpLoadGlobalOpt
		}
		c.emit(op, k, g, 0)
	}
}

// ---------- functions ----------

func (c *fnComp) params(hasSelf bool, params []*ast.Param) {
	c.pushScope()
	if hasSelf {
		c.p.ParamSlots = append(c.p.ParamSlots, c.declare("self", false).slot)
	}
	for _, p := range params {
		c.p.ParamSlots = append(c.p.ParamSlots, c.declare(p.Name, false).slot)
	}
}

// body compiles a function body: a block runs its statements (the result
// comes from return), an expression body is the result.
func (c *fnComp) body(body *ast.Block, exprBody ast.Expr) {
	switch {
	case exprBody != nil:
		c.expr(exprBody)
	case body != nil:
		c.block(body)
		c.op(OpNil)
	default:
		c.op(OpNil)
	}
	c.op(OpReturn)
}

func (c *fnComp) funcLit(e *ast.FuncLit) {
	sub := newFnComp("<lambda>", c, e.Body, e.ExprBody)
	sub.p.Lit = e
	sub.pos = e.Pos
	sub.params(false, e.Params)
	sub.body(e.Body, e.ExprBody)
	c.p.Protos = append(c.p.Protos, sub.finish())
	c.at(e.Pos)
	c.emit(OpMakeClosure, len(c.p.Protos)-1, 0, 0)
}

// ---------- statements ----------

// block runs statements in a new scope (execBlock).
func (c *fnComp) block(b *ast.Block) {
	c.pushScope()
	for _, s := range b.Stmts {
		c.stmt(s)
	}
	c.popScope()
}

// blockValue leaves the value of the block's final expression statement
// (or nil) on the stack.
func (c *fnComp) blockValue(b *ast.Block) {
	c.pushScope()
	defer c.popScope()
	for i, s := range b.Stmts {
		if i == len(b.Stmts)-1 {
			if es, ok := s.(*ast.ExprStmt); ok {
				c.at(es.Pos)
				c.op(OpStep)
				c.expr(es.X)
				return
			}
		}
		c.stmt(s)
	}
	c.op(OpNil)
}

func (c *fnComp) stmt(s ast.Stmt) {
	c.at(s.P())
	c.op(OpStep)
	switch s := s.(type) {
	case *ast.ExprStmt:
		c.expr(s.X)
		c.op(OpPop)
	case *ast.Let:
		c.let(s)
	case *ast.Assign:
		c.assign(s)
	case *ast.Return:
		if s.Value != nil {
			c.expr(s.Value)
		} else {
			c.op(OpNil)
		}
		c.at(s.Pos)
		c.op(OpReturn)
	case *ast.Break:
		c.jumpOut(true)
	case *ast.Continue:
		c.jumpOut(false)
	case *ast.While:
		c.while(s)
	case *ast.For:
		c.forLoop(s)
	case *ast.Fail:
		c.expr(s.Value)
		c.at(s.Pos)
		c.emit(OpFail, c.aux(s), 0, 0)
	case *ast.Assert:
		c.assert(s)
	case *ast.Defer:
		npos, nnamed := c.callParts(s.Call)
		c.at(s.Call.Pos)
		c.emit(OpDefer, npos, c.aux(s.Call), nnamed)
	default:
		c.fail(s.P(), "unsupported statement %T", s)
	}
}

func (c *fnComp) let(s *ast.Let) {
	if fl, ok := s.Value.(*ast.FuncLit); ok && c.boxed[s.Name] {
		// a local function literal can refer to itself: its cell exists
		// before the closure is created
		lv := c.declare(s.Name, s.Mutable)
		c.op(OpNil)
		c.emit(OpNewCell, lv.slot, 0, 0)
		c.funcLit(fl)
		if s.Type != nil {
			c.at(s.Pos)
			c.emit(OpLetCheck, c.aux(s), 0, 0)
		}
		c.emit(OpStoreCell, lv.slot, 0, 0)
		return
	}
	c.expr(s.Value)
	if s.Type != nil {
		c.at(s.Pos)
		c.emit(OpLetCheck, c.aux(s), 0, 0)
	}
	c.initLocal(c.declare(s.Name, s.Mutable))
}

func (c *fnComp) assign(s *ast.Assign) {
	switch t := s.Target.(type) {
	case *ast.Ident:
		c.expr(s.Value)
		r := c.resolve(t.Name)
		if s.Op != "=" {
			c.at(t.Pos)
			c.loadRef(r, t.Name, false)
			c.at(s.Pos)
			c.emit(OpAssignCompute, c.aux(s), 0, 0)
		}
		c.at(t.Pos)
		switch {
		case r.kind == 2:
			c.emit(OpStoreGlobal, 0, c.aux(t), 0)
		case !r.mutable:
			c.emit(OpPanicImmutable, c.aux(t), 0, 0)
		case r.kind == 1:
			c.emit(OpStoreUpval, r.idx, 0, 0)
		case r.boxed:
			c.emit(OpStoreCell, r.idx, 0, 0)
		default:
			c.emit(OpStoreLocal, r.idx, 0, 0)
		}
	case *ast.Selector:
		c.expr(s.Value)
		c.expr(t.X)
		c.at(s.Pos)
		c.emit(OpAssignField, c.aux(s), 0, 0)
	case *ast.Index:
		c.expr(s.Value)
		c.expr(t.X)
		c.expr(t.Index)
		c.at(s.Pos)
		c.emit(OpAssignIndex, c.aux(s), 0, 0)
	default:
		c.fail(s.Pos, "unsupported assignment target %T", s.Target)
	}
}

// jumpOut compiles break/continue: it closes the try/catch regions and
// drops the stack values opened inside the loop, then jumps.
func (c *fnComp) jumpOut(isBreak bool) {
	if len(c.loops) == 0 {
		a := 0
		if !isBreak {
			a = 1
		}
		c.emit(OpBreakOutside, a, 0, 0)
		return
	}
	l := c.loops[len(c.loops)-1]
	d := c.depth
	for i := len(c.regions) - 1; i >= l.regions; i-- {
		c.op(OpTryExit)
		if c.regions[i] {
			c.op(OpCatchPop)
		}
	}
	if c.depth > l.depth {
		c.emit(OpPopN, c.depth-l.depth, 0, 0)
	}
	if isBreak {
		l.breaks = append(l.breaks, c.jump(OpJump))
	} else {
		c.emit(OpJump, l.cont, 0, 0)
	}
	c.depth = d
}

func (c *fnComp) while(s *ast.While) {
	head := len(c.p.Code)
	c.at(s.Pos)
	c.op(OpStep)
	c.expr(s.Cond)
	c.at(s.Pos)
	c.emit(OpWhileCond, c.aux(s), 0, 0)
	exit := c.jump(OpJumpIfFalse)
	l := &vmLoop{depth: c.depth, regions: len(c.regions), cont: head}
	c.loops = append(c.loops, l)
	c.block(s.Body)
	c.loops = c.loops[:len(c.loops)-1]
	c.emit(OpJump, head, 0, 0)
	c.patch(exit)
	for _, b := range l.breaks {
		c.patch(b)
	}
}

func (c *fnComp) forLoop(s *ast.For) {
	c.expr(s.Iter)
	c.at(s.Pos)
	c.emit(OpIterInit, c.aux(s), 0, 0)
	base := c.depth // the iterator is on the stack
	head := len(c.p.Code)
	hasKey := 0
	if s.Key != "" {
		hasKey = 1
	}
	next := c.emit(OpIterNext, -1, hasKey, 0)
	c.op(OpStep)
	c.pushScope()
	c.initLocal(c.declare(s.Val, false))
	if s.Key != "" {
		c.initLocal(c.declare(s.Key, false))
	}
	l := &vmLoop{depth: base, regions: len(c.regions), cont: head}
	c.loops = append(c.loops, l)
	c.block(s.Body)
	c.loops = c.loops[:len(c.loops)-1]
	c.popScope()
	c.emit(OpJump, head, 0, 0)
	c.patch(next)
	for _, b := range l.breaks {
		c.patch(b)
	}
	c.depth = base
	c.op(OpPop)
}

func (c *fnComp) assert(s *ast.Assert) {
	if b := assertBinary(s); b != nil {
		c.expr(b.X)
		c.expr(b.Y)
		c.at(s.Pos)
		c.emit(OpAssertBin, c.aux(s), 0, 0)
	} else {
		c.expr(s.Cond)
		c.at(s.Pos)
		c.emit(OpAssertCond, c.aux(s), 0, 0)
	}
	ok := c.jump(OpJumpIfTrue)
	if assertBinary(s) == nil {
		var names []string
		seen := map[string]bool{}
		walkExpr(s.Cond, func(x ast.Expr) {
			if id, isID := x.(*ast.Ident); isID && !seen[id.Name] {
				seen[id.Name] = true
				names = append(names, id.Name)
			}
		})
		for _, name := range names {
			c.loadRef(c.resolve(name), name, true)
		}
		c.emit(OpAssertCollect, c.aux(&assertInfo{s: s, names: names}), len(names), 0)
	}
	hasMsg := 0
	if s.Msg != nil {
		c.expr(s.Msg)
		hasMsg = 1
	}
	c.at(s.Pos)
	c.emit(OpAssertRaise, c.aux(s), hasMsg, 0)
	c.patch(ok)
}

// ---------- expressions ----------

func (c *fnComp) expr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.IntLit:
		c.emit(OpConst, c.konst(e.Value), 0, 0)
	case *ast.FloatLit:
		c.emit(OpConst, c.konst(e.Value), 0, 0)
	case *ast.BoolLit:
		if e.Value {
			c.op(OpTrue)
		} else {
			c.op(OpFalse)
		}
	case *ast.NilLit:
		c.op(OpNil)
	case *ast.StrLit:
		c.strLit(e)
	case *ast.Ident:
		c.at(e.Pos)
		c.loadRef(c.resolve(e.Name), e.Name, false)
	case *ast.Paren:
		c.expr(e.X)
	case *ast.ListLit:
		for _, x := range e.Elems {
			c.expr(x)
		}
		c.emit(OpMakeList, len(e.Elems), 0, 0)
	case *ast.MapLit:
		for _, en := range e.Entries {
			c.expr(en.Key)
			c.at(en.Key.P())
			c.op(OpCheckKey)
			c.expr(en.Value)
		}
		c.emit(OpMakeMap, len(e.Entries), 0, 0)
	case *ast.StructLit:
		c.structLit(e)
	case *ast.Unary:
		c.expr(e.X)
		c.at(e.Pos)
		c.emit(OpUnary, c.aux(e), 0, 0)
	case *ast.Binary:
		c.binary(e)
	case *ast.Range:
		c.expr(e.Lo)
		c.expr(e.Hi)
		c.at(e.Pos)
		c.emit(OpMakeRange, c.aux(e), 0, 0)
	case *ast.Selector:
		c.expr(e.X)
		c.at(e.Pos)
		c.emit(OpSelector, c.aux(e), 0, 0)
	case *ast.Index:
		c.expr(e.X)
		c.expr(e.Index)
		c.at(e.Pos)
		c.emit(OpIndex, c.aux(e), 0, 0)
	case *ast.Call:
		npos, nnamed := c.callParts(e)
		c.at(e.Pos)
		c.emit(OpCall, npos, c.aux(e), nnamed)
	case *ast.FuncLit:
		c.funcLit(e)
	case *ast.If:
		c.ifExpr(e)
	case *ast.Match:
		c.match(e)
	case *ast.Block:
		c.blockValue(e)
	case *ast.Try:
		c.at(e.Pos)
		c.op(OpTryEnter)
		c.regions = append(c.regions, false)
		c.expr(e.X)
		c.regions = c.regions[:len(c.regions)-1]
		c.op(OpTryExit)
	case *ast.Catch:
		c.catch(e)
	case *ast.Spawn:
		npos, nnamed := c.callParts(e.Call)
		c.at(e.Call.Pos)
		c.emit(OpSpawn, npos, c.aux(e.Call), nnamed)
	default:
		c.fail(e.P(), "unsupported expression %T", e)
	}
}

func (c *fnComp) strLit(e *ast.StrLit) {
	if len(e.Parts) == 1 && e.Parts[0].Expr == nil {
		c.emit(OpConst, c.konst(e.Parts[0].Lit), 0, 0)
		return
	}
	a := c.aux(e)
	n := 0
	for i, part := range e.Parts {
		if part.Expr == nil {
			continue
		}
		c.expr(part.Expr)
		c.at(part.Expr.P())
		c.emit(OpFormatPart, a, i, 0)
		n++
	}
	c.emit(OpStrBuild, a, n, 0)
}

func (c *fnComp) structLit(e *ast.StructLit) {
	c.expr(e.Type)
	a := c.aux(e)
	tmp := c.newSlot(false)
	c.at(e.Pos)
	c.emit(OpStructType, a, tmp, 0)
	for k, f := range e.Fields {
		c.at(f.Pos)
		c.emit(OpStructPre, a, k, tmp)
		c.expr(f.Value)
		c.at(f.Pos)
		c.emit(OpStructCheck, a, k, tmp)
	}
	c.at(e.Pos)
	c.emit(OpMakeStruct, a, len(e.Fields), tmp)
}

func (c *fnComp) binary(e *ast.Binary) {
	switch e.Op {
	case "and", "or":
		a := c.aux(e)
		c.expr(e.X)
		c.at(e.Pos)
		c.emit(OpLogic, a, 0, 0)
		short := OpJumpIfFalse
		if e.Op == "or" {
			short = OpJumpIfTrue
		}
		d := c.depth - 1
		j := c.jump(short)
		c.expr(e.Y)
		c.at(e.Pos)
		c.emit(OpLogic, a, 1, 0)
		end := c.jump(OpJump)
		c.patch(j)
		c.depth = d
		if e.Op == "and" {
			c.op(OpFalse)
		} else {
			c.op(OpTrue)
		}
		c.patch(end)
	case "??":
		c.expr(e.X)
		j := c.jump(OpJumpIfNotNil)
		c.expr(e.Y)
		c.patch(j)
	default:
		c.expr(e.X)
		c.expr(e.Y)
		c.at(e.Pos)
		c.emit(OpBinary, c.aux(e), 0, 0)
	}
}

// callParts pushes the callee, the positional arguments, then the named
// ones (evaluated in source order, as the interpreter does).
func (c *fnComp) callParts(call *ast.Call) (npos, nnamed int) {
	c.expr(call.Fn)
	for _, a := range call.Args {
		if a.Name == "" {
			if nnamed > 0 {
				c.fail(a.Pos, "positional argument after a named one")
			}
			npos++
		} else {
			nnamed++
		}
		c.expr(a.Value)
	}
	return npos, nnamed
}

func (c *fnComp) ifExpr(e *ast.If) {
	c.expr(e.Cond)
	c.at(e.Pos)
	c.emit(OpIfCond, c.aux(e), 0, 0)
	d := c.depth - 1
	els := c.jump(OpJumpIfFalse)
	c.blockValue(e.Then)
	end := c.jump(OpJump)
	c.patch(els)
	c.depth = d
	if e.Else != nil {
		c.expr(e.Else)
	} else {
		c.op(OpNil)
	}
	c.patch(end)
}

func (c *fnComp) match(e *ast.Match) {
	c.expr(e.Subject)
	subj := c.newSlot(false)
	c.emit(OpStoreLocal, subj, 0, 0)
	d := c.depth
	var ends []int
	for _, arm := range e.Arms {
		for _, pat := range arm.Patterns {
			c.pushScope()
			mi := &matchInfo{pat: pat, slots: map[string]int{}, boxed: map[string]bool{}}
			for _, name := range patternNames(pat) {
				lv := c.declare(name, false)
				mi.slots[name], mi.boxed[name] = lv.slot, lv.boxed
			}
			c.at(pat.P())
			c.emit(OpMatchPat, c.aux(mi), subj, 0)
			next := []int{c.jump(OpJumpIfFalse)}
			if arm.Guard != nil {
				c.expr(arm.Guard)
				c.at(arm.Guard.P())
				c.emit(OpGuard, c.aux(arm), 0, 0)
				next = append(next, c.jump(OpJumpIfFalse))
			}
			c.expr(arm.Body)
			ends = append(ends, c.jump(OpJump))
			c.popScope()
			c.depth = d
			for _, j := range next {
				c.patch(j)
			}
		}
	}
	c.at(e.Pos)
	c.emit(OpNoMatch, c.aux(e), subj, 0)
	for _, j := range ends {
		c.patch(j)
	}
	c.depth = d + 1
}

// patternNames lists the names a pattern may bind (an IdentPat that turns
// out to be an enum variant binds nothing; its slot then stays nil).
func patternNames(p ast.Pattern) []string {
	var out []string
	var walk func(p ast.Pattern)
	walk = func(p ast.Pattern) {
		switch p := p.(type) {
		case *ast.IdentPat:
			out = append(out, p.Name)
		case *ast.VariantPat:
			for _, a := range p.Args {
				walk(a)
			}
		}
	}
	walk(p)
	return out
}

func (c *fnComp) catch(e *ast.Catch) {
	c.at(e.Pos)
	d := c.depth
	h := c.jump(OpCatchPush)
	c.op(OpTryEnter)
	c.regions = append(c.regions, true)
	c.expr(e.X)
	c.regions = c.regions[:len(c.regions)-1]
	c.op(OpTryExit)
	c.op(OpCatchPop)
	end := c.jump(OpJump)
	c.patch(h)
	c.depth = d + 1 // the error value
	c.pushScope()
	if e.Name != "" {
		c.initLocal(c.declare(e.Name, false))
	} else {
		c.op(OpPop)
	}
	c.blockValue(e.Body)
	c.popScope()
	c.patch(end)
}

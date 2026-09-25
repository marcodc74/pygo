package interp

// Bytecode virtual machine.
//
// Functions are compiled (vm_compile.go) into a Proto: a flat instruction
// list for a stack machine, with locals in numbered slots. The VM reuses the
// whole runtime of the tree-walking interpreter (values, operators, stdlib,
// calls, contracts, defer, spawn), so both engines have identical semantics;
// the differential tests run every program on both and compare results.

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/marcodc74/pygo/internal/ast"
)

type Opcode uint8

const (
	OpNop Opcode = iota
	OpConst
	OpNil
	OpTrue
	OpFalse
	OpPop
	OpPopN
	OpLoadLocal
	OpStoreLocal
	OpLoadCell
	OpStoreCell
	OpNewCell
	OpLoadUpval
	OpStoreUpval
	OpLoadGlobal
	OpLoadGlobalOpt
	OpStoreGlobal
	OpPanicImmutable
	OpBinary
	OpUnary
	OpLogic
	OpJump
	OpJumpIfFalse
	OpJumpIfTrue
	OpJumpIfNotNil
	OpIfCond
	OpWhileCond
	OpStep
	OpMakeList
	OpMakeMap
	OpMakeRange
	OpStrBuild
	OpStructType
	OpStructPre
	OpStructCheck
	OpMakeStruct
	OpSelector
	OpIndex
	OpCall
	OpSpawn
	OpDefer
	OpReturn
	OpFail
	OpTryEnter
	OpTryExit
	OpCatchPush
	OpCatchPop
	OpLetCheck
	OpAssignField
	OpAssignIndex
	OpAssignCompute
	OpIterInit
	OpIterNext
	OpMatchPat
	OpGuard
	OpNoMatch
	OpMakeClosure
	OpAssertBin
	OpAssertCond
	OpAssertCollect
	OpAssertRaise
	OpBreakOutside
	OpFormatPart
	OpCheckKey
	numOpcodes
)

var opNames = [...]string{
	"NOP", "CONST", "NIL", "TRUE", "FALSE", "POP", "POPN", "LOAD_LOCAL", "STORE_LOCAL",
	"LOAD_CELL", "STORE_CELL", "NEW_CELL", "LOAD_UPVAL", "STORE_UPVAL", "LOAD_GLOBAL",
	"LOAD_GLOBAL_OPT", "STORE_GLOBAL", "PANIC_IMMUTABLE", "BINARY", "UNARY", "LOGIC",
	"JUMP", "JUMP_IF_FALSE", "JUMP_IF_TRUE", "JUMP_IF_NOT_NIL", "IF_COND", "WHILE_COND",
	"STEP", "MAKE_LIST", "MAKE_MAP", "MAKE_RANGE", "STR_BUILD", "STRUCT_TYPE",
	"STRUCT_PRE", "STRUCT_CHECK", "MAKE_STRUCT", "SELECTOR", "INDEX", "CALL", "SPAWN",
	"DEFER", "RETURN", "FAIL", "TRY_ENTER", "TRY_EXIT", "CATCH_PUSH", "CATCH_POP",
	"LET_CHECK", "ASSIGN_FIELD", "ASSIGN_INDEX", "ASSIGN_COMPUTE", "ITER_INIT",
	"ITER_NEXT", "MATCH_PAT", "GUARD", "NO_MATCH", "MAKE_CLOSURE", "ASSERT_BIN",
	"ASSERT_COND", "ASSERT_COLLECT", "ASSERT_RAISE", "BREAK_OUTSIDE", "FORMAT_PART",
	"CHECK_KEY",
}

func (o Opcode) String() string {
	if int(o) < len(opNames) {
		return opNames[o]
	}
	return fmt.Sprintf("OP%d", o)
}

// Instr is one instruction; the meaning of A, B, C depends on the opcode.
type Instr struct {
	Op      Opcode
	A, B, C int32
}

type upvalDesc struct {
	FromLocal bool // captured from the enclosing function's slot (else its upvalue)
	Index     int
}

// Proto is a compiled function body.
type Proto struct {
	Name       string
	Code       []Instr
	Pos        []ast.Pos // source position of each instruction
	Consts     []Value
	Aux        []any // AST nodes and tables used by instructions (errors, patterns)
	NumSlots   int
	MaxStack   int      // operand stack depth reached (from the compiler)
	Boxed      []bool   // slot holds a *Cell (captured by a closure)
	SlotNames  []string // variable of each slot ($-names are temporaries)
	ParamSlots []int    // self (if any) then parameters
	Upvals     []upvalDesc
	Protos     []*Proto
	Lit        *ast.FuncLit // for lambdas
	NumGlobals int          // module-level names used (one cache entry each)
	globals    []globalCache
}

// instance returns a copy of p with its own runtime caches: the code and
// tables are shared, the cache of module-level names (which holds values
// of one Interp) is not. Used when the same compiled bytecode runs in
// several interpreters.
func (p *Proto) instance() *Proto {
	q := *p
	q.globals = make([]globalCache, p.NumGlobals)
	if len(p.Protos) > 0 {
		q.Protos = make([]*Proto, len(p.Protos))
		for i, n := range p.Protos {
			q.Protos[i] = n.instance()
		}
	}
	return &q
}

// prepare allocates the runtime caches of p and its nested functions
// (after compiling or decoding).
func (p *Proto) prepare() {
	p.globals = make([]globalCache, p.NumGlobals)
	for _, q := range p.Protos {
		q.prepare()
	}
}

type globalCache struct {
	v atomic.Pointer[Value]
}

// Cell is a boxed variable shared between a function and its closures.
type Cell struct {
	mu sync.Mutex
	v  Value
}

func (c *Cell) get() Value {
	c.mu.Lock()
	v := c.v
	c.mu.Unlock()
	return v
}

func (c *Cell) set(v Value) {
	c.mu.Lock()
	c.v = v
	c.mu.Unlock()
}

// vmMissing marks an identifier that has no value (assert introspection).
type vmMissing struct{}

type vmHandler struct {
	target   int
	stackH   int
	tryDepth int
}

// matchInfo is the Aux entry of OpMatchPat: the pattern and the slot that
// receives each binding.
type matchInfo struct {
	Pat   ast.Pattern
	Names []string // names the pattern may bind
	Slots []int
	Boxed []bool
}

// assertInfo is the Aux entry of OpAssertCollect.
type assertInfo struct {
	S     *ast.Assert
	Names []string
}

// runProto executes a compiled body. slots holds self and the arguments in
// ParamSlots order (nil entries for the rest).
func (th *Thread) runProto(f *Function, p *Proto, args []Value) (Value, error) {
	buf := make([]Value, p.NumSlots+p.MaxStack+1)
	slots := buf[:p.NumSlots:p.NumSlots]
	for i, s := range p.ParamSlots {
		if i < len(args) {
			if p.Boxed[s] {
				slots[s] = &Cell{v: args[i]}
			} else {
				slots[s] = args[i]
			}
		}
	}
	stack := buf[p.NumSlots:p.NumSlots]
	var handlers []vmHandler
	var pending map[string]string
	code := p.Code
	pc := 0

	push := func(v Value) { stack = append(stack, v) }
	pop := func() Value {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v
	}

	for pc < len(code) {
		in := code[pc]
		pc++
		var err error
		switch in.Op {
		case OpNop:
		case OpConst:
			push(p.Consts[in.A])
		case OpNil:
			push(nil)
		case OpTrue:
			push(true)
		case OpFalse:
			push(false)
		case OpPop:
			stack = stack[:len(stack)-1]
		case OpPopN:
			stack = stack[:len(stack)-int(in.A)]
		case OpLoadLocal:
			push(slots[in.A])
		case OpStoreLocal:
			slots[in.A] = pop()
		case OpLoadCell:
			push(slots[in.A].(*Cell).get())
		case OpStoreCell:
			slots[in.A].(*Cell).set(pop())
		case OpNewCell:
			slots[in.A] = &Cell{v: pop()}
		case OpLoadUpval:
			push(f.Upvals[in.A].get())
		case OpStoreUpval:
			f.Upvals[in.A].set(pop())
		case OpLoadGlobal, OpLoadGlobalOpt:
			v, ok := th.vmGlobal(f, p, in)
			if !ok {
				if in.Op == OpLoadGlobalOpt {
					push(vmMissing{})
					break
				}
				err = th.panicAt(p.Pos[pc-1], PName, "", "undefined name '%s'", p.Consts[in.A].(string))
				break
			}
			push(v)
		case OpStoreGlobal:
			id := p.Aux[in.B].(*ast.Ident)
			found, mutable := f.Mod.Env.Set(id.Name, pop())
			if !found {
				err = th.panicAt(id.Pos, PName, "", "undefined name '%s'", id.Name)
			} else if !mutable {
				err = th.panicAt(id.Pos, PType, "declare it with 'var'", "cannot assign to immutable '%s'", id.Name)
			}
		case OpPanicImmutable:
			id := p.Aux[in.A].(*ast.Ident)
			err = th.panicAt(id.Pos, PType, "declare it with 'var'", "cannot assign to immutable '%s'", id.Name)
		case OpBinary:
			y := pop()
			x := pop()
			var r Value
			r, err = th.vmBinary(p.Aux[in.A].(*ast.Binary), x, y)
			if err == nil {
				push(r)
			}
		case OpUnary:
			var r Value
			r, err = th.unary(p.Aux[in.A].(*ast.Unary), pop())
			if err == nil {
				push(r)
			}
		case OpLogic:
			side := "left"
			if in.B == 1 {
				side = "right"
			}
			var b bool
			b, err = th.logicOperand(p.Aux[in.A].(*ast.Binary), pop(), side)
			if err == nil {
				push(b)
			}
		case OpJump:
			pc = int(in.A)
		case OpJumpIfFalse:
			if !pop().(bool) {
				pc = int(in.A)
			}
		case OpJumpIfTrue:
			if pop().(bool) {
				pc = int(in.A)
			}
		case OpJumpIfNotNil:
			if stack[len(stack)-1] != nil {
				pc = int(in.A)
			} else {
				stack = stack[:len(stack)-1]
			}
		case OpIfCond:
			var b bool
			b, err = th.ifCond(p.Aux[in.A].(*ast.If), pop())
			if err == nil {
				push(b)
			}
		case OpWhileCond:
			w := p.Aux[in.A].(*ast.While)
			c := pop()
			b, ok := c.(bool)
			if !ok {
				err = th.panicAt(w.Cond.P(), PType, "conditions must be Bool (no truthiness)", "while condition is %s, not Bool", TypeName(c))
				break
			}
			push(b)
		case OpStep:
			err = th.step(p.Pos[pc-1])
		case OpMakeList:
			n := int(in.A)
			items := make([]Value, n)
			copy(items, stack[len(stack)-n:])
			stack = stack[:len(stack)-n]
			push(NewList(items))
		case OpMakeMap:
			n := int(in.A)
			base := len(stack) - 2*n
			m := NewMap()
			for i := 0; i < n; i++ {
				m.Set(stack[base+2*i], stack[base+2*i+1])
			}
			stack = stack[:base]
			push(m)
		case OpCheckKey:
			err = th.checkKey(p.Pos[pc-1], stack[len(stack)-1])
		case OpFormatPart:
			e := p.Aux[in.A].(*ast.StrLit)
			var s string
			s, err = th.formatPart(e.Parts[in.B], stack[len(stack)-1])
			if err == nil {
				stack[len(stack)-1] = s
			}
		case OpMakeRange:
			hi := pop()
			lo := pop()
			var r Value
			r, err = th.makeRange(p.Aux[in.A].(*ast.Range), lo, hi)
			if err == nil {
				push(r)
			}
		case OpStrBuild:
			n := int(in.B)
			s := vmStrBuild(p.Aux[in.A].(*ast.StrLit), stack[len(stack)-n:])
			stack = stack[:len(stack)-n]
			push(s)
		case OpStructType:
			var st *StructType
			st, err = th.structTypeOf(p.Aux[in.A].(*ast.StructLit), pop())
			if err == nil {
				slots[in.B] = st
			}
		case OpStructPre:
			e := p.Aux[in.A].(*ast.StructLit)
			_, err = th.structFieldIdx(slots[in.C].(*StructType), e.Fields[in.B])
		case OpStructCheck:
			e := p.Aux[in.A].(*ast.StructLit)
			st := slots[in.C].(*StructType)
			f := e.Fields[in.B]
			err = th.structFieldCheck(st, st.FieldIdx[f.Name], f, stack[len(stack)-1])
		case OpMakeStruct:
			e := p.Aux[in.A].(*ast.StructLit)
			st := slots[in.C].(*StructType)
			n := len(e.Fields)
			vals := make([]Value, len(st.Fields))
			set := make([]bool, len(st.Fields))
			base := len(stack) - n
			for k, fi := range e.Fields {
				i := st.FieldIdx[fi.Name]
				vals[i], set[i] = stack[base+k], true
			}
			stack = stack[:base]
			var v Value
			v, err = th.finishStruct(e, st, vals, set)
			if err == nil {
				push(v)
			}
		case OpSelector:
			var v Value
			v, err = th.selector(p.Aux[in.A].(*ast.Selector), pop())
			if err == nil {
				push(v)
			}
		case OpIndex:
			idx := pop()
			x := pop()
			var v Value
			v, err = th.indexValue(p.Aux[in.A].(*ast.Index), x, idx)
			if err == nil {
				push(v)
			}
		case OpCall, OpSpawn, OpDefer:
			c := p.Aux[in.B].(*ast.Call)
			npos := int(in.A)
			nnamed := len(c.Args) - npos
			base := len(stack) - npos - nnamed - 1
			fnv := stack[base]
			var pos []Value
			if npos > 0 {
				if in.Op == OpCall {
					// the callee copies what it keeps (spawn and defer
					// run later and need their own copy)
					pos = stack[base+1 : base+1+npos : base+1+npos]
				} else {
					pos = make([]Value, npos)
					copy(pos, stack[base+1:base+1+npos])
				}
			}
			var named []namedArg
			if nnamed > 0 {
				named = make([]namedArg, 0, nnamed)
				k := base + 1 + npos
				for _, a := range c.Args {
					if a.Name != "" {
						named = append(named, namedArg{a.Name, stack[k]})
						k++
					}
				}
			}
			stack = stack[:base]
			switch in.Op {
			case OpCall:
				th.top().pos = c.Pos
				v, cerr := th.vmCall(fnv, pos, named, c.Pos)
				v, err = th.finishCall(c, v, cerr)
				if err == nil {
					push(v)
				}
			case OpSpawn:
				push(th.spawnCall(fnv, pos, named, c.Pos))
			case OpDefer:
				callPos := c.Pos
				fr := th.top()
				fr.defers = append(fr.defers, func() error {
					_, err := th.callValue(fnv, pos, named, callPos)
					return err
				})
			}
		case OpReturn:
			return pop(), nil
		case OpFail:
			err = th.failWith(p.Aux[in.A].(*ast.Fail), pop())
		case OpTryEnter:
			th.tryDepth++
		case OpTryExit:
			th.tryDepth--
		case OpCatchPush:
			handlers = append(handlers, vmHandler{target: int(in.A), stackH: len(stack), tryDepth: th.tryDepth})
		case OpCatchPop:
			handlers = handlers[:len(handlers)-1]
		case OpLetCheck:
			err = th.letCheck(p.Aux[in.A].(*ast.Let), stack[len(stack)-1])
		case OpAssignField:
			s := p.Aux[in.A].(*ast.Assign)
			obj := pop()
			val := pop()
			err = th.assignField(s, s.Target.(*ast.Selector), obj, val)
		case OpAssignIndex:
			s := p.Aux[in.A].(*ast.Assign)
			key := pop()
			obj := pop()
			val := pop()
			err = th.assignIndex(s, s.Target.(*ast.Index), obj, key, val)
		case OpAssignCompute:
			old := pop()
			val := pop()
			var v Value
			v, err = th.assignCompute(p.Aux[in.A].(*ast.Assign), old, val)
			if err == nil {
				push(v)
			}
		case OpIterInit:
			var it *vmIter
			it, err = th.newIter(p.Aux[in.A].(*ast.For), pop())
			if err == nil {
				push(it)
			}
		case OpIterNext:
			it := stack[len(stack)-1].(*vmIter)
			k, v, ok := it.next()
			if !ok {
				pc = int(in.A)
				break
			}
			if in.B == 1 {
				push(k)
			}
			push(v)
		case OpMatchPat:
			mi := p.Aux[in.A].(*matchInfo)
			binds := map[string]Value{}
			var ok bool
			ok, err = th.matchPattern(th.in.universe, mi.Pat, slots[in.B], binds)
			if err == nil {
				if ok {
					for i, name := range mi.Names {
						v := binds[name]
						if mi.Boxed[i] {
							slots[mi.Slots[i]] = &Cell{v: v}
						} else {
							slots[mi.Slots[i]] = v
						}
					}
				}
				push(ok)
			}
		case OpGuard:
			var b bool
			b, err = th.guardBool(p.Aux[in.A].(*ast.MatchArm), pop())
			if err == nil {
				push(b)
			}
		case OpNoMatch:
			err = th.noMatch(p.Aux[in.A].(*ast.Match), slots[in.B])
		case OpMakeClosure:
			push(th.vmClosure(f, p.Protos[in.A], slots))
		case OpAssertBin:
			s := p.Aux[in.A].(*ast.Assert)
			y := pop()
			x := pop()
			pending = map[string]string{}
			var ok bool
			ok, err = th.assertBinaryValues(assertBinary(s), x, y, pending)
			if err == nil {
				push(ok)
			}
		case OpAssertCond:
			pending = map[string]string{}
			var ok bool
			ok, err = th.assertCond(p.Aux[in.A].(*ast.Assert), pop())
			if err == nil {
				push(ok)
			}
		case OpAssertCollect:
			ai := p.Aux[in.A].(*assertInfo)
			n := len(ai.Names)
			vals := map[string]Value{}
			for i, name := range ai.Names {
				if v := stack[len(stack)-n+i]; v != (vmMissing{}) {
					vals[name] = v
				}
			}
			stack = stack[:len(stack)-n]
			collectIdentValues(ai.S.Cond, func(name string) (Value, bool) {
				v, ok := vals[name]
				return v, ok
			}, pending)
		case OpAssertRaise:
			var msg Value
			if in.B == 1 {
				msg = pop()
			}
			err = th.assertPanic(p.Aux[in.A].(*ast.Assert), pending, msg)
		case OpBreakOutside:
			if in.A == 1 {
				return nil, &continueSig{}
			}
			return nil, &breakSig{}
		default:
			err = th.panicAt(p.Pos[pc-1], PInternal, "", "unknown opcode %s", in.Op)
		}
		if err != nil {
			if fl, ok := err.(*Failure); ok && len(handlers) > 0 {
				h := handlers[len(handlers)-1]
				handlers = handlers[:len(handlers)-1]
				stack = stack[:h.stackH]
				th.tryDepth = h.tryDepth
				push(fl.Err)
				pc = h.target
				continue
			}
			return nil, err
		}
	}
	return nil, nil
}

// vmBinary evaluates a binary operator with fast paths for Int and Float.
func (th *Thread) vmBinary(e *ast.Binary, x, y Value) (Value, error) {
	if a, ok := x.(int64); ok {
		if b, ok := y.(int64); ok {
			switch e.Op {
			case "+":
				r := a + b
				if (a > 0 && b > 0 && r < 0) || (a < 0 && b < 0 && r >= 0) {
					break
				}
				return r, nil
			case "-":
				r := a - b
				if (a >= 0 && b < 0 && r < 0) || (a < 0 && b > 0 && r >= 0) {
					break
				}
				return r, nil
			case "<":
				return a < b, nil
			case "<=":
				return a <= b, nil
			case ">":
				return a > b, nil
			case ">=":
				return a >= b, nil
			case "==":
				return a == b, nil
			case "!=":
				return a != b, nil
			case "*":
				if a != 0 && b != 0 && a != -1 && b != -1 && a != math.MinInt64 && b != math.MinInt64 {
					r := a * b
					if r/b == a {
						return r, nil
					}
				}
			case "%":
				if b > 0 {
					return a % b, nil
				}
			case "/":
				if b > 0 {
					return a / b, nil
				}
			}
		}
	} else if a, ok := x.(float64); ok {
		if b, ok := y.(float64); ok {
			switch e.Op {
			case "+":
				return a + b, nil
			case "-":
				return a - b, nil
			case "*":
				return a * b, nil
			case "<":
				return a < b, nil
			case "<=":
				return a <= b, nil
			case ">":
				return a > b, nil
			case ">=":
				return a >= b, nil
			}
		}
	}
	return th.binaryValues(e, x, y)
}

// vmGlobal resolves a module-level (or builtin) name, caching it once the
// module has finished loading (module members never change afterwards).
func (th *Thread) vmGlobal(f *Function, p *Proto, in Instr) (Value, bool) {
	gc := &p.globals[in.B]
	if v := gc.v.Load(); v != nil {
		return *v, true
	}
	name := p.Consts[in.A].(string)
	v, ok := f.Mod.Env.Get(name)
	if ok && f.Mod.loaded.Load() {
		gc.v.Store(&v)
	}
	return v, ok
}

// vmCall calls a function value, running compiled bodies directly.
func (th *Thread) vmCall(fnv Value, pos []Value, named []namedArg, at ast.Pos) (Value, error) {
	return th.callValue(fnv, pos, named, at)
}

func (th *Thread) vmClosure(f *Function, p *Proto, slots []Value) *Function {
	lit := p.Lit
	fallible := lit.Fallible
	if !fallible {
		if v, ok := lambdaFallible.Load(lit); ok {
			fallible = v.(bool)
		} else {
			fallible = ast.ContainsTryOrFail(lit)
			lambdaFallible.Store(lit, fallible)
		}
	}
	up := make([]*Cell, len(p.Upvals))
	for i, u := range p.Upvals {
		if u.FromLocal {
			up[i] = slots[u.Index].(*Cell)
		} else {
			up[i] = f.Upvals[u.Index]
		}
	}
	return &Function{
		Name: "<lambda>", Params: lit.Params, Ret: lit.Ret, Fallible: fallible,
		Body: lit.Body, ExprBody: lit.ExprBody, Mod: f.Mod, Pos: lit.Pos,
		Proto: p, Upvals: up, Env: f.Mod.Env,
	}
}

// vmStrBuild joins the literal parts of e with the formatted values of its
// interpolated parts (already converted to strings by OpFormatPart).
func vmStrBuild(e *ast.StrLit, vals []Value) string {
	var b strings.Builder
	k := 0
	for _, part := range e.Parts {
		if part.Expr == nil {
			b.WriteString(part.Lit)
			continue
		}
		b.WriteString(vals[k].(string))
		k++
	}
	return b.String()
}

// ---------- iteration (same order and snapshots as the interpreter) ----------

type vmIter struct {
	kind   int // 0 range, 1 list, 2 map, 3 string, 4 chan
	hasKey bool
	i      int
	n      int64
	end    int64
	items  []Value
	keys   []Value
	runes  []rune
	ch     *Chan
	count  int64
}

func (th *Thread) newIter(s *ast.For, it Value) (*vmIter, error) {
	hasKey := s.Key != ""
	switch c := it.(type) {
	case *RangeVal:
		return &vmIter{kind: 0, n: c.Lo, end: c.End(), hasKey: hasKey}, nil
	case *List:
		return &vmIter{kind: 1, items: c.Snapshot(), hasKey: hasKey}, nil
	case *Map:
		ks, vs := c.Items()
		return &vmIter{kind: 2, keys: ks, items: vs, hasKey: hasKey}, nil
	case string:
		return &vmIter{kind: 3, runes: []rune(c), hasKey: hasKey}, nil
	case *Chan:
		return &vmIter{kind: 4, ch: c, hasKey: hasKey}, nil
	}
	return nil, th.panicAt(s.Iter.P(), PType, "iterate over List, Map, Str, Range (a..b) or Chan", "cannot iterate over %s", TypeName(it))
}

// next returns (key, value, ok). For maps without a key variable the value
// is the key, as in the interpreter.
func (it *vmIter) next() (Value, Value, bool) {
	switch it.kind {
	case 0:
		if it.n >= it.end {
			return nil, nil, false
		}
		k, v := int64(it.i), it.n
		it.i++
		it.n++
		return k, v, true
	case 1:
		if it.i >= len(it.items) {
			return nil, nil, false
		}
		k, v := int64(it.i), it.items[it.i]
		it.i++
		return k, v, true
	case 2:
		if it.i >= len(it.keys) {
			return nil, nil, false
		}
		k, v := it.keys[it.i], it.items[it.i]
		it.i++
		if !it.hasKey {
			return nil, k, true
		}
		return k, v, true
	case 3:
		if it.i >= len(it.runes) {
			return nil, nil, false
		}
		k, v := int64(it.i), string(it.runes[it.i])
		it.i++
		return k, v, true
	case 4:
		v, ok := <-it.ch.ch
		if !ok {
			return nil, nil, false
		}
		k := it.count
		it.count++
		return k, v, true
	}
	return nil, nil, false
}

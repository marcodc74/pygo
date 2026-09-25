package interp

// Disassembler: the bytecode of a program in a form an agent (or a
// person) can read, as text or JSON (pygo disasm).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/printer"
)

// DisasmFunc is one compiled function.
type DisasmFunc struct {
	Name     string        `json:"name"`
	File     string        `json:"file"`
	Line     int           `json:"line"`
	Slots    []string      `json:"slots"` // variable of each slot; $-names are temporaries
	Boxed    []int         `json:"boxed,omitempty"`
	Upvals   []string      `json:"upvalues,omitempty"`
	MaxStack int           `json:"max_stack"`
	Code     []DisasmInstr `json:"code"`
	Nested   []DisasmFunc  `json:"nested,omitempty"` // function literals, referenced by MAKE_CLOSURE
	Note     string        `json:"note,omitempty"`
}

// DisasmInstr is one instruction.
type DisasmInstr struct {
	PC   int    `json:"pc"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Op   string `json:"op"`
	A    int32  `json:"a"`
	B    int32  `json:"b"`
	C    int32  `json:"c"`
	Note string `json:"note,omitempty"`
}

// Disassemble lists the bytecode of every function and test of prog, in
// file and source order. Functions without bytecode (left to the
// interpreter) are listed with a note.
func Disassemble(prog *loader.Program, c *Compiled) []DisasmFunc {
	var out []DisasmFunc
	for _, file := range sortedFiles(prog) {
		for _, d := range prog.Files[file].Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				out = append(out, disasmTop(d.Name, file, d.Pos, c.Funcs[d]))
			case *ast.ImplDecl:
				for _, md := range d.Methods {
					out = append(out, disasmTop(d.Type+"."+md.Name, file, md.Pos, c.Funcs[md]))
				}
			case *ast.TestDecl:
				out = append(out, disasmTop(fmt.Sprintf("test %q", d.Name), file, d.Pos, c.Tests[d]))
			}
		}
	}
	return out
}

func disasmTop(name, file string, pos ast.Pos, p *Proto) DisasmFunc {
	if p == nil {
		return DisasmFunc{Name: name, File: file, Line: pos.Line, Note: "not compiled: runs on the interpreter"}
	}
	f := disasmProto(p, file, pos)
	f.Name = name
	return f
}

func disasmProto(p *Proto, file string, pos ast.Pos) DisasmFunc {
	f := DisasmFunc{Name: p.Name, File: file, Line: pos.Line, Slots: p.SlotNames, MaxStack: p.MaxStack}
	for i, b := range p.Boxed {
		if b {
			f.Boxed = append(f.Boxed, i)
		}
	}
	for _, u := range p.Upvals {
		if u.FromLocal {
			f.Upvals = append(f.Upvals, fmt.Sprintf("slot %d", u.Index))
		} else {
			f.Upvals = append(f.Upvals, fmt.Sprintf("upvalue %d", u.Index))
		}
	}
	for pc, in := range p.Code {
		ip := p.Pos[pc]
		f.Code = append(f.Code, DisasmInstr{PC: pc, Line: ip.Line, Col: ip.Col, Op: in.Op.String(),
			A: in.A, B: in.B, C: in.C, Note: instrNote(p, in)})
	}
	for _, q := range p.Protos {
		lp := pos
		if q.Lit != nil {
			lp = q.Lit.Pos
		}
		f.Nested = append(f.Nested, disasmProto(q, file, lp))
	}
	return f
}

func slotName(p *Proto, s int32) string {
	if int(s) < len(p.SlotNames) {
		return p.SlotNames[s]
	}
	return fmt.Sprintf("slot %d", s)
}

// instrNote explains the operands of an instruction.
func instrNote(p *Proto, in Instr) string {
	aux := func(i int32) any {
		if int(i) >= 0 && int(i) < len(p.Aux) {
			return p.Aux[i]
		}
		return nil
	}
	switch in.Op {
	case OpConst:
		return Repr(p.Consts[in.A])
	case OpLoadLocal, OpStoreLocal, OpLoadCell, OpStoreCell, OpNewCell:
		return slotName(p, in.A)
	case OpLoadGlobal, OpLoadGlobalOpt:
		return fmt.Sprint(p.Consts[in.A])
	case OpStoreGlobal:
		if id, ok := aux(in.B).(*ast.Ident); ok {
			return id.Name
		}
	case OpPanicImmutable:
		if id, ok := aux(in.A).(*ast.Ident); ok {
			return id.Name
		}
	case OpBinary, OpLogic:
		if b, ok := aux(in.A).(*ast.Binary); ok {
			return b.Op
		}
	case OpUnary:
		if u, ok := aux(in.A).(*ast.Unary); ok {
			return u.Op
		}
	case OpJump, OpJumpIfFalse, OpJumpIfTrue, OpJumpIfNotNil:
		return fmt.Sprintf("-> %d", in.A)
	case OpIterNext:
		return fmt.Sprintf("exit -> %d", in.A)
	case OpCatchPush:
		return fmt.Sprintf("handler -> %d", in.A)
	case OpCall, OpSpawn, OpDefer:
		if c, ok := aux(in.B).(*ast.Call); ok {
			return fmt.Sprintf("%s (%d positional, %d named)", printer.Expr(c.Fn), in.A, in.C)
		}
	case OpSelector:
		if s, ok := aux(in.A).(*ast.Selector); ok {
			return "." + s.Name
		}
	case OpStructType, OpMakeStruct:
		if s, ok := aux(in.A).(*ast.StructLit); ok {
			return printer.Expr(s.Type)
		}
	case OpStructPre, OpStructCheck:
		if s, ok := aux(in.A).(*ast.StructLit); ok && int(in.B) < len(s.Fields) {
			return printer.Expr(s.Type) + "." + s.Fields[in.B].Name
		}
	case OpFormatPart:
		if s, ok := aux(in.A).(*ast.StrLit); ok && int(in.B) < len(s.Parts) && s.Parts[in.B].Format != "" {
			return ":" + s.Parts[in.B].Format
		}
	case OpMatchPat:
		if mi, ok := aux(in.A).(*matchInfo); ok {
			return printer.Pattern(mi.Pat)
		}
	case OpMakeClosure:
		return fmt.Sprintf("nested %d", in.A)
	case OpAssertBin, OpAssertCond, OpAssertRaise:
		if s, ok := aux(in.A).(*ast.Assert); ok {
			return printer.Expr(s.Cond)
		}
	case OpAssertCollect:
		if ai, ok := aux(in.A).(*assertInfo); ok {
			return strings.Join(ai.Names, ", ")
		}
	case OpLetCheck:
		if s, ok := aux(in.A).(*ast.Let); ok {
			return s.Name + ": " + printer.Type(s.Type)
		}
	case OpAssignField, OpAssignIndex, OpAssignCompute:
		if s, ok := aux(in.A).(*ast.Assign); ok {
			return printer.Expr(s.Target) + " " + s.Op
		}
	case OpBreakOutside:
		if in.A == 1 {
			return "continue"
		}
		return "break"
	}
	return ""
}

// DisasmText renders functions as an assembly-like listing.
func DisasmText(funcs []DisasmFunc) string {
	var b strings.Builder
	var render func(f DisasmFunc, indent string)
	render = func(f DisasmFunc, indent string) {
		fmt.Fprintf(&b, "%sfn %s  (%s:%d)\n", indent, f.Name, f.File, f.Line)
		if f.Note != "" {
			fmt.Fprintf(&b, "%s    ; %s\n\n", indent, f.Note)
			return
		}
		slots := make([]string, len(f.Slots))
		boxed := map[int]bool{}
		for _, i := range f.Boxed {
			boxed[i] = true
		}
		for i, s := range f.Slots {
			slots[i] = fmt.Sprintf("%d=%s", i, s)
			if boxed[i] {
				slots[i] += "*"
			}
		}
		fmt.Fprintf(&b, "%s    ; slots: %s  (* = shared cell)  max stack: %d\n", indent, strings.Join(slots, " "), f.MaxStack)
		if len(f.Upvals) > 0 {
			fmt.Fprintf(&b, "%s    ; upvalues: %s\n", indent, strings.Join(f.Upvals, ", "))
		}
		for _, in := range f.Code {
			operands := operandText(in)
			line := fmt.Sprintf("%s  %4d  %4d:%-3d  %-16s %-12s", indent, in.PC, in.Line, in.Col, in.Op, operands)
			if in.Note != "" {
				line += "; " + in.Note
			}
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
		b.WriteString("\n")
		for i, n := range f.Nested {
			n.Name = fmt.Sprintf("%s/nested %d <lambda>", f.Name, i)
			render(n, indent+"    ")
		}
	}
	for _, f := range funcs {
		render(f, "")
	}
	return b.String()
}

// operandText shows the operands an opcode uses.
func operandText(in DisasmInstr) string {
	switch in.Op {
	case "NOP", "NIL", "TRUE", "FALSE", "POP", "STEP", "TRY_ENTER", "TRY_EXIT", "CATCH_POP", "RETURN", "CHECK_KEY":
		return ""
	case "CALL", "SPAWN", "DEFER", "STRUCT_PRE", "STRUCT_CHECK", "MAKE_STRUCT":
		return fmt.Sprintf("%d %d %d", in.A, in.B, in.C)
	case "LOAD_GLOBAL", "LOAD_GLOBAL_OPT", "STORE_GLOBAL", "ITER_NEXT", "FORMAT_PART", "STR_BUILD",
		"STRUCT_TYPE", "MATCH_PAT", "NO_MATCH", "ASSERT_RAISE", "ASSERT_COLLECT", "LOGIC":
		return fmt.Sprintf("%d %d", in.A, in.B)
	}
	return fmt.Sprint(in.A)
}

// funcNames returns the sorted names of the listed functions (for errors).
func funcNames(funcs []DisasmFunc) []string {
	var out []string
	for _, f := range funcs {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// FilterDisasm keeps the functions named name (or "Type.name").
func FilterDisasm(funcs []DisasmFunc, name string) ([]DisasmFunc, []string) {
	var out []DisasmFunc
	for _, f := range funcs {
		if f.Name == name || strings.HasSuffix(f.Name, "."+name) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, funcNames(funcs)
	}
	return out, nil
}

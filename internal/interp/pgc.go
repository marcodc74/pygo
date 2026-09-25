package interp

// The .pgc file: a compiled Pygo program.
//
//	"PYGC"                      magic
//	uvarint  format version     (pgcVersion)
//	uvarint  opcode count       (numOpcodes; the VM rejects other sets)
//	string   pygo version       (informational)
//	string   source hash        (sha256 of the sources, hex)
//	graph    the program        (pgcProgram, see below)
//	[32]byte sha256 of everything before (integrity)
//
// The program is its checked syntax tree (declarations, types,
// signatures) plus the bytecode of every function and test. It is
// written as an object graph: each pointer is encoded once and later
// occurrences refer back to it, so the syntax nodes shared by the
// declarations and the bytecode stay shared. Strings go through a table
// (each distinct string is written once), instructions and source
// positions have compact encodings (positions as deltas). Encoding is
// deterministic: the same source gives the same bytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/loader"
)

const (
	pgcMagic   = "PYGC"
	pgcVersion = 1
)

// Compiled holds the bytecode of a program's functions and tests (from a
// .pgc file or from CompileProgram). An Interp given Options.Compiled uses
// it instead of compiling; it can be shared by several Interps.
type Compiled struct {
	Funcs map[*ast.FuncDecl]*Proto
	Tests map[*ast.TestDecl]*Proto
}

type pgcProgram struct {
	Main    string
	Order   []string
	Modules []*pgcModule
}

type pgcModule struct {
	Path    string
	Decls   []ast.Decl
	Imports []pgcImport // sorted by Path
	Funcs   []pgcFunc   // top-level functions and methods, in source order
	Tests   []pgcTest
}

type pgcImport struct{ Path, Target string }

type pgcFunc struct {
	Decl  *ast.FuncDecl
	Proto *Proto // nil: the function runs on the interpreter
}

type pgcTest struct {
	Decl  *ast.TestDecl
	Proto *Proto
}

// PgcInfo describes a .pgc file.
type PgcInfo struct {
	Version    int    `json:"format_version"`
	Pygo       string `json:"pygo_version"`
	SourceHash string `json:"source_hash"`
}

// CompileProgram compiles every function, method and test of prog to
// bytecode. fallbacks lists the ones left to the interpreter.
func CompileProgram(prog *loader.Program) (c *Compiled, fallbacks []string) {
	c = &Compiled{Funcs: map[*ast.FuncDecl]*Proto{}, Tests: map[*ast.TestDecl]*Proto{}}
	for _, file := range sortedFiles(prog) {
		for _, d := range prog.Files[file].Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				c.Funcs[d], fallbacks = compileDecl(d, d.Name, fallbacks)
			case *ast.ImplDecl:
				for _, md := range d.Methods {
					c.Funcs[md], fallbacks = compileDecl(md, d.Type+"."+md.Name, fallbacks)
				}
			case *ast.TestDecl:
				name := fmt.Sprintf("test %q", d.Name)
				p, err := compileFunc(name, false, nil, d.Body, nil)
				if err != nil {
					fallbacks = append(fallbacks, name+": "+err.Error())
				}
				c.Tests[d] = p
			}
		}
	}
	return c, fallbacks
}

func compileDecl(d *ast.FuncDecl, name string, fallbacks []string) (*Proto, []string) {
	p, err := compileFunc(name, d.HasSelf, d.Params, d.Body, d.ExprBody)
	if err != nil {
		fallbacks = append(fallbacks, name+": "+err.Error())
	}
	return p, fallbacks
}

func sortedFiles(prog *loader.Program) []string {
	files := make([]string, 0, len(prog.Files))
	for f := range prog.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// SourceHash is the sha256 (hex) of the program's sources.
func SourceHash(prog *loader.Program) string {
	h := sha256.New()
	for _, f := range sortedFiles(prog) {
		fmt.Fprintf(h, "%d:%s%d:%s", len(f), f, len(prog.Sources[f]), prog.Sources[f])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EncodePgc writes prog and its bytecode as a .pgc file.
func EncodePgc(prog *loader.Program, c *Compiled, pygoVersion string) ([]byte, error) {
	root := &pgcProgram{Main: prog.Main, Order: prog.Order}
	for _, file := range sortedFiles(prog) {
		m := &pgcModule{Path: file, Decls: prog.Files[file].Decls}
		imps := prog.Imports[file]
		for p, target := range imps {
			m.Imports = append(m.Imports, pgcImport{p, target})
		}
		sort.Slice(m.Imports, func(i, j int) bool { return m.Imports[i].Path < m.Imports[j].Path })
		for _, d := range m.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				m.Funcs = append(m.Funcs, pgcFunc{d, c.Funcs[d]})
			case *ast.ImplDecl:
				for _, md := range d.Methods {
					m.Funcs = append(m.Funcs, pgcFunc{md, c.Funcs[md]})
				}
			case *ast.TestDecl:
				m.Tests = append(m.Tests, pgcTest{d, c.Tests[d]})
			}
		}
		root.Modules = append(root.Modules, m)
	}
	var out bytes.Buffer
	out.WriteString(pgcMagic)
	w := &pgcWriter{buf: &out, ids: map[pgcPtrKey]int{}, strings: map[string]int{}}
	w.uvarint(pgcVersion)
	w.uvarint(uint64(numOpcodes))
	w.str(pygoVersion)
	w.str(SourceHash(prog))
	if err := w.value(reflect.ValueOf(root).Elem()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(out.Bytes())
	out.Write(sum[:])
	return out.Bytes(), nil
}

// IsPgc reports whether data starts like a .pgc file.
func IsPgc(data []byte) bool { return bytes.HasPrefix(data, []byte(pgcMagic)) }

// DecodePgc reads a .pgc file back into a program and its bytecode.
func DecodePgc(data []byte) (*loader.Program, *Compiled, *PgcInfo, error) {
	if !IsPgc(data) {
		return nil, nil, nil, errors.New("not a Pygo bytecode file (missing PYGC header)")
	}
	if len(data) < len(pgcMagic)+sha256.Size {
		return nil, nil, nil, errors.New("truncated .pgc file")
	}
	body, sum := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	r := &pgcReader{data: body, pos: len(pgcMagic)}
	info := &PgcInfo{}
	ver, err := r.uvarint()
	if err != nil {
		return nil, nil, nil, err
	}
	info.Version = int(ver)
	if ver != pgcVersion {
		return nil, nil, nil, fmt.Errorf("bytecode format version %d is not supported (this pygo reads version %d); recompile with pygo compile", ver, pgcVersion)
	}
	if want := sha256.Sum256(body); !bytes.Equal(want[:], sum) {
		return nil, nil, nil, errors.New("corrupted .pgc file (checksum mismatch); recompile with pygo compile")
	}
	nops, err := r.uvarint()
	if err != nil {
		return nil, nil, nil, err
	}
	if nops != uint64(numOpcodes) {
		return nil, nil, nil, fmt.Errorf("bytecode uses %d opcodes, this VM has %d; recompile with this pygo", nops, numOpcodes)
	}
	if info.Pygo, err = r.str(); err != nil {
		return nil, nil, nil, err
	}
	if info.SourceHash, err = r.str(); err != nil {
		return nil, nil, nil, err
	}
	root := &pgcProgram{}
	if err := r.value(reflect.ValueOf(root).Elem(), 0); err != nil {
		return nil, nil, nil, fmt.Errorf("corrupted .pgc file: %v", err)
	}
	if r.pos != len(r.data) {
		return nil, nil, nil, errors.New("corrupted .pgc file: trailing data")
	}
	prog := &loader.Program{Main: root.Main, Order: root.Order, Files: map[string]*ast.File{},
		Sources: map[string]string{}, Imports: map[string]map[string]string{}}
	c := &Compiled{Funcs: map[*ast.FuncDecl]*Proto{}, Tests: map[*ast.TestDecl]*Proto{}}
	for _, m := range root.Modules {
		if m == nil {
			return nil, nil, nil, errors.New("corrupted .pgc file: missing module")
		}
		prog.Files[m.Path] = &ast.File{Name: m.Path, Decls: m.Decls}
		imps := map[string]string{}
		for _, im := range m.Imports {
			imps[im.Path] = im.Target
		}
		prog.Imports[m.Path] = imps
		for _, f := range m.Funcs {
			if f.Decl == nil {
				return nil, nil, nil, errors.New("corrupted .pgc file: function without declaration")
			}
			if f.Proto != nil {
				if err := validateProto(f.Proto, 0); err != nil {
					return nil, nil, nil, fmt.Errorf("corrupted .pgc file: %s: %v", f.Decl.Name, err)
				}
				f.Proto.prepare()
			}
			c.Funcs[f.Decl] = f.Proto
		}
		for _, t := range m.Tests {
			if t.Decl == nil {
				return nil, nil, nil, errors.New("corrupted .pgc file: test without declaration")
			}
			if t.Proto != nil {
				if err := validateProto(t.Proto, 0); err != nil {
					return nil, nil, nil, fmt.Errorf("corrupted .pgc file: test %q: %v", t.Decl.Name, err)
				}
				t.Proto.prepare()
			}
			c.Tests[t.Decl] = t.Proto
		}
	}
	if prog.Files[prog.Main] == nil {
		return nil, nil, nil, errors.New("corrupted .pgc file: main module missing")
	}
	return prog, c, info, nil
}

// validateProto checks the structure of decoded bytecode: sizes, opcodes,
// and the operands that index slots, constants, tables, jump targets and
// nested functions. (The VM runs on Go's memory-safe runtime, so a file
// crafted to pass these checks can at worst stop with an internal-error
// panic; the checks turn every accidental or simple corruption into a
// clear load error.)
func validateProto(p *Proto, depth int) error {
	const limit = 1 << 20
	switch {
	case depth > 200:
		return errors.New("functions nested too deeply")
	case p.NumSlots < 0 || p.NumSlots > limit || p.MaxStack < 0 || p.MaxStack > limit ||
		p.NumGlobals < 0 || p.NumGlobals > limit:
		return errors.New("bad sizes")
	case len(p.Boxed) != p.NumSlots || len(p.Pos) != len(p.Code):
		return errors.New("inconsistent tables")
	}
	for _, s := range p.ParamSlots {
		if s < 0 || s >= p.NumSlots {
			return errors.New("bad parameter slot")
		}
	}
	for _, u := range p.Upvals {
		if u.Index < 0 {
			return errors.New("bad upvalue")
		}
	}
	n := len(p.Code)
	in := func(x int32, size int) bool { return x >= 0 && int(x) < size }
	for pc, ins := range p.Code {
		ok := true
		switch ins.Op {
		case OpConst:
			ok = in(ins.A, len(p.Consts))
		case OpLoadLocal, OpStoreLocal, OpLoadCell, OpStoreCell, OpNewCell:
			ok = in(ins.A, p.NumSlots)
		case OpLoadUpval, OpStoreUpval:
			ok = in(ins.A, len(p.Upvals))
		case OpLoadGlobal, OpLoadGlobalOpt:
			ok = in(ins.A, len(p.Consts)) && in(ins.B, p.NumGlobals)
			if ok {
				_, ok = p.Consts[ins.A].(string)
			}
		case OpJump, OpJumpIfFalse, OpJumpIfTrue, OpJumpIfNotNil, OpIterNext, OpCatchPush:
			ok = ins.A >= 0 && int(ins.A) <= n
		case OpMakeClosure:
			ok = in(ins.A, len(p.Protos))
		case OpStructType, OpNoMatch, OpMatchPat:
			ok = in(ins.A, len(p.Aux)) && in(ins.B, p.NumSlots)
		case OpStructPre, OpStructCheck, OpMakeStruct:
			ok = in(ins.A, len(p.Aux)) && in(ins.C, p.NumSlots)
		case OpCall, OpSpawn, OpDefer, OpStoreGlobal:
			ok = in(ins.B, len(p.Aux))
		case OpBinary, OpUnary, OpLogic, OpIfCond, OpWhileCond, OpSelector, OpIndex, OpMakeRange,
			OpStrBuild, OpFormatPart, OpFail, OpLetCheck, OpAssignField, OpAssignIndex, OpAssignCompute,
			OpIterInit, OpGuard, OpAssertBin, OpAssertCond, OpAssertCollect, OpAssertRaise, OpPanicImmutable:
			ok = in(ins.A, len(p.Aux))
		default:
			ok = ins.Op < numOpcodes
		}
		if !ok {
			return fmt.Errorf("bad instruction %d (%s)", pc, ins.Op)
		}
	}
	for _, q := range p.Protos {
		if q == nil {
			return errors.New("missing nested function")
		}
		if err := validateProto(q, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// ---------- object graph encoding ----------

// pgcTypes lists the concrete types that can appear in interface fields
// (their index is written). Append only; changing the list requires a
// new pgcVersion.
var pgcTypes = []reflect.Type{
	reflect.TypeOf(int64(0)), reflect.TypeOf(float64(0)), reflect.TypeOf(""), reflect.TypeOf(false),
	// expressions
	reflect.TypeOf(&ast.Ident{}), reflect.TypeOf(&ast.IntLit{}), reflect.TypeOf(&ast.FloatLit{}),
	reflect.TypeOf(&ast.BoolLit{}), reflect.TypeOf(&ast.NilLit{}), reflect.TypeOf(&ast.StrLit{}),
	reflect.TypeOf(&ast.ListLit{}), reflect.TypeOf(&ast.MapLit{}), reflect.TypeOf(&ast.StructLit{}),
	reflect.TypeOf(&ast.Unary{}), reflect.TypeOf(&ast.Binary{}), reflect.TypeOf(&ast.Call{}),
	reflect.TypeOf(&ast.Index{}), reflect.TypeOf(&ast.Selector{}), reflect.TypeOf(&ast.Range{}),
	reflect.TypeOf(&ast.FuncLit{}), reflect.TypeOf(&ast.If{}), reflect.TypeOf(&ast.Match{}),
	reflect.TypeOf(&ast.Block{}), reflect.TypeOf(&ast.Try{}), reflect.TypeOf(&ast.Catch{}),
	reflect.TypeOf(&ast.Spawn{}), reflect.TypeOf(&ast.Paren{}),
	// patterns
	reflect.TypeOf(&ast.WildcardPat{}), reflect.TypeOf(&ast.IdentPat{}), reflect.TypeOf(&ast.LitPat{}),
	reflect.TypeOf(&ast.RangePat{}), reflect.TypeOf(&ast.VariantPat{}),
	// statements
	reflect.TypeOf(&ast.Let{}), reflect.TypeOf(&ast.Assign{}), reflect.TypeOf(&ast.ExprStmt{}),
	reflect.TypeOf(&ast.Return{}), reflect.TypeOf(&ast.Break{}), reflect.TypeOf(&ast.Continue{}),
	reflect.TypeOf(&ast.For{}), reflect.TypeOf(&ast.While{}), reflect.TypeOf(&ast.Fail{}),
	reflect.TypeOf(&ast.Assert{}), reflect.TypeOf(&ast.Defer{}),
	// declarations
	reflect.TypeOf(&ast.FuncDecl{}), reflect.TypeOf(&ast.StructDecl{}), reflect.TypeOf(&ast.EnumDecl{}),
	reflect.TypeOf(&ast.ImplDecl{}), reflect.TypeOf(&ast.ImportDecl{}), reflect.TypeOf(&ast.TestDecl{}),
	reflect.TypeOf(&ast.ConstDecl{}), reflect.TypeOf(&ast.ExternDecl{}),
	// bytecode tables
	reflect.TypeOf(&ast.MatchArm{}), reflect.TypeOf(&matchInfo{}), reflect.TypeOf(&assertInfo{}),
}

var pgcTypeIndex = func() map[reflect.Type]int {
	m := map[reflect.Type]int{}
	for i, t := range pgcTypes {
		m[t] = i
	}
	return m
}()

type pgcPtrKey struct {
	t reflect.Type
	p uintptr
}

type pgcWriter struct {
	buf     *bytes.Buffer
	ids     map[pgcPtrKey]int
	strings map[string]int
}

var (
	typeInstrs = reflect.TypeOf([]Instr(nil))
	typePos    = reflect.TypeOf([]ast.Pos(nil))
	pgcFields  sync.Map // reflect.Type -> []int (exported fields)
)

// exportedFields lists the fields of a struct type that are encoded
// (unexported ones are runtime caches, rebuilt after decoding).
func exportedFields(t reflect.Type) []int {
	if f, ok := pgcFields.Load(t); ok {
		return f.([]int)
	}
	var out []int
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			out = append(out, i)
		}
	}
	pgcFields.Store(t, out)
	return out
}

func (w *pgcWriter) varint(x int64) {
	var b [binary.MaxVarintLen64]byte
	w.buf.Write(b[:binary.PutVarint(b[:], x)])
}

// interned writes a string of the program: 0 + the text the first time,
// then the index of its first occurrence + 1.
func (w *pgcWriter) interned(s string) {
	if i, ok := w.strings[s]; ok {
		w.uvarint(uint64(i) + 1)
		return
	}
	w.strings[s] = len(w.strings)
	w.uvarint(0)
	w.str(s)
}

func (w *pgcWriter) uvarint(x uint64) {
	var b [binary.MaxVarintLen64]byte
	w.buf.Write(b[:binary.PutUvarint(b[:], x)])
}

func (w *pgcWriter) str(s string) {
	w.uvarint(uint64(len(s)))
	w.buf.WriteString(s)
}

// value writes v according to its static type.
func (w *pgcWriter) value(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			w.buf.WriteByte(1)
		} else {
			w.buf.WriteByte(0)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.varint(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		w.uvarint(v.Uint())
	case reflect.Float64:
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v.Float()))
		w.buf.Write(b[:])
	case reflect.String:
		w.interned(v.String())
	case reflect.Slice:
		if v.IsNil() {
			w.uvarint(0)
			return nil
		}
		w.uvarint(uint64(v.Len()) + 1)
		switch v.Type() {
		case typeInstrs:
			for _, in := range v.Interface().([]Instr) {
				w.buf.WriteByte(byte(in.Op))
				w.varint(int64(in.A))
				w.varint(int64(in.B))
				w.varint(int64(in.C))
			}
			return nil
		case typePos:
			prev := ast.Pos{}
			for _, p := range v.Interface().([]ast.Pos) {
				w.varint(int64(p.Line - prev.Line))
				w.varint(int64(p.Col))
				prev = p
			}
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := w.value(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for _, i := range exportedFields(v.Type()) {
			if err := w.value(v.Field(i)); err != nil {
				return err
			}
		}
	case reflect.Pointer:
		if v.IsNil() {
			w.uvarint(0)
			return nil
		}
		key := pgcPtrKey{v.Type(), v.Pointer()}
		if id, ok := w.ids[key]; ok {
			w.uvarint(uint64(id) + 2)
			return nil
		}
		w.ids[key] = len(w.ids)
		w.uvarint(1)
		return w.value(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			w.uvarint(0)
			return nil
		}
		e := v.Elem()
		idx, ok := pgcTypeIndex[e.Type()]
		if !ok {
			return fmt.Errorf("pgc: cannot encode a value of type %s", e.Type())
		}
		w.uvarint(uint64(idx) + 1)
		return w.value(e)
	default:
		return fmt.Errorf("pgc: cannot encode kind %s (%s)", v.Kind(), v.Type())
	}
	return nil
}

type pgcReader struct {
	data    []byte
	pos     int
	objs    []reflect.Value
	strings []string
}

func (r *pgcReader) varint() (int64, error) {
	x, n := binary.Varint(r.data[r.pos:])
	if n <= 0 {
		return 0, errors.New("bad number")
	}
	r.pos += n
	return x, nil
}

func (r *pgcReader) interned() (string, error) {
	tag, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if tag == 0 {
		s, err := r.str()
		r.strings = append(r.strings, s)
		return s, err
	}
	if tag-1 >= uint64(len(r.strings)) {
		return "", errors.New("bad string reference")
	}
	return r.strings[tag-1], nil
}

// int32Of reads a varint that must fit an int32.
func (r *pgcReader) int32Of() (int32, error) {
	x, err := r.varint()
	if err == nil && (x < math.MinInt32 || x > math.MaxInt32) {
		err = errors.New("operand out of range")
	}
	return int32(x), err
}

func (r *pgcReader) uvarint() (uint64, error) {
	x, n := binary.Uvarint(r.data[r.pos:])
	if n <= 0 {
		return 0, errors.New("bad number")
	}
	r.pos += n
	return x, nil
}

func (r *pgcReader) str() (string, error) {
	n, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if n > uint64(len(r.data)-r.pos) {
		return "", errors.New("string out of bounds")
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, nil
}

const pgcMaxDepth = 10000

func (r *pgcReader) value(v reflect.Value, depth int) error {
	if depth > pgcMaxDepth {
		return errors.New("nesting too deep")
	}
	switch v.Kind() {
	case reflect.Bool:
		if r.pos >= len(r.data) {
			return errors.New("unexpected end")
		}
		v.SetBool(r.data[r.pos] != 0)
		r.pos++
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		x, err := r.varint()
		if err != nil {
			return err
		}
		if v.OverflowInt(x) {
			return errors.New("number out of range")
		}
		v.SetInt(x)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		x, err := r.uvarint()
		if err != nil {
			return err
		}
		if v.OverflowUint(x) {
			return errors.New("number out of range")
		}
		v.SetUint(x)
	case reflect.Float64:
		if len(r.data)-r.pos < 8 {
			return errors.New("unexpected end")
		}
		v.SetFloat(math.Float64frombits(binary.LittleEndian.Uint64(r.data[r.pos:])))
		r.pos += 8
	case reflect.String:
		s, err := r.interned()
		if err != nil {
			return err
		}
		v.SetString(s)
	case reflect.Slice:
		n, err := r.uvarint()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		n--
		if n > uint64(len(r.data)-r.pos) { // every element takes at least one byte
			return errors.New("list length out of bounds")
		}
		switch v.Type() {
		case typeInstrs:
			code := make([]Instr, n)
			for i := range code {
				if r.pos >= len(r.data) {
					return errors.New("unexpected end")
				}
				code[i].Op = Opcode(r.data[r.pos])
				r.pos++
				if code[i].Op >= numOpcodes {
					return errors.New("unknown opcode")
				}
				var err error
				if code[i].A, err = r.int32Of(); err != nil {
					return err
				}
				if code[i].B, err = r.int32Of(); err != nil {
					return err
				}
				if code[i].C, err = r.int32Of(); err != nil {
					return err
				}
			}
			v.Set(reflect.ValueOf(code))
			return nil
		case typePos:
			ps := make([]ast.Pos, n)
			line := int64(0)
			for i := range ps {
				d, err := r.varint()
				if err != nil {
					return err
				}
				col, err := r.varint()
				if err != nil {
					return err
				}
				line += d
				if line < 0 || line > math.MaxInt32 || col < 0 || col > math.MaxInt32 {
					return errors.New("bad position")
				}
				ps[i] = ast.Pos{Line: int(line), Col: int(col)}
			}
			v.Set(reflect.ValueOf(ps))
			return nil
		}
		s := reflect.MakeSlice(v.Type(), int(n), int(n))
		for i := 0; i < int(n); i++ {
			if err := r.value(s.Index(i), depth+1); err != nil {
				return err
			}
		}
		v.Set(s)
	case reflect.Struct:
		for _, i := range exportedFields(v.Type()) {
			if err := r.value(v.Field(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Pointer:
		tag, err := r.uvarint()
		if err != nil {
			return err
		}
		switch {
		case tag == 0:
			return nil
		case tag == 1:
			p := reflect.New(v.Type().Elem())
			r.objs = append(r.objs, p)
			v.Set(p)
			return r.value(p.Elem(), depth+1)
		default:
			id := tag - 2
			if id >= uint64(len(r.objs)) || r.objs[id].Type() != v.Type() {
				return errors.New("bad reference")
			}
			v.Set(r.objs[id])
		}
	case reflect.Interface:
		tag, err := r.uvarint()
		if err != nil {
			return err
		}
		if tag == 0 {
			return nil
		}
		if tag-1 >= uint64(len(pgcTypes)) {
			return errors.New("unknown type")
		}
		t := pgcTypes[tag-1]
		if !t.Implements(v.Type()) {
			return fmt.Errorf("type %s does not fit %s", t, v.Type())
		}
		e := reflect.New(t).Elem()
		if err := r.value(e, depth+1); err != nil {
			return err
		}
		v.Set(e)
	default:
		return fmt.Errorf("cannot decode kind %s", v.Kind())
	}
	return nil
}

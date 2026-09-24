// Package check is the static checker: names, types, fallibility, effects,
// exhaustiveness and the AI-oriented rules (named arguments, no shadowing,
// no truthiness, nil safety).
package check

import (
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
)

type Kind int

const (
	KAny Kind = iota
	KInt
	KFloat
	KStr
	KBool
	KNil
	KList
	KMap
	KOpt
	KFn
	KStruct
	KEnum
	KChan
	KTask
	KRange
	KModule
	KTypeVal // a type used as a value (User, Shape, http.Request)
	KParam   // generic type parameter
	KVoid    // no value (function without result)
)

type Type struct {
	K        Kind
	Elem     *Type // List/Opt/Chan/Task element, TypeVal underlying type
	Key, Val *Type // Map
	Name     string
	Params   []*Type // Fn
	Ret      *Type   // Fn
	Fallible bool    // Fn
	Fn       *funcInfo
	Struct   *structInfo
	Enum     *enumInfo
	Mod      *modInfo
}

var (
	tAny   = &Type{K: KAny}
	tInt   = &Type{K: KInt}
	tFloat = &Type{K: KFloat}
	tStr   = &Type{K: KStr}
	tBool  = &Type{K: KBool}
	tNil   = &Type{K: KNil}
	tRange = &Type{K: KRange}
	tVoid  = &Type{K: KVoid}
)

func listOf(t *Type) *Type     { return &Type{K: KList, Elem: t} }
func mapOf(k, v *Type) *Type   { return &Type{K: KMap, Key: k, Val: v} }
func chanOf(t *Type) *Type     { return &Type{K: KChan, Elem: t} }
func taskOf(t *Type) *Type     { return &Type{K: KTask, Elem: t} }
func typeValOf(t *Type) *Type  { return &Type{K: KTypeVal, Elem: t} }
func paramType(n string) *Type { return &Type{K: KParam, Name: n} }

func optOf(t *Type) *Type {
	if t == nil || t.K == KOpt || t.K == KAny || t.K == KNil {
		return t
	}
	return &Type{K: KOpt, Elem: t}
}

func (t *Type) String() string {
	if t == nil {
		return "?"
	}
	switch t.K {
	case KAny:
		return "Any"
	case KInt:
		return "Int"
	case KFloat:
		return "Float"
	case KStr:
		return "Str"
	case KBool:
		return "Bool"
	case KNil:
		return "nil"
	case KVoid:
		return "no value"
	case KRange:
		return "Range"
	case KList:
		return "List[" + t.Elem.String() + "]"
	case KMap:
		return "Map[" + t.Key.String() + ", " + t.Val.String() + "]"
	case KOpt:
		return t.Elem.String() + "?"
	case KChan:
		return "Chan[" + t.Elem.String() + "]"
	case KTask:
		return "Task[" + t.Elem.String() + "]"
	case KStruct:
		return t.Struct.qualName()
	case KEnum:
		return t.Enum.qualName()
	case KModule:
		return "module " + t.Mod.name
	case KTypeVal:
		return "Type[" + t.Elem.String() + "]"
	case KParam:
		return t.Name
	case KFn:
		var b strings.Builder
		b.WriteString("fn(")
		for i, p := range t.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.String())
		}
		b.WriteString(")")
		if t.Ret != nil && t.Ret.K != KVoid || t.Fallible {
			b.WriteString(" -> ")
			if t.Fallible {
				b.WriteString("!")
			}
			if t.Ret != nil && t.Ret.K != KVoid {
				b.WriteString(t.Ret.String())
			}
		}
		return b.String()
	}
	return "?"
}

func isUnknown(t *Type) bool { return t == nil || t.K == KAny || t.K == KParam }

// assignable reports whether a value of type src can be used where dst is expected.
func assignable(src, dst *Type) bool {
	if isUnknown(src) || isUnknown(dst) {
		return true
	}
	if dst.K == KOpt {
		if src.K == KNil {
			return true
		}
		if src.K == KOpt {
			return assignable(src.Elem, dst.Elem)
		}
		return assignable(src, dst.Elem)
	}
	if src.K == KNil || src.K == KOpt || src.K == KVoid {
		return false
	}
	if src.K != dst.K {
		return false
	}
	switch src.K {
	case KList, KChan, KTask, KTypeVal:
		return assignable(src.Elem, dst.Elem)
	case KMap:
		return assignable(src.Key, dst.Key) && assignable(src.Val, dst.Val)
	case KStruct:
		return src.Struct == dst.Struct
	case KEnum:
		return src.Enum == dst.Enum
	case KFn:
		if len(src.Params) != len(dst.Params) {
			return false
		}
		for i := range src.Params {
			if !assignable(dst.Params[i], src.Params[i]) {
				return false
			}
		}
		if dst.Ret != nil && dst.Ret.K != KVoid && src.Ret != nil && src.Ret.K != KVoid && !assignable(src.Ret, dst.Ret) {
			return false
		}
		return true
	case KModule:
		return src.Mod == dst.Mod
	}
	return true
}

// join is the common type of two branches.
func join(a, b *Type) *Type {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.K == KVoid || b.K == KVoid:
		return tVoid
	case a.K == KNil && b.K == KNil:
		return tNil
	case a.K == KNil:
		return optOf(b)
	case b.K == KNil:
		return optOf(a)
	case isUnknown(a) || isUnknown(b):
		return tAny
	case assignable(a, b):
		return b
	case assignable(b, a):
		return a
	}
	return tAny
}

// unify binds generic parameters in pt from the argument type at.
func unify(pt, at *Type, b map[string]*Type) {
	if pt == nil || at == nil {
		return
	}
	switch pt.K {
	case KParam:
		if isUnknown(at) || at.K == KNil {
			return
		}
		if cur, ok := b[pt.Name]; !ok || isUnknown(cur) {
			b[pt.Name] = at
		}
	case KList, KChan, KTask, KTypeVal:
		if at.K == pt.K {
			unify(pt.Elem, at.Elem, b)
		}
	case KOpt:
		if at.K == KOpt {
			unify(pt.Elem, at.Elem, b)
		} else {
			unify(pt.Elem, at, b)
		}
	case KMap:
		if at.K == KMap {
			unify(pt.Key, at.Key, b)
			unify(pt.Val, at.Val, b)
		}
	case KFn:
		if at.K == KFn {
			for i := 0; i < len(pt.Params) && i < len(at.Params); i++ {
				unify(pt.Params[i], at.Params[i], b)
			}
			unify(pt.Ret, at.Ret, b)
		}
	}
}

// subst replaces bound generic parameters (unbound ones become Any).
func subst(t *Type, b map[string]*Type) *Type {
	if t == nil {
		return nil
	}
	switch t.K {
	case KParam:
		if r, ok := b[t.Name]; ok {
			return r
		}
		return tAny
	case KList:
		return listOf(subst(t.Elem, b))
	case KChan:
		return chanOf(subst(t.Elem, b))
	case KTask:
		return taskOf(subst(t.Elem, b))
	case KTypeVal:
		return typeValOf(subst(t.Elem, b))
	case KOpt:
		return optOf(subst(t.Elem, b))
	case KMap:
		return mapOf(subst(t.Key, b), subst(t.Val, b))
	case KFn:
		nt := *t
		nt.Params = make([]*Type, len(t.Params))
		for i, p := range t.Params {
			nt.Params[i] = subst(p, b)
		}
		nt.Ret = subst(t.Ret, b)
		return &nt
	}
	return t
}

// ---------- declarations ----------

type paramInfo struct {
	name       string
	typ        *Type
	hasDefault bool
	variadic   bool
}

type funcInfo struct {
	name       string
	decl       *ast.FuncDecl
	params     []paramInfo
	ret        *Type // KVoid if none
	fallible   bool
	uses       []string
	typeParams []string
	hasSelf    bool
	recv       *Type
	mod        *modInfo
	std        bool
}

func (f *funcInfo) fnType() *Type {
	t := &Type{K: KFn, Ret: f.ret, Fallible: f.fallible, Fn: f}
	for _, p := range f.params {
		t.Params = append(t.Params, p.typ)
	}
	return t
}

type fieldInfo struct {
	name       string
	typ        *Type
	hasDefault bool
	pos        ast.Pos
}

type structInfo struct {
	name    string
	decl    *ast.StructDecl
	fields  []*fieldInfo
	methods map[string]*funcInfo
	mod     *modInfo
}

func (s *structInfo) qualName() string {
	if s.mod != nil && s.mod.std && s.mod.name != "core" {
		return s.mod.name + "." + s.name
	}
	return s.name
}

func (s *structInfo) field(name string) *fieldInfo {
	for _, f := range s.fields {
		if f.name == name {
			return f
		}
	}
	return nil
}

type variantInfo struct {
	name   string
	fields []*fieldInfo
}

type enumInfo struct {
	name     string
	variants []*variantInfo
	methods  map[string]*funcInfo
	mod      *modInfo
}

func (e *enumInfo) qualName() string { return e.name }

func (e *enumInfo) variant(name string) *variantInfo {
	for _, v := range e.variants {
		if v.name == name {
			return v
		}
	}
	return nil
}

type modInfo struct {
	name    string
	path    string
	std     bool
	funcs   map[string]*funcInfo
	structs map[string]*structInfo
	enums   map[string]*enumInfo
	consts  map[string]*Type
	imports map[string]*modInfo
	usedImp map[string]bool
	file    *ast.File
}

func newMod(name, path string, std bool) *modInfo {
	return &modInfo{
		name: name, path: path, std: std,
		funcs:   map[string]*funcInfo{},
		structs: map[string]*structInfo{},
		enums:   map[string]*enumInfo{},
		consts:  map[string]*Type{},
		imports: map[string]*modInfo{},
		usedImp: map[string]bool{},
	}
}

// member returns the type of a top-level member, or nil.
func (m *modInfo) member(name string) *Type {
	if f, ok := m.funcs[name]; ok {
		return f.fnType()
	}
	if s, ok := m.structs[name]; ok {
		return typeValOf(&Type{K: KStruct, Struct: s})
	}
	if e, ok := m.enums[name]; ok {
		return typeValOf(&Type{K: KEnum, Enum: e})
	}
	if c, ok := m.consts[name]; ok {
		return c
	}
	return nil
}

func (m *modInfo) memberNames() []string {
	var out []string
	for k := range m.funcs {
		out = append(out, k)
	}
	for k := range m.structs {
		out = append(out, k)
	}
	for k := range m.enums {
		out = append(out, k)
	}
	for k := range m.consts {
		out = append(out, k)
	}
	return out
}

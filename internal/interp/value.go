package interp

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/marcodc74/pygo/internal/ast"
)

// Value is any runtime value. Scalars use Go types directly:
// Int=int64, Float=float64, Str=string, Bool=bool, nil=nil.
type Value = any

// ---------- List ----------

type List struct {
	mu    sync.RWMutex
	items []Value
}

func NewList(items []Value) *List { return &List{items: items} }

func (l *List) Snapshot() []Value {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Value, len(l.items))
	copy(out, l.items)
	return out
}

func (l *List) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.items)
}

// ---------- Map (insertion ordered) ----------

type mapEntry struct {
	key, val Value
	dead     bool
}

type Map struct {
	mu      sync.RWMutex
	entries []mapEntry
	idx     map[Value]int
	live    int
}

func NewMap() *Map { return &Map{idx: map[Value]int{}} }

func (m *Map) Get(k Value) (Value, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, ok := m.idx[k]
	if !ok {
		return nil, false
	}
	return m.entries[i].val, true
}

func (m *Map) Set(k, v Value) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setLocked(k, v)
}

func (m *Map) setLocked(k, v Value) {
	if i, ok := m.idx[k]; ok {
		m.entries[i].val = v
		return
	}
	m.idx[k] = len(m.entries)
	m.entries = append(m.entries, mapEntry{key: k, val: v})
	m.live++
}

func (m *Map) Delete(k Value) (Value, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.idx[k]
	if !ok {
		return nil, false
	}
	v := m.entries[i].val
	m.entries[i] = mapEntry{dead: true}
	delete(m.idx, k)
	m.live--
	if len(m.entries) > 32 && m.live < len(m.entries)/2 {
		m.compact()
	}
	return v, true
}

func (m *Map) compact() {
	out := make([]mapEntry, 0, m.live)
	for _, e := range m.entries {
		if !e.dead {
			m.idx[e.key] = len(out)
			out = append(out, e)
		}
	}
	m.entries = out
}

func (m *Map) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.live
}

// Items returns a snapshot of the live entries in insertion order.
func (m *Map) Items() (keys, vals []Value) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys = make([]Value, 0, m.live)
	vals = make([]Value, 0, m.live)
	for _, e := range m.entries {
		if !e.dead {
			keys = append(keys, e.key)
			vals = append(vals, e.val)
		}
	}
	return
}

func (m *Map) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
	m.idx = map[Value]int{}
	m.live = 0
}

// ---------- user types ----------

type FieldInfo struct {
	Name    string
	Type    *ast.TypeExpr
	Default ast.Expr
}

type StructType struct {
	Name     string
	Module   *Module
	Fields   []*FieldInfo
	FieldIdx map[string]int
	Methods  map[string]*Function
}

type Struct struct {
	mu sync.RWMutex
	T  *StructType
	F  []Value
}

func (s *Struct) Field(name string) (Value, bool) {
	i, ok := s.T.FieldIdx[name]
	if !ok {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.F[i], true
}

func (s *Struct) SetField(name string, v Value) bool {
	i, ok := s.T.FieldIdx[name]
	if !ok {
		return false
	}
	s.mu.Lock()
	s.F[i] = v
	s.mu.Unlock()
	return true
}

func (s *Struct) Snapshot() []Value {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Value, len(s.F))
	copy(out, s.F)
	return out
}

type EnumType struct {
	Name     string
	Module   *Module
	Variants []*VariantInfo
	ByName   map[string]*VariantInfo
	Methods  map[string]*Function
}

type VariantInfo struct {
	Enum   *EnumType
	Name   string
	Fields []*FieldInfo
	Index  int
}

// Enum is an (immutable) enum value.
type Enum struct {
	V *VariantInfo
	F []Value
}

// ---------- callables ----------

type Function struct {
	Name       string
	Params     []*ast.Param
	Ret        *ast.TypeExpr
	Fallible   bool
	Body       *ast.Block
	ExprBody   ast.Expr
	Requires   []ast.Expr
	Ensures    []ast.Expr
	Env        *Env
	Mod        *Module
	Pos        ast.Pos
	HasSelf    bool
	TypeParams map[string]bool
	RecvType   any // *StructType or *EnumType for methods
}

type BoundMethod struct {
	Recv Value
	Fn   *Function
}

// BuiltinFn implements a stdlib function. args are already bound to the
// declared parameters (defaults filled, variadic collected into a *List).
type BuiltinFn func(th *Thread, recv Value, args []Value) (Value, error)

type Builtin struct {
	Name    string
	Decl    *ast.FuncDecl
	Fn      BuiltinFn
	Recv    Value // for methods of builtin types
	Mod     string
	TypeMod *Module // module used to resolve parameter types (extern blocks)
}

type Module struct {
	Name    string
	Path    string
	Members map[string]Value
	Env     *Env
	Types   map[string]any // *StructType / *EnumType
	Imports map[string]*Module
	Std     bool
}

type RangeVal struct {
	Lo, Hi    int64
	Inclusive bool
}

func (r *RangeVal) End() int64 {
	if r.Inclusive {
		return r.Hi + 1
	}
	return r.Hi
}

func (r *RangeVal) Len() int64 {
	n := r.End() - r.Lo
	if n < 0 {
		return 0
	}
	return n
}

type Chan struct {
	ch     chan Value
	closed atomic.Bool
	mu     sync.Mutex
}

type Task struct {
	done chan struct{}
	val  Value
	err  error
}

// ---------- names ----------

func TypeName(v Value) string {
	switch v := v.(type) {
	case nil:
		return "Nil"
	case int64:
		return "Int"
	case float64:
		return "Float"
	case string:
		return "Str"
	case bool:
		return "Bool"
	case *List:
		return "List"
	case *Map:
		return "Map"
	case *Struct:
		return v.T.Name
	case *Enum:
		return v.V.Enum.Name
	case *Function, *BoundMethod, *Builtin, *VariantInfo:
		return "Fn"
	case *StructType, *EnumType:
		return "Type"
	case *Module:
		return "Module"
	case *RangeVal:
		return "Range"
	case *Chan:
		return "Chan"
	case *Task:
		return "Task"
	case *PyHandle:
		if v.T != nil {
			return v.T.Name
		}
		return "python:" + v.PyType
	}
	return "?"
}

// ---------- equality and ordering ----------

func Equal(a, b Value) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case int64, float64, string, bool:
		return a == b
	case *List:
		bl, ok := b.(*List)
		if !ok {
			return false
		}
		if a == bl {
			return true
		}
		x, y := a.Snapshot(), bl.Snapshot()
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case *Map:
		bm, ok := b.(*Map)
		if !ok {
			return false
		}
		if a == bm {
			return true
		}
		ks, vs := a.Items()
		if len(ks) != bm.Len() {
			return false
		}
		for i, k := range ks {
			v, ok := bm.Get(k)
			if !ok || !Equal(vs[i], v) {
				return false
			}
		}
		return true
	case *Struct:
		bs, ok := b.(*Struct)
		if !ok || bs.T != a.T {
			return false
		}
		if a == bs {
			return true
		}
		x, y := a.Snapshot(), bs.Snapshot()
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case *Enum:
		be, ok := b.(*Enum)
		if !ok || be.V != a.V {
			return false
		}
		for i := range a.F {
			if !Equal(a.F[i], be.F[i]) {
				return false
			}
		}
		return true
	case *RangeVal:
		br, ok := b.(*RangeVal)
		return ok && *a == *br
	case *PyHandle:
		bh, ok := b.(*PyHandle)
		return ok && bh.ID == a.ID
	}
	return a == b
}

// compare returns -1/0/1, ok=false if the values are not ordered together.
func compare(a, b Value) (int, bool) {
	switch x := a.(type) {
	case int64:
		if y, ok := b.(int64); ok {
			return cmp3(x < y, x > y), true
		}
	case float64:
		if y, ok := b.(float64); ok {
			return cmp3(x < y, x > y), true
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), true
		}
	case bool:
		if y, ok := b.(bool); ok {
			return cmp3(!x && y, x && !y), true
		}
	case *List:
		if y, ok := b.(*List); ok {
			xs, ys := x.Snapshot(), y.Snapshot()
			for i := 0; i < len(xs) && i < len(ys); i++ {
				c, ok := compare(xs[i], ys[i])
				if !ok {
					return 0, false
				}
				if c != 0 {
					return c, true
				}
			}
			return cmp3(len(xs) < len(ys), len(xs) > len(ys)), true
		}
	}
	return 0, false
}

func cmp3(lt, gt bool) int {
	if lt {
		return -1
	}
	if gt {
		return 1
	}
	return 0
}

// ---------- formatting ----------

// Str is the canonical text form (used by print, str and interpolation).
func Str(v Value) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	writeRepr(&b, v, 0)
	return b.String()
}

func Repr(v Value) string {
	var b strings.Builder
	writeRepr(&b, v, 0)
	return b.String()
}

func FormatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func writeRepr(b *strings.Builder, v Value, depth int) {
	if depth > 50 {
		b.WriteString("...")
		return
	}
	switch v := v.(type) {
	case nil:
		b.WriteString("nil")
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float64:
		b.WriteString(FormatFloat(v))
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case string:
		b.WriteString(strconv.Quote(v))
	case *List:
		b.WriteByte('[')
		for i, x := range v.Snapshot() {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, x, depth+1)
		}
		b.WriteByte(']')
	case *Map:
		b.WriteByte('{')
		ks, vs := v.Items()
		for i := range ks {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, ks[i], depth+1)
			b.WriteString(": ")
			writeRepr(b, vs[i], depth+1)
		}
		b.WriteByte('}')
	case *Struct:
		b.WriteString(v.T.Name)
		b.WriteByte('{')
		for i, x := range v.Snapshot() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(v.T.Fields[i].Name)
			b.WriteString(": ")
			writeRepr(b, x, depth+1)
		}
		b.WriteByte('}')
	case *Enum:
		b.WriteString(v.V.Enum.Name + "." + v.V.Name)
		if len(v.V.Fields) > 0 {
			b.WriteByte('(')
			for i, x := range v.F {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(v.V.Fields[i].Name + ": ")
				writeRepr(b, x, depth+1)
			}
			b.WriteByte(')')
		}
	case *RangeVal:
		op := ".."
		if v.Inclusive {
			op = "..="
		}
		b.WriteString(strconv.FormatInt(v.Lo, 10) + op + strconv.FormatInt(v.Hi, 10))
	case *Function:
		b.WriteString("<fn " + v.Name + ">")
	case *BoundMethod:
		b.WriteString("<method " + v.Fn.Name + ">")
	case *Builtin:
		b.WriteString("<builtin " + v.Name + ">")
	case *VariantInfo:
		b.WriteString("<variant " + v.Enum.Name + "." + v.Name + ">")
	case *StructType:
		b.WriteString("<type " + v.Name + ">")
	case *EnumType:
		b.WriteString("<type " + v.Name + ">")
	case *Module:
		b.WriteString("<module " + v.Name + ">")
	case *Chan:
		b.WriteString("<chan>")
	case *Task:
		b.WriteString("<task>")
	case *PyHandle:
		b.WriteString("<python " + v.PyType + ">")
	default:
		b.WriteString("<?>")
	}
}

// sortValues sorts in place; ok=false if items are not mutually ordered.
func sortValues(xs []Value, keys []Value, desc bool) bool {
	ok := true
	if keys == nil {
		keys = xs
	}
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		c, good := compare(keys[idx[i]], keys[idx[j]])
		if !good {
			ok = false
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	out := make([]Value, len(xs))
	for i, k := range idx {
		out[i] = xs[k]
	}
	copy(xs, out)
	return ok
}

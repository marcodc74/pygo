package interp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/marcodc74/pygo/internal/ast"
)

func (th *Thread) unary(e *ast.Unary, x Value) (Value, error) {
	switch e.Op {
	case "-":
		switch v := x.(type) {
		case int64:
			if v == math.MinInt64 {
				return nil, th.panicAt(e.Pos, POverflow, "", "integer overflow in -(%d)", v)
			}
			return -v, nil
		case float64:
			return -v, nil
		}
		return nil, th.panicAt(e.Pos, PType, "", "cannot negate %s", TypeName(x))
	case "not":
		b, ok := x.(bool)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "'not' needs a Bool (no truthiness); e.g. x == nil, xs.is_empty()", "cannot apply 'not' to %s", TypeName(x))
		}
		return !b, nil
	}
	return nil, th.panicAt(e.Pos, PInternal, "", "unknown unary operator %s", e.Op)
}

func (th *Thread) evalBinary(env *Env, e *ast.Binary) (Value, error) {
	x, err := th.eval(env, e.X)
	if err != nil {
		return nil, err
	}
	switch e.Op {
	case "and", "or":
		xb, ok := x.(bool)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "operands of and/or must be Bool (no truthiness)", "left operand of '%s' is %s", e.Op, TypeName(x))
		}
		if (e.Op == "and" && !xb) || (e.Op == "or" && xb) {
			return xb, nil
		}
		y, err := th.eval(env, e.Y)
		if err != nil {
			return nil, err
		}
		yb, ok := y.(bool)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "operands of and/or must be Bool (no truthiness)", "right operand of '%s' is %s", e.Op, TypeName(y))
		}
		return yb, nil
	case "??":
		if x != nil {
			return x, nil
		}
		return th.eval(env, e.Y)
	}
	y, err := th.eval(env, e.Y)
	if err != nil {
		return nil, err
	}
	return th.binaryValues(e, x, y)
}

func (th *Thread) binaryValues(e *ast.Binary, x, y Value) (Value, error) {
	switch e.Op {
	case "==":
		if err := th.checkComparable(e, x, y); err != nil {
			return nil, err
		}
		return Equal(x, y), nil
	case "!=":
		if err := th.checkComparable(e, x, y); err != nil {
			return nil, err
		}
		return !Equal(x, y), nil
	case "<", "<=", ">", ">=":
		c, ok := compare(x, y)
		if !ok {
			return nil, th.mixErr(e, x, y)
		}
		switch e.Op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		}
		return c >= 0, nil
	case "in":
		return th.contains(e, y, x)
	}
	switch a := x.(type) {
	case int64:
		b, ok := y.(int64)
		if !ok {
			return nil, th.mixErr(e, x, y)
		}
		return th.intOp(e, a, b)
	case float64:
		b, ok := y.(float64)
		if !ok {
			return nil, th.mixErr(e, x, y)
		}
		switch e.Op {
		case "+":
			return a + b, nil
		case "-":
			return a - b, nil
		case "*":
			return a * b, nil
		case "/":
			if b == 0 {
				return nil, th.panicAt(e.Pos, PDivZero, "", "float division by zero")
			}
			return a / b, nil
		case "%":
			if b == 0 {
				return nil, th.panicAt(e.Pos, PDivZero, "", "float modulo by zero")
			}
			return math.Mod(a, b), nil
		}
	case string:
		if e.Op == "+" {
			b, ok := y.(string)
			if !ok {
				return nil, th.panicAt(e.Pos, PType, `use interpolation: "{a}{b}" or str(x)`, "cannot add Str and %s", TypeName(y))
			}
			return a + b, nil
		}
	case *List:
		if e.Op == "+" {
			b, ok := y.(*List)
			if !ok {
				return nil, th.mixErr(e, x, y)
			}
			return NewList(append(a.Snapshot(), b.Snapshot()...)), nil
		}
	}
	return nil, th.mixErr(e, x, y)
}

func (th *Thread) checkComparable(e *ast.Binary, x, y Value) error {
	if x == nil || y == nil {
		return nil
	}
	_, xi := x.(int64)
	_, xf := x.(float64)
	_, yi := y.(int64)
	_, yf := y.(float64)
	if (xi && yf) || (xf && yi) {
		return th.mixErr(e, x, y)
	}
	return nil
}

func (th *Thread) mixErr(e *ast.Binary, x, y Value) error {
	hint := ""
	tx, ty := TypeName(x), TypeName(y)
	if (tx == "Int" && ty == "Float") || (tx == "Float" && ty == "Int") {
		hint = "no implicit conversions: use float(x) or int(x)"
	} else if x == nil || y == nil {
		hint = "a value is nil; check it first or use ??"
	}
	return th.panicAt(e.Pos, PType, hint, "operator '%s' not defined for %s and %s", e.Op, tx, ty)
}

func (th *Thread) intOp(e *ast.Binary, a, b int64) (Value, error) {
	switch e.Op {
	case "+":
		r := a + b
		if (a > 0 && b > 0 && r < 0) || (a < 0 && b < 0 && r >= 0) {
			return nil, th.panicAt(e.Pos, POverflow, "", "integer overflow in %d + %d", a, b)
		}
		return r, nil
	case "-":
		r := a - b
		if (a >= 0 && b < 0 && r < 0) || (a < 0 && b > 0 && r >= 0) {
			return nil, th.panicAt(e.Pos, POverflow, "", "integer overflow in %d - %d", a, b)
		}
		return r, nil
	case "*":
		if a == 0 || b == 0 {
			return int64(0), nil
		}
		r := a * b
		if r/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
			return nil, th.panicAt(e.Pos, POverflow, "", "integer overflow in %d * %d", a, b)
		}
		return r, nil
	case "/":
		if b == 0 {
			return nil, th.panicAt(e.Pos, PDivZero, "", "integer division by zero")
		}
		if a == math.MinInt64 && b == -1 {
			return nil, th.panicAt(e.Pos, POverflow, "", "integer overflow in %d / %d", a, b)
		}
		return a / b, nil
	case "%":
		if b == 0 {
			return nil, th.panicAt(e.Pos, PDivZero, "", "integer modulo by zero")
		}
		if b == -1 {
			return int64(0), nil
		}
		return a % b, nil
	}
	return nil, th.panicAt(e.Pos, PType, "", "operator '%s' not defined for Int", e.Op)
}

func (th *Thread) contains(e *ast.Binary, container, item Value) (Value, error) {
	switch c := container.(type) {
	case *List:
		for _, v := range c.Snapshot() {
			if Equal(v, item) {
				return true, nil
			}
		}
		return false, nil
	case *Map:
		_, ok := c.Get(item)
		return ok, nil
	case string:
		s, ok := item.(string)
		if !ok {
			return nil, th.panicAt(e.Pos, PType, "", "'in' on Str needs a Str, got %s", TypeName(item))
		}
		return strings.Contains(c, s), nil
	case *RangeVal:
		n, ok := item.(int64)
		if !ok {
			return false, nil
		}
		return n >= c.Lo && n < c.End(), nil
	}
	return nil, th.panicAt(e.Pos, PType, "", "'in' not defined for %s", TypeName(container))
}

// ---------- runtime type checks ----------

func (th *Thread) typeMatches(v Value, te *ast.TypeExpr, m *Module, tparams map[string]bool) bool {
	if te == nil {
		return true
	}
	if v == nil {
		return te.Optional || te.Name == "Any" || te.Name == "Nil" || tparams[te.Name] || isTypeParamName(te.Name, m)
	}
	switch te.Name {
	case "Any":
		return true
	case "Int":
		_, ok := v.(int64)
		return ok
	case "Float":
		_, ok := v.(float64)
		return ok
	case "Str":
		_, ok := v.(string)
		return ok
	case "Bool":
		_, ok := v.(bool)
		return ok
	case "List":
		_, ok := v.(*List)
		return ok
	case "Map":
		_, ok := v.(*Map)
		return ok
	case "Range":
		_, ok := v.(*RangeVal)
		return ok
	case "Chan":
		_, ok := v.(*Chan)
		return ok
	case "Task":
		_, ok := v.(*Task)
		return ok
	case "Nil":
		return false
	case "Error":
		s, ok := v.(*Struct)
		return ok && s.T == th.in.errType
	case "fn":
		switch v.(type) {
		case *Function, *BoundMethod, *Builtin, *VariantInfo:
			return true
		}
		return false
	case "Type":
		switch v.(type) {
		case *StructType, *EnumType:
			return true
		}
		return false
	}
	if tparams[te.Name] {
		return true
	}
	var t any
	if i := strings.IndexByte(te.Name, '.'); i >= 0 {
		if m != nil {
			if im := m.Imports[te.Name[:i]]; im != nil {
				t = im.Types[te.Name[i+1:]]
			}
		}
	} else if m != nil {
		t = m.Types[te.Name]
	}
	switch t := t.(type) {
	case *StructType:
		s, ok := v.(*Struct)
		return ok && s.T == t
	case *EnumType:
		en, ok := v.(*Enum)
		return ok && en.V.Enum == t
	}
	return true // unknown names (type parameters) are not checked at runtime
}

func isTypeParamName(name string, m *Module) bool {
	if len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z' {
		return true
	}
	return false
}

// ---------- format specs for "{x:spec}" ----------

// formatSpec applies [[fill]align][0][width][.prec][type].
func formatSpec(v Value, spec string) (string, string) {
	rs := []rune(spec)
	i := 0
	fill, align := ' ', rune(0)
	if len(rs) >= 2 && strings.ContainsRune("<>^", rs[1]) {
		fill, align = rs[0], rs[1]
		i = 2
	} else if len(rs) >= 1 && strings.ContainsRune("<>^", rs[0]) {
		align = rs[0]
		i = 1
	}
	zero := false
	if i < len(rs) && rs[i] == '0' {
		zero = true
		i++
	}
	width := 0
	for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
		width = width*10 + int(rs[i]-'0')
		i++
	}
	prec := -1
	if i < len(rs) && rs[i] == '.' {
		i++
		prec = 0
		for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
			prec = prec*10 + int(rs[i]-'0')
			i++
		}
	}
	typ := rune(0)
	if i < len(rs) {
		typ = rs[i]
		i++
	}
	if i != len(rs) {
		return "", fmt.Sprintf("invalid format spec %q", spec)
	}
	var s string
	switch typ {
	case 0:
		if f, ok := v.(float64); ok && prec >= 0 {
			s = strconv.FormatFloat(f, 'f', prec, 64)
		} else if st, ok := v.(string); ok && prec >= 0 {
			if r := []rune(st); len(r) > prec {
				st = string(r[:prec])
			}
			s = st
		} else {
			s = Str(v)
		}
	case 'f', 'e', '%':
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int64:
			f = float64(n)
		default:
			return "", fmt.Sprintf("format '%c' needs a number, got %s", typ, TypeName(v))
		}
		if prec < 0 {
			prec = 6
		}
		if typ == '%' {
			s = strconv.FormatFloat(f*100, 'f', prec, 64) + "%"
		} else {
			s = strconv.FormatFloat(f, byte(typ), prec, 64)
		}
	case 'x', 'X', 'b', 'o', 'd':
		n, ok := v.(int64)
		if !ok {
			return "", fmt.Sprintf("format '%c' needs an Int, got %s", typ, TypeName(v))
		}
		base := map[rune]int{'x': 16, 'X': 16, 'b': 2, 'o': 8, 'd': 10}[typ]
		s = strconv.FormatInt(n, base)
		if typ == 'X' {
			s = strings.ToUpper(s)
		}
	default:
		return "", fmt.Sprintf("unknown format type '%c'", typ)
	}
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s, ""
	}
	pad := width - n
	if zero && align == 0 {
		sign := ""
		if strings.HasPrefix(s, "-") {
			sign, s = "-", s[1:]
		}
		return sign + strings.Repeat("0", pad) + s, ""
	}
	if align == 0 {
		align = '<'
		switch v.(type) {
		case int64, float64:
			align = '>'
		}
	}
	f := string(fill)
	switch align {
	case '>':
		return strings.Repeat(f, pad) + s, ""
	case '^':
		return strings.Repeat(f, pad/2) + s + strings.Repeat(f, pad-pad/2), ""
	}
	return s + strings.Repeat(f, pad), ""
}

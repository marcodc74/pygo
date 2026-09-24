package interp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// stdImpls maps module -> function -> implementation. Every function
// declared in internal/sig/std/<module>.pg must have an entry.
var stdImpls = map[string]map[string]BuiltinFn{}

// methodImpls maps builtin type -> method -> implementation.
var methodImpls = map[string]map[string]BuiltinFn{}

func register(mod string, fns map[string]BuiltinFn) {
	if stdImpls[mod] == nil {
		stdImpls[mod] = map[string]BuiltinFn{}
	}
	for k, v := range fns {
		stdImpls[mod][k] = v
	}
}

// Implemented reports the registered implementations (for tests).
func Implemented() (map[string]map[string]BuiltinFn, map[string]map[string]BuiltinFn) {
	return stdImpls, methodImpls
}

// fail builds a recoverable failure with the given error code.
func (th *Thread) fail(code, format string, args ...any) error {
	return &Failure{Err: th.in.newError(fmt.Sprintf(format, args...), code, nil), Trace: th.trace()}
}

func perr(code, hint, format string, args ...any) error {
	return &Panic{Code: code, Hint: hint, Message: fmt.Sprintf(format, args...)}
}

// call invokes a callable value from inside a builtin.
func (th *Thread) call(f Value, args ...Value) (Value, error) {
	return th.callValue(f, args, nil, th.top().pos)
}

func boolArg(th *Thread, f Value, x Value, what string) (bool, error) {
	r, err := th.call(f, x)
	if err != nil {
		return false, err
	}
	b, ok := r.(bool)
	if !ok {
		return false, perr(PType, "", "%s function must return Bool, got %s", what, TypeName(r))
	}
	return b, nil
}

func init() {
	register("core", map[string]BuiltinFn{
		"print": func(th *Thread, _ Value, a []Value) (Value, error) {
			th.in.out.WriteString(joinValues(a[0].(*List)) + "\n")
			return nil, nil
		},
		"eprint": func(th *Thread, _ Value, a []Value) (Value, error) {
			th.in.out.Flush()
			th.in.errw.WriteString(joinValues(a[0].(*List)) + "\n")
			th.in.errw.Flush()
			return nil, nil
		},
		"len": func(th *Thread, _ Value, a []Value) (Value, error) {
			switch v := a[0].(type) {
			case string:
				return int64(utf8.RuneCountInString(v)), nil
			case *List:
				return int64(v.Len()), nil
			case *Map:
				return int64(v.Len()), nil
			case *RangeVal:
				return v.Len(), nil
			case *Chan:
				return int64(len(v.ch)), nil
			}
			return nil, perr(PType, "", "len() not defined for %s", TypeName(a[0]))
		},
		"str":  func(th *Thread, _ Value, a []Value) (Value, error) { return Str(a[0]), nil },
		"repr": func(th *Thread, _ Value, a []Value) (Value, error) { return Repr(a[0]), nil },
		"int": func(th *Thread, _ Value, a []Value) (Value, error) {
			switch v := a[0].(type) {
			case int64:
				return v, nil
			case float64:
				if math.IsNaN(v) || v >= 9.223372036854775807e18 || v < -9.223372036854775808e18 {
					return nil, perr(POverflow, "", "cannot convert %s to Int", FormatFloat(v))
				}
				return int64(v), nil
			case bool:
				if v {
					return int64(1), nil
				}
				return int64(0), nil
			case string:
				return nil, perr(PType, "use try parse_int(s)", "int() does not parse strings")
			}
			return nil, perr(PType, "", "cannot convert %s to Int", TypeName(a[0]))
		},
		"float": func(th *Thread, _ Value, a []Value) (Value, error) {
			switch v := a[0].(type) {
			case int64:
				return float64(v), nil
			case float64:
				return v, nil
			case string:
				return nil, perr(PType, "use try parse_float(s)", "float() does not parse strings")
			}
			return nil, perr(PType, "", "cannot convert %s to Float", TypeName(a[0]))
		},
		"parse_int": func(th *Thread, _ Value, a []Value) (Value, error) {
			s := strings.ReplaceAll(strings.TrimSpace(a[0].(string)), "_", "")
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, th.fail("E_PARSE", "invalid integer %q", a[0])
			}
			return n, nil
		},
		"parse_float": func(th *Thread, _ Value, a []Value) (Value, error) {
			f, err := strconv.ParseFloat(strings.TrimSpace(a[0].(string)), 64)
			if err != nil {
				return nil, th.fail("E_PARSE", "invalid float %q", a[0])
			}
			return f, nil
		},
		"type_of": func(th *Thread, _ Value, a []Value) (Value, error) { return TypeName(a[0]), nil },
		"error": func(th *Thread, _ Value, a []Value) (Value, error) {
			return th.in.newError(a[0].(string), a[1].(string), a[2]), nil
		},
		"panic": func(th *Thread, _ Value, a []Value) (Value, error) {
			return nil, perr(PUser, "", "%s", a[0].(string))
		},
		"chan": func(th *Thread, _ Value, a []Value) (Value, error) {
			n := a[0].(int64)
			if n < 0 {
				return nil, perr(PChan, "", "negative channel capacity")
			}
			return &Chan{ch: make(chan Value, n)}, nil
		},
		"wait_all": func(th *Thread, _ Value, a []Value) (Value, error) {
			items := a[0].(*List).Snapshot()
			out := make([]Value, len(items))
			var firstErr error
			for i, it := range items {
				t, ok := it.(*Task)
				if !ok {
					return nil, perr(PType, "", "wait_all expects List[Task], found %s", TypeName(it))
				}
				<-t.done
				if t.err != nil && firstErr == nil {
					firstErr = t.err
				}
				out[i] = t.val
			}
			if firstErr != nil {
				return nil, firstErr
			}
			return NewList(out), nil
		},
		"read_line": func(th *Thread, _ Value, a []Value) (Value, error) {
			th.in.out.Flush()
			th.in.stdinMu.Lock()
			defer th.in.stdinMu.Unlock()
			line, err := th.in.stdin.ReadString('\n')
			if err != nil && line == "" {
				return nil, nil
			}
			return strings.TrimRight(line, "\r\n"), nil
		},
		"read_all": func(th *Thread, _ Value, a []Value) (Value, error) {
			th.in.stdinMu.Lock()
			defer th.in.stdinMu.Unlock()
			var b strings.Builder
			buf := make([]byte, 32*1024)
			for {
				n, err := th.in.stdin.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			return b.String(), nil
		},
	})

	methodImpls["Str"] = map[string]BuiltinFn{
		"len": func(th *Thread, r Value, a []Value) (Value, error) {
			return int64(utf8.RuneCountInString(r.(string))), nil
		},
		"is_empty": func(th *Thread, r Value, a []Value) (Value, error) { return r.(string) == "", nil },
		"upper":    func(th *Thread, r Value, a []Value) (Value, error) { return strings.ToUpper(r.(string)), nil },
		"lower":    func(th *Thread, r Value, a []Value) (Value, error) { return strings.ToLower(r.(string)), nil },
		"trim":     func(th *Thread, r Value, a []Value) (Value, error) { return strings.TrimSpace(r.(string)), nil },
		"trim_start": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.TrimLeft(r.(string), " \t\r\n"), nil
		},
		"trim_end": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.TrimRight(r.(string), " \t\r\n"), nil
		},
		"split": func(th *Thread, r Value, a []Value) (Value, error) {
			sep := a[0].(string)
			if sep == "" {
				return nil, perr(PArgs, "use s.chars() to split into characters", "split separator is empty")
			}
			return strList(strings.Split(r.(string), sep)), nil
		},
		"lines": func(th *Thread, r Value, a []Value) (Value, error) {
			s := strings.ReplaceAll(r.(string), "\r\n", "\n")
			s = strings.TrimSuffix(s, "\n")
			if s == "" {
				return NewList([]Value{}), nil
			}
			return strList(strings.Split(s, "\n")), nil
		},
		"chars": func(th *Thread, r Value, a []Value) (Value, error) {
			var out []Value
			for _, c := range r.(string) {
				out = append(out, string(c))
			}
			return NewList(append([]Value{}, out...)), nil
		},
		"contains": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.Contains(r.(string), a[0].(string)), nil
		},
		"starts_with": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.HasPrefix(r.(string), a[0].(string)), nil
		},
		"ends_with": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.HasSuffix(r.(string), a[0].(string)), nil
		},
		"find": func(th *Thread, r Value, a []Value) (Value, error) {
			s := r.(string)
			i := strings.Index(s, a[0].(string))
			if i < 0 {
				return nil, nil
			}
			return int64(utf8.RuneCountInString(s[:i])), nil
		},
		"replace": func(th *Thread, r Value, a []Value) (Value, error) {
			return strings.ReplaceAll(r.(string), a[0].(string), a[1].(string)), nil
		},
		"repeat": func(th *Thread, r Value, a []Value) (Value, error) {
			n := a[0].(int64)
			if n < 0 || n*int64(len(r.(string))) > 1<<30 {
				return nil, perr(PArgs, "", "invalid repeat count %d", n)
			}
			return strings.Repeat(r.(string), int(n)), nil
		},
		"pad_left": func(th *Thread, r Value, a []Value) (Value, error) {
			return pad(r.(string), a[0].(int64), a[1].(string), true)
		},
		"pad_right": func(th *Thread, r Value, a []Value) (Value, error) {
			return pad(r.(string), a[0].(int64), a[1].(string), false)
		},
	}

	methodImpls["List"] = map[string]BuiltinFn{
		"len":      func(th *Thread, r Value, a []Value) (Value, error) { return int64(r.(*List).Len()), nil },
		"is_empty": func(th *Thread, r Value, a []Value) (Value, error) { return r.(*List).Len() == 0, nil },
		"push": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			l.mu.Lock()
			l.items = append(l.items, a[0])
			l.mu.Unlock()
			return nil, nil
		},
		"pop": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			l.mu.Lock()
			defer l.mu.Unlock()
			if len(l.items) == 0 {
				return nil, nil
			}
			v := l.items[len(l.items)-1]
			l.items = l.items[:len(l.items)-1]
			return v, nil
		},
		"insert": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			i := a[0].(int64)
			l.mu.Lock()
			defer l.mu.Unlock()
			if i < 0 || i > int64(len(l.items)) {
				return nil, perr(PIndex, "", "insert index %d out of range for length %d", i, len(l.items))
			}
			l.items = append(l.items, nil)
			copy(l.items[i+1:], l.items[i:])
			l.items[i] = a[1]
			return nil, nil
		},
		"remove": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			i := a[0].(int64)
			l.mu.Lock()
			defer l.mu.Unlock()
			if i < 0 || i >= int64(len(l.items)) {
				return nil, perr(PIndex, "", "remove index %d out of range for length %d", i, len(l.items))
			}
			v := l.items[i]
			l.items = append(l.items[:i], l.items[i+1:]...)
			return v, nil
		},
		"clear": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			l.mu.Lock()
			l.items = nil
			l.mu.Unlock()
			return nil, nil
		},
		"extend": func(th *Thread, r Value, a []Value) (Value, error) {
			l := r.(*List)
			other := a[0].(*List).Snapshot()
			l.mu.Lock()
			l.items = append(l.items, other...)
			l.mu.Unlock()
			return nil, nil
		},
		"contains": func(th *Thread, r Value, a []Value) (Value, error) {
			for _, v := range r.(*List).Snapshot() {
				if Equal(v, a[0]) {
					return true, nil
				}
			}
			return false, nil
		},
		"index_of": func(th *Thread, r Value, a []Value) (Value, error) {
			for i, v := range r.(*List).Snapshot() {
				if Equal(v, a[0]) {
					return int64(i), nil
				}
			}
			return nil, nil
		},
		"first": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			if len(items) == 0 {
				return nil, nil
			}
			return items[0], nil
		},
		"last": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			if len(items) == 0 {
				return nil, nil
			}
			return items[len(items)-1], nil
		},
		"map": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			out := make([]Value, len(items))
			for i, v := range items {
				x, err := th.call(a[0], v)
				if err != nil {
					return nil, err
				}
				out[i] = x
			}
			return NewList(out), nil
		},
		"filter": func(th *Thread, r Value, a []Value) (Value, error) {
			out := []Value{}
			for _, v := range r.(*List).Snapshot() {
				ok, err := boolArg(th, a[0], v, "filter")
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, v)
				}
			}
			return NewList(out), nil
		},
		"reduce": func(th *Thread, r Value, a []Value) (Value, error) {
			acc := a[0]
			for _, v := range r.(*List).Snapshot() {
				x, err := th.call(a[1], acc, v)
				if err != nil {
					return nil, err
				}
				acc = x
			}
			return acc, nil
		},
		"find": func(th *Thread, r Value, a []Value) (Value, error) {
			for _, v := range r.(*List).Snapshot() {
				ok, err := boolArg(th, a[0], v, "find")
				if err != nil {
					return nil, err
				}
				if ok {
					return v, nil
				}
			}
			return nil, nil
		},
		"any": func(th *Thread, r Value, a []Value) (Value, error) {
			for _, v := range r.(*List).Snapshot() {
				ok, err := boolArg(th, a[0], v, "any")
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		},
		"all": func(th *Thread, r Value, a []Value) (Value, error) {
			for _, v := range r.(*List).Snapshot() {
				ok, err := boolArg(th, a[0], v, "all")
				if err != nil || !ok {
					return ok, err
				}
			}
			return true, nil
		},
		"count": func(th *Thread, r Value, a []Value) (Value, error) {
			n := int64(0)
			for _, v := range r.(*List).Snapshot() {
				ok, err := boolArg(th, a[0], v, "count")
				if err != nil {
					return nil, err
				}
				if ok {
					n++
				}
			}
			return n, nil
		},
		"sorted": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			var keys []Value
			if a[0] != nil {
				keys = make([]Value, len(items))
				for i, v := range items {
					k, err := th.call(a[0], v)
					if err != nil {
						return nil, err
					}
					keys[i] = k
				}
			}
			if !sortValues(items, keys, a[1].(bool)) {
				return nil, perr(PType, "sort keys must all be Int, all Float or all Str; pass key: fn(x) => ...", "cannot sort: items are not mutually comparable")
			}
			return NewList(items), nil
		},
		"reversed": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
			return NewList(items), nil
		},
		"unique": func(th *Thread, r Value, a []Value) (Value, error) {
			out := []Value{}
			for _, v := range r.(*List).Snapshot() {
				dup := false
				for _, o := range out {
					if Equal(o, v) {
						dup = true
						break
					}
				}
				if !dup {
					out = append(out, v)
				}
			}
			return NewList(out), nil
		},
		"join": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			parts := make([]string, len(items))
			for i, v := range items {
				parts[i] = Str(v)
			}
			return strings.Join(parts, a[0].(string)), nil
		},
		"sum": func(th *Thread, r Value, a []Value) (Value, error) {
			items := r.(*List).Snapshot()
			var ai int64
			var af float64
			isFloat := false
			for i, v := range items {
				switch n := v.(type) {
				case int64:
					if isFloat {
						return nil, perr(PType, "", "sum of mixed Int and Float")
					}
					r := ai + n
					if (ai > 0 && n > 0 && r < 0) || (ai < 0 && n < 0 && r >= 0) {
						return nil, perr(POverflow, "", "integer overflow in sum")
					}
					ai = r
				case float64:
					if i > 0 && !isFloat {
						return nil, perr(PType, "", "sum of mixed Int and Float")
					}
					isFloat = true
					af += n
				default:
					return nil, perr(PType, "", "sum of non-numeric %s", TypeName(v))
				}
			}
			if isFloat {
				return af, nil
			}
			return ai, nil
		},
		"min": func(th *Thread, r Value, a []Value) (Value, error) { return extreme(r.(*List), -1) },
		"max": func(th *Thread, r Value, a []Value) (Value, error) { return extreme(r.(*List), 1) },
		"copy": func(th *Thread, r Value, a []Value) (Value, error) {
			return NewList(r.(*List).Snapshot()), nil
		},
	}

	methodImpls["Map"] = map[string]BuiltinFn{
		"len":      func(th *Thread, r Value, a []Value) (Value, error) { return int64(r.(*Map).Len()), nil },
		"is_empty": func(th *Thread, r Value, a []Value) (Value, error) { return r.(*Map).Len() == 0, nil },
		"get": func(th *Thread, r Value, a []Value) (Value, error) {
			v, _ := r.(*Map).Get(a[0])
			return v, nil
		},
		"has": func(th *Thread, r Value, a []Value) (Value, error) {
			_, ok := r.(*Map).Get(a[0])
			return ok, nil
		},
		"delete": func(th *Thread, r Value, a []Value) (Value, error) {
			v, _ := r.(*Map).Delete(a[0])
			return v, nil
		},
		"keys": func(th *Thread, r Value, a []Value) (Value, error) {
			ks, _ := r.(*Map).Items()
			return NewList(ks), nil
		},
		"values": func(th *Thread, r Value, a []Value) (Value, error) {
			_, vs := r.(*Map).Items()
			return NewList(vs), nil
		},
		"clear": func(th *Thread, r Value, a []Value) (Value, error) { r.(*Map).Clear(); return nil, nil },
		"copy": func(th *Thread, r Value, a []Value) (Value, error) {
			m := NewMap()
			ks, vs := r.(*Map).Items()
			for i := range ks {
				m.Set(ks[i], vs[i])
			}
			return m, nil
		},
	}

	methodImpls["Range"] = map[string]BuiltinFn{
		"len": func(th *Thread, r Value, a []Value) (Value, error) { return r.(*RangeVal).Len(), nil },
		"contains": func(th *Thread, r Value, a []Value) (Value, error) {
			rv, n := r.(*RangeVal), a[0].(int64)
			return n >= rv.Lo && n < rv.End(), nil
		},
		"to_list": func(th *Thread, r Value, a []Value) (Value, error) {
			rv := r.(*RangeVal)
			if rv.Len() > 1<<26 {
				return nil, perr(PArgs, "", "range too large for to_list()")
			}
			out := make([]Value, 0, rv.Len())
			for n := rv.Lo; n < rv.End(); n++ {
				out = append(out, n)
			}
			return NewList(out), nil
		},
	}

	methodImpls["Chan"] = map[string]BuiltinFn{
		"send": func(th *Thread, r Value, a []Value) (Value, error) {
			c := r.(*Chan)
			if c.closed.Load() {
				return nil, perr(PChan, "", "send on closed channel")
			}
			defer func() { recover() }() // closed concurrently: drop silently
			c.ch <- a[0]
			return nil, nil
		},
		"recv": func(th *Thread, r Value, a []Value) (Value, error) {
			v, ok := <-r.(*Chan).ch
			if !ok {
				return nil, nil
			}
			return v, nil
		},
		"close": func(th *Thread, r Value, a []Value) (Value, error) {
			c := r.(*Chan)
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed.Swap(true) {
				return nil, perr(PChan, "", "channel already closed")
			}
			close(c.ch)
			return nil, nil
		},
		"len": func(th *Thread, r Value, a []Value) (Value, error) { return int64(len(r.(*Chan).ch)), nil },
	}

	methodImpls["Task"] = map[string]BuiltinFn{
		"wait": func(th *Thread, r Value, a []Value) (Value, error) {
			t := r.(*Task)
			<-t.done
			return t.val, t.err
		},
		"done": func(th *Thread, r Value, a []Value) (Value, error) {
			select {
			case <-r.(*Task).done:
				return true, nil
			default:
				return false, nil
			}
		},
	}
}

func joinValues(l *List) string {
	items := l.Snapshot()
	parts := make([]string, len(items))
	for i, v := range items {
		parts[i] = Str(v)
	}
	return strings.Join(parts, " ")
}

func strList(ss []string) *List {
	out := make([]Value, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return NewList(out)
}

func pad(s string, width int64, fill string, left bool) (Value, error) {
	if utf8.RuneCountInString(fill) != 1 {
		return nil, perr(PArgs, "", "fill must be a single character")
	}
	n := int64(utf8.RuneCountInString(s))
	if n >= width {
		return s, nil
	}
	p := strings.Repeat(fill, int(width-n))
	if left {
		return p + s, nil
	}
	return s + p, nil
}

func extreme(l *List, dir int) (Value, error) {
	items := l.Snapshot()
	if len(items) == 0 {
		return nil, nil
	}
	best := items[0]
	for _, v := range items[1:] {
		c, ok := compare(v, best)
		if !ok {
			return nil, perr(PType, "", "min/max: items are not mutually comparable")
		}
		if c*dir > 0 {
			best = v
		}
	}
	return best, nil
}

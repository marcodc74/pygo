package interp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
)

func init() {
	register("json", map[string]BuiltinFn{
		"encode": func(th *Thread, _ Value, a []Value) (Value, error) {
			var b bytes.Buffer
			if err := encodeJSON(&b, a[0], int(a[1].(int64)), 0); err != nil {
				return nil, perr(PType, "", "json.encode: %v", err)
			}
			return b.String(), nil
		},
		"decode": func(th *Thread, _ Value, a []Value) (Value, error) {
			v, err := decodeJSON(a[0].(string))
			if err != nil {
				return nil, th.fail("E_JSON", "invalid JSON: %v", err)
			}
			return v, nil
		},
		"decode_as": func(th *Thread, _ Value, a []Value) (Value, error) {
			v, err := decodeJSON(a[0].(string))
			if err != nil {
				return nil, th.fail("E_JSON", "invalid JSON: %v", err)
			}
			var te *ast.TypeExpr
			var mod *Module
			switch t := a[1].(type) {
			case *StructType:
				te, mod = &ast.TypeExpr{Name: t.Name}, t.Module
			case *EnumType:
				te, mod = &ast.TypeExpr{Name: t.Name}, t.Module
			default:
				return nil, perr(PType, "pass a struct type: json.decode_as(text, schema: User)", "schema must be a struct or enum type, got %s", TypeName(a[1]))
			}
			out, cerr := th.convert(v, te, mod, "$")
			if cerr != "" {
				return nil, th.fail("E_SCHEMA", "%s", cerr)
			}
			return out, nil
		},
	})

	register("fs", map[string]BuiltinFn{
		"read": func(th *Thread, _ Value, a []Value) (Value, error) {
			b, err := os.ReadFile(a[0].(string))
			if err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			return string(b), nil
		},
		"write": func(th *Thread, _ Value, a []Value) (Value, error) {
			if err := os.WriteFile(a[0].(string), []byte(a[1].(string)), 0o644); err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			return nil, nil
		},
		"append": func(th *Thread, _ Value, a []Value) (Value, error) {
			f, err := os.OpenFile(a[0].(string), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				_, err = f.WriteString(a[1].(string))
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			return nil, nil
		},
		"exists": func(th *Thread, _ Value, a []Value) (Value, error) {
			_, err := os.Stat(a[0].(string))
			return err == nil, nil
		},
		"list": func(th *Thread, _ Value, a []Value) (Value, error) {
			es, err := os.ReadDir(a[0].(string))
			if err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			var names []string
			for _, e := range es {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			return strList(names), nil
		},
		"remove": func(th *Thread, _ Value, a []Value) (Value, error) {
			if err := os.Remove(a[0].(string)); err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			return nil, nil
		},
		"mkdir": func(th *Thread, _ Value, a []Value) (Value, error) {
			if err := os.MkdirAll(a[0].(string), 0o755); err != nil {
				return nil, th.fail("E_IO", "%v", err)
			}
			return nil, nil
		},
	})

	register("os", map[string]BuiltinFn{
		"args": func(th *Thread, _ Value, a []Value) (Value, error) { return strList(th.in.opt.Args), nil },
		"env": func(th *Thread, _ Value, a []Value) (Value, error) {
			v, ok := os.LookupEnv(a[0].(string))
			if !ok {
				return nil, nil
			}
			return v, nil
		},
		"exit": func(th *Thread, _ Value, a []Value) (Value, error) {
			return nil, &exitSig{code: int(a[0].(int64))}
		},
		"platform": func(th *Thread, _ Value, a []Value) (Value, error) {
			return runtime.GOOS + "/" + runtime.GOARCH, nil
		},
		"cwd": func(th *Thread, _ Value, a []Value) (Value, error) {
			d, _ := os.Getwd()
			return d, nil
		},
	})

	register("http", map[string]BuiltinFn{
		"get": func(th *Thread, _ Value, a []Value) (Value, error) {
			return th.httpDo("GET", a[0].(string), "", a[1].(*Map), a[2].(int64))
		},
		"post": func(th *Thread, _ Value, a []Value) (Value, error) {
			return th.httpDo("POST", a[0].(string), a[1].(string), a[2].(*Map), a[3].(int64))
		},
		"request": func(th *Thread, _ Value, a []Value) (Value, error) {
			return th.httpDo(strings.ToUpper(a[0].(string)), a[1].(string), a[2].(string), a[3].(*Map), a[4].(int64))
		},
		"serve": func(th *Thread, _ Value, a []Value) (Value, error) {
			return th.httpServe(a[0].(string), a[1])
		},
		"text": func(th *Thread, _ Value, a []Value) (Value, error) {
			h := NewMap()
			h.Set("content-type", "text/plain; charset=utf-8")
			return th.httpResponse(a[0].(int64), a[1].(string), h), nil
		},
		"json": func(th *Thread, _ Value, a []Value) (Value, error) {
			var b bytes.Buffer
			if err := encodeJSON(&b, a[1], 0, 0); err != nil {
				return nil, perr(PType, "", "http.json: %v", err)
			}
			h := NewMap()
			h.Set("content-type", "application/json")
			return th.httpResponse(a[0].(int64), b.String(), h), nil
		},
	})

	register("time", map[string]BuiltinFn{
		"now": func(th *Thread, _ Value, a []Value) (Value, error) {
			return float64(time.Now().UnixNano()) / 1e9, nil
		},
		"now_ms": func(th *Thread, _ Value, a []Value) (Value, error) { return time.Now().UnixMilli(), nil },
		"iso": func(th *Thread, _ Value, a []Value) (Value, error) {
			return time.Now().UTC().Format(time.RFC3339), nil
		},
		"sleep": func(th *Thread, _ Value, a []Value) (Value, error) {
			ms := a[0].(int64)
			if ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			return nil, nil
		},
	})

	logFn := func(level string) BuiltinFn {
		return func(th *Thread, _ Value, a []Value) (Value, error) {
			var b bytes.Buffer
			b.WriteString(`{"ts":`)
			encodeJSON(&b, time.Now().UTC().Format(time.RFC3339Nano), 0, 0)
			b.WriteString(`,"level":"` + level + `","msg":`)
			encodeJSON(&b, a[0], 0, 0)
			ks, vs := a[1].(*Map).Items()
			for i := range ks {
				b.WriteByte(',')
				encodeJSON(&b, Str(ks[i]), 0, 0)
				b.WriteByte(':')
				if err := encodeJSON(&b, vs[i], 0, 0); err != nil {
					encodeJSON(&b, Str(vs[i]), 0, 0)
				}
			}
			b.WriteString("}\n")
			th.in.errw.WriteString(b.String())
			th.in.errw.Flush()
			return nil, nil
		}
	}
	register("log", map[string]BuiltinFn{
		"debug": logFn("debug"), "info": logFn("info"), "warn": logFn("warn"), "error": logFn("error"),
	})

	f1 := func(fn func(float64) float64) BuiltinFn {
		return func(th *Thread, _ Value, a []Value) (Value, error) { return fn(a[0].(float64)), nil }
	}
	toInt := func(fn func(float64) float64) BuiltinFn {
		return func(th *Thread, _ Value, a []Value) (Value, error) {
			r := fn(a[0].(float64))
			if math.IsNaN(r) || math.IsInf(r, 0) || r >= 9.223372036854775807e18 || r < -9.223372036854775808e18 {
				return nil, perr(POverflow, "", "cannot convert %s to Int", FormatFloat(r))
			}
			return int64(r), nil
		}
	}
	register("math", map[string]BuiltinFn{
		"abs": func(th *Thread, _ Value, a []Value) (Value, error) {
			switch v := a[0].(type) {
			case int64:
				if v == math.MinInt64 {
					return nil, perr(POverflow, "", "integer overflow in abs")
				}
				if v < 0 {
					return -v, nil
				}
				return v, nil
			case float64:
				return math.Abs(v), nil
			}
			return nil, perr(PType, "", "math.abs needs Int or Float, got %s", TypeName(a[0]))
		},
		"sqrt": f1(math.Sqrt), "exp": f1(math.Exp), "log": f1(math.Log),
		"sin": f1(math.Sin), "cos": f1(math.Cos), "tan": f1(math.Tan),
		"pow": func(th *Thread, _ Value, a []Value) (Value, error) {
			return math.Pow(a[0].(float64), a[1].(float64)), nil
		},
		"floor": toInt(math.Floor), "ceil": toInt(math.Ceil), "round": toInt(math.Round),
		"is_nan": func(th *Thread, _ Value, a []Value) (Value, error) { return math.IsNaN(a[0].(float64)), nil },
	})

	register("re", map[string]BuiltinFn{
		"matches": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			return re.MatchString(a[1].(string)), nil
		},
		"find": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			loc := re.FindStringIndex(a[1].(string))
			if loc == nil {
				return nil, nil
			}
			return a[1].(string)[loc[0]:loc[1]], nil
		},
		"find_all": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			return strList(re.FindAllString(a[1].(string), -1)), nil
		},
		"groups": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			m := re.FindStringSubmatch(a[1].(string))
			if m == nil {
				return nil, nil
			}
			return strList(m), nil
		},
		"replace": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			return re.ReplaceAllString(a[1].(string), a[2].(string)), nil
		},
		"split": func(th *Thread, _ Value, a []Value) (Value, error) {
			re, err := th.regex(a[0].(string))
			if err != nil {
				return nil, err
			}
			return strList(re.Split(a[1].(string), -1)), nil
		},
	})

	register("proc", map[string]BuiltinFn{
		"run": func(th *Thread, _ Value, a []Value) (Value, error) {
			items := a[0].(*List).Snapshot()
			if len(items) == 0 {
				return nil, perr(PArgs, "", "proc.run: empty command")
			}
			args := make([]string, len(items))
			for i, it := range items {
				s, ok := it.(string)
				if !ok {
					return nil, perr(PType, "", "proc.run: command items must be Str, got %s", TypeName(it))
				}
				args[i] = s
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a[2].(int64))*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Stdin = strings.NewReader(a[1].(string))
			var so, se bytes.Buffer
			cmd.Stdout, cmd.Stderr = &so, &se
			err := cmd.Run()
			code := 0
			if err != nil {
				var ee *exec.ExitError
				if ctx.Err() != nil {
					return nil, th.fail("E_TIMEOUT", "command timed out: %s", args[0])
				}
				if errors.As(err, &ee) {
					code = ee.ExitCode()
				} else {
					return nil, th.fail("E_PROC", "%v", err)
				}
			}
			st := th.in.stdModule("proc").Types["Result"].(*StructType)
			return &Struct{T: st, F: []Value{int64(code), so.String(), se.String()}}, nil
		},
	})

	register("rand", map[string]BuiltinFn{
		"int": func(th *Thread, _ Value, a []Value) (Value, error) {
			lo, hi := a[0].(int64), a[1].(int64)
			if hi <= lo {
				return nil, perr(PArgs, "", "rand.int: hi (%d) must be greater than lo (%d)", hi, lo)
			}
			th.in.rngMu.Lock()
			defer th.in.rngMu.Unlock()
			return lo + th.in.rng.Int63n(hi-lo), nil
		},
		"float": func(th *Thread, _ Value, a []Value) (Value, error) {
			th.in.rngMu.Lock()
			defer th.in.rngMu.Unlock()
			return th.in.rng.Float64(), nil
		},
		"choice": func(th *Thread, _ Value, a []Value) (Value, error) {
			items := a[0].(*List).Snapshot()
			if len(items) == 0 {
				return nil, nil
			}
			th.in.rngMu.Lock()
			defer th.in.rngMu.Unlock()
			return items[th.in.rng.Intn(len(items))], nil
		},
		"shuffle": func(th *Thread, _ Value, a []Value) (Value, error) {
			items := a[0].(*List).Snapshot()
			th.in.rngMu.Lock()
			th.in.rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
			th.in.rngMu.Unlock()
			return NewList(items), nil
		},
		"uuid": func(th *Thread, _ Value, a []Value) (Value, error) {
			var b [16]byte
			th.in.rngMu.Lock()
			for i := range b {
				b[i] = byte(th.in.rng.Intn(256))
			}
			th.in.rngMu.Unlock()
			b[6] = (b[6] & 0x0f) | 0x40
			b[8] = (b[8] & 0x3f) | 0x80
			return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
		},
	})
}

// ---------- regex ----------

var reCache sync.Map

func (th *Thread) regex(p string) (*regexp.Regexp, error) {
	if re, ok := reCache.Load(p); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, th.fail("E_REGEX", "invalid regex %q: %v", p, err)
	}
	reCache.Store(p, re)
	return re, nil
}

// ---------- http ----------

func (th *Thread) httpResponse(status int64, body string, headers *Map) *Struct {
	st := th.in.stdModule("http").Types["Response"].(*StructType)
	return &Struct{T: st, F: []Value{status, body, headers}}
}

func (th *Thread) httpDo(method, url, body string, headers *Map, timeoutMs int64) (Value, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, th.fail("E_NET", "%v", err)
	}
	ks, vs := headers.Items()
	for i := range ks {
		req.Header.Set(Str(ks[i]), Str(vs[i]))
	}
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, th.fail("E_NET", "%v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, th.fail("E_NET", "%v", err)
	}
	h := NewMap()
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		h.Set(strings.ToLower(k), resp.Header.Get(k))
	}
	return th.httpResponse(int64(resp.StatusCode), string(b), h), nil
}

func (th *Thread) httpServe(addr string, handler Value) (Value, error) {
	in := th.in
	mod := th.mod()
	reqType := in.stdModule("http").Types["Request"].(*StructType)
	respType := in.stdModule("http").Types["Response"].(*StructType)
	logErr := func(msg string, extra map[string]string) {
		var b bytes.Buffer
		b.WriteString(`{"ts":`)
		encodeJSON(&b, time.Now().UTC().Format(time.RFC3339Nano), 0, 0)
		b.WriteString(`,"level":"error","msg":`)
		encodeJSON(&b, msg, 0, 0)
		for k, v := range extra {
			b.WriteString(`,`)
			encodeJSON(&b, k, 0, 0)
			b.WriteString(`:`)
			encodeJSON(&b, v, 0, 0)
		}
		b.WriteString("}\n")
		in.errw.WriteString(b.String())
		in.errw.Flush()
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		q := NewMap()
		qk := make([]string, 0)
		for k := range r.URL.Query() {
			qk = append(qk, k)
		}
		sort.Strings(qk)
		for _, k := range qk {
			q.Set(k, r.URL.Query().Get(k))
		}
		hd := NewMap()
		hk := make([]string, 0)
		for k := range r.Header {
			hk = append(hk, k)
		}
		sort.Strings(hk)
		for _, k := range hk {
			hd.Set(strings.ToLower(k), r.Header.Get(k))
		}
		req := &Struct{T: reqType, F: []Value{r.Method, r.URL.Path, q, hd, string(body)}}
		nth := in.newThread(mod)
		var res Value
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = &Panic{Code: PInternal, Message: fmt.Sprint(p)}
				}
			}()
			defer nth.flushSteps()
			res, err = nth.callValue(handler, []Value{req}, nil, ast.Pos{})
		}()
		if err != nil {
			logErr("handler error", map[string]string{"error": err.Error(), "path": r.URL.Path})
			http.Error(w, "internal error", 500)
			return
		}
		resp, ok := res.(*Struct)
		if !ok || resp.T != respType {
			logErr("handler must return http.Response", map[string]string{"got": TypeName(res)})
			http.Error(w, "internal error", 500)
			return
		}
		f := resp.Snapshot()
		if hm, ok := f[2].(*Map); ok {
			ks, vs := hm.Items()
			for i := range ks {
				w.Header().Set(Str(ks[i]), Str(vs[i]))
			}
		}
		status, _ := f[0].(int64)
		if status < 100 || status > 999 {
			status = 500
		}
		w.WriteHeader(int(status))
		io.WriteString(w, Str(f[1]))
	})
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		if err != nil && err != http.ErrServerClosed {
			return nil, th.fail("E_NET", "%v", err)
		}
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}
	return nil, nil
}

// ---------- JSON ----------

func encodeJSON(b *bytes.Buffer, v Value, indent, depth int) error {
	if depth > 200 {
		return fmt.Errorf("value too deeply nested")
	}
	nl := func(d int) {
		if indent > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*d))
		}
	}
	colon := ":"
	if indent > 0 {
		colon = ": "
	}
	writeObj := func(keys []string, vals []Value) error {
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(depth + 1)
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteString(colon)
			if err := encodeJSON(b, vals[i], indent, depth+1); err != nil {
				return err
			}
		}
		if len(keys) > 0 {
			nl(depth)
		}
		b.WriteByte('}')
		return nil
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
		} else {
			s := strconv.FormatFloat(x, 'g', -1, 64)
			if !strings.ContainsAny(s, ".e") {
				s += ".0"
			}
			b.WriteString(s)
		}
	case string:
		enc := json.NewEncoder(b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(x); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // drop the newline added by Encode
	case *List:
		items := x.Snapshot()
		b.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(depth + 1)
			if err := encodeJSON(b, it, indent, depth+1); err != nil {
				return err
			}
		}
		if len(items) > 0 {
			nl(depth)
		}
		b.WriteByte(']')
	case *Map:
		ks, vs := x.Items()
		keys := make([]string, len(ks))
		for i, k := range ks {
			keys[i] = Str(k)
		}
		return writeObj(keys, vs)
	case *Struct:
		return writeObj(fieldNames(x.T), x.Snapshot())
	case *Enum:
		keys := []string{"variant"}
		vals := []Value{x.V.Name}
		for i, f := range x.V.Fields {
			keys = append(keys, f.Name)
			vals = append(vals, x.F[i])
		}
		return writeObj(keys, vals)
	default:
		return fmt.Errorf("cannot encode %s as JSON", TypeName(v))
	}
	return nil
}

func decodeJSON(s string) (Value, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := NewMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				m.Set(kt.(string), v)
			}
			_, err := dec.Token()
			return m, err
		case '[':
			items := []Value{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
			_, err := dec.Token()
			return NewList(items), err
		}
	case json.Number:
		str := t.String()
		if !strings.ContainsAny(str, ".eE") {
			if n, err := strconv.ParseInt(str, 10, 64); err == nil {
				return n, nil
			}
		}
		f, err := strconv.ParseFloat(str, 64)
		return f, err
	case string:
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// convert validates/converts decoded JSON against a declared type.
func (th *Thread) convert(v Value, te *ast.TypeExpr, m *Module, path string) (Value, string) {
	if te == nil {
		return v, ""
	}
	if v == nil {
		if te.Optional || te.Name == "Any" {
			return nil, ""
		}
		return nil, fmt.Sprintf("%s: expected %s, got null", path, te.Name)
	}
	if big, ok := v.(pyBigInt); ok {
		if te.Name == "Any" {
			return string(big), ""
		}
		return nil, fmt.Sprintf("%s: integer %s does not fit Int (64-bit)", path, string(big))
	}
	bad := func() (Value, string) {
		return nil, fmt.Sprintf("%s: expected %s, got %s", path, te.Name, jsonKind(v))
	}
	switch te.Name {
	case "Any":
		return sanitizeAny(v), ""
	case "Int":
		if _, ok := v.(int64); ok {
			return v, ""
		}
		return bad()
	case "Float":
		switch n := v.(type) {
		case float64:
			return n, ""
		case int64:
			return float64(n), ""
		}
		return bad()
	case "Str":
		if _, ok := v.(string); ok {
			return v, ""
		}
		return bad()
	case "Bool":
		if _, ok := v.(bool); ok {
			return v, ""
		}
		return bad()
	case "List":
		l, ok := v.(*List)
		if !ok {
			return bad()
		}
		var et *ast.TypeExpr
		if len(te.Args) == 1 {
			et = te.Args[0]
		}
		items := l.Snapshot()
		for i, it := range items {
			c, err := th.convert(it, et, m, fmt.Sprintf("%s[%d]", path, i))
			if err != "" {
				return nil, err
			}
			items[i] = c
		}
		return NewList(items), ""
	case "Map":
		mp, ok := v.(*Map)
		if !ok {
			return bad()
		}
		var vt *ast.TypeExpr
		if len(te.Args) == 2 {
			vt = te.Args[1]
		}
		out := NewMap()
		ks, vs := mp.Items()
		for i := range ks {
			c, err := th.convert(vs[i], vt, m, path+"."+Str(ks[i]))
			if err != "" {
				return nil, err
			}
			out.Set(ks[i], c)
		}
		return out, ""
	}
	var t any
	if i := strings.IndexByte(te.Name, '.'); i >= 0 {
		if im := m.Imports[te.Name[:i]]; im != nil {
			t = im.Types[te.Name[i+1:]]
		}
	} else {
		t = m.Types[te.Name]
	}
	obj, isObj := v.(*Map)
	switch t := t.(type) {
	case *ExternType:
		h, ok := v.(*PyHandle)
		if !ok {
			return bad()
		}
		if h.T == nil {
			h.T = t
		} else if h.T != t {
			return nil, fmt.Sprintf("%s: expected %s, got %s", path, t.Name, h.T.Name)
		}
		return h, ""
	case *StructType:
		if !isObj {
			return bad()
		}
		vals := make([]Value, len(t.Fields))
		for i, f := range t.Fields {
			raw, present := obj.Get(f.Name)
			if !present {
				if f.Default != nil {
					d, err := th.in.evalDetached(t.Module, t.Module.Env, f.Default)
					if err != nil {
						return nil, err.Error()
					}
					vals[i] = d
					continue
				}
				if f.Type != nil && f.Type.Optional {
					continue
				}
				return nil, fmt.Sprintf("%s: missing field '%s'", path, f.Name)
			}
			c, err := th.convert(raw, f.Type, t.Module, path+"."+f.Name)
			if err != "" {
				return nil, err
			}
			vals[i] = c
		}
		return &Struct{T: t, F: vals}, ""
	case *EnumType:
		if !isObj {
			return bad()
		}
		name, _ := obj.Get("variant")
		vi := t.ByName[Str(name)]
		if vi == nil {
			return nil, fmt.Sprintf("%s: unknown variant %s of %s", path, Repr(name), t.Name)
		}
		vals := make([]Value, len(vi.Fields))
		for i, f := range vi.Fields {
			raw, _ := obj.Get(f.Name)
			c, err := th.convert(raw, f.Type, t.Module, path+"."+f.Name)
			if err != "" {
				return nil, err
			}
			vals[i] = c
		}
		return &Enum{V: vi, F: vals}, ""
	}
	return v, ""
}

// sanitizeAny replaces transient bridge values (huge ints) inside Any data.
func sanitizeAny(v Value) Value {
	switch x := v.(type) {
	case pyBigInt:
		return string(x)
	case *List:
		items := x.Snapshot()
		for i, it := range items {
			items[i] = sanitizeAny(it)
		}
		return NewList(items)
	case *Map:
		ks, vs := x.Items()
		m := NewMap()
		for i := range ks {
			m.Set(ks[i], sanitizeAny(vs[i]))
		}
		return m
	}
	return v
}

func jsonKind(v Value) string {
	switch x := v.(type) {
	case *PyHandle:
		return "Python object " + x.PyType
	case *Map:
		return "object"
	case *List:
		return "array"
	case string:
		return "string"
	case int64, float64:
		return "number"
	case bool:
		return "boolean"
	}
	return "null"
}

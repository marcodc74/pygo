package interp

// Trusted text: html"..." literals (Html) and sql"..." literals (Sql).
//
// Html is only produced by html literals (every interpolated value escaped
// for its context by html/template) and by html.raw; Sql only by sql
// literals, which cannot interpolate. Neither can be decoded from JSON or
// returned by Python, so untrusted text never becomes trusted.

import (
	"html/template"
	"math"
	"strings"
	"sync"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/safehtml"
)

// HtmlStr is a value of type Html.
type HtmlStr string

// SqlStr is a value of type Sql.
type SqlStr string

// htmlTemplates caches the compiled template of each html literal.
var htmlTemplates sync.Map // *ast.StrLit -> *template.Template

// htmlFloat renders a Float like the rest of Pygo (1.0, not 1) in markup
// and as a JSON number inside <script>.
type htmlFloat float64

func (f htmlFloat) String() string { return FormatFloat(float64(f)) }

func (f htmlFloat) MarshalJSON() ([]byte, error) {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return []byte("null"), nil
	}
	return []byte(FormatFloat(float64(f))), nil
}

// renderHtml renders an html literal; vals are its interpolated values in
// order. Both engines call it after evaluating all the values.
func (th *Thread) renderHtml(e *ast.StrLit, vals []Value) (Value, error) {
	var t *template.Template
	if c, ok := htmlTemplates.Load(e); ok {
		t = c.(*template.Template)
	} else {
		var err error
		t, err = safehtml.Compile(e.Parts)
		if err != nil {
			return nil, th.panicAt(e.Pos, PType, "", "%s", err)
		}
		htmlTemplates.Store(e, t)
	}
	args := make([]any, 0, len(vals))
	k := 0
	for _, p := range e.Parts {
		if p.Expr == nil {
			continue
		}
		a, err := th.htmlArg(p, vals[k])
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		k++
	}
	s, err := safehtml.Render(t, args)
	if err != nil {
		return nil, th.panicAt(e.Pos, PInternal, "", "html literal: %v", err)
	}
	return HtmlStr(s), nil
}

// htmlArg converts an interpolated value for html/template: text is
// escaped for its context, Html is inserted as it is.
func (th *Thread) htmlArg(p ast.StrPart, v Value) (any, error) {
	const allowed = "interpolate Str, Int, Float, Bool, Html or List[Html]"
	if p.Format != "" {
		switch v.(type) {
		case string, int64, float64, bool:
			s, err := th.formatPart(p, v)
			return s, err
		}
		return nil, th.panicAt(p.Expr.P(), PType, "format specs apply to Str, Int, Float and Bool", "html literal: cannot format %s", TypeName(v))
	}
	switch v := v.(type) {
	case string, int64, bool:
		return v, nil
	case float64:
		return htmlFloat(v), nil
	case HtmlStr:
		return template.HTML(v), nil
	case *List:
		var b strings.Builder
		for _, x := range v.Snapshot() {
			h, ok := x.(HtmlStr)
			if !ok {
				return nil, th.panicAt(p.Expr.P(), PType, `build each item with html"..."`, "html literal: list item is %s, not Html", TypeName(x))
			}
			b.WriteString(string(h))
		}
		return template.HTML(b.String()), nil
	}
	return nil, th.panicAt(p.Expr.P(), PType, allowed, "html literal: cannot interpolate %s", TypeName(v))
}

// evalTrusted evaluates an html or sql literal on the interpreter.
func (th *Thread) evalTrusted(env *Env, e *ast.StrLit) (Value, error) {
	if e.Kind == "sql" {
		return SqlStr(sqlText(e)), nil
	}
	var vals []Value
	for _, p := range e.Parts {
		if p.Expr == nil {
			continue
		}
		v, err := th.eval(env, p.Expr)
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	return th.renderHtml(e, vals)
}

// sqlText is the text of a sql literal (the lexer rejects interpolation).
func sqlText(e *ast.StrLit) string {
	var b strings.Builder
	for _, p := range e.Parts {
		b.WriteString(p.Lit)
	}
	return b.String()
}

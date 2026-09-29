// Package safehtml compiles html"..." literals. The literal text is the
// trusted template; every ${expr} becomes an action that html/template
// escapes according to where it appears (text, attribute, URL, script,
// style). The checker compiles each literal once to report context errors
// at compile time; the runtime compiles it again and executes it.
package safehtml

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/printer"
)

// Action delimiters: control characters that the literal text cannot
// contain by accident (the lexer only produces them from \u{..} escapes).
const (
	left  = "\x00\x01"
	right = "\x01\x00"
)

// Error is a problem with the structure of an html literal.
type Error struct {
	Msg  string
	Hint string
}

func (e *Error) Error() string { return e.Msg }

// Compile turns the parts of an html literal into an escaping template.
// The k-th interpolation (counting expressions only) is the k-th argument
// of Execute.
func Compile(parts []ast.StrPart) (*template.Template, error) {
	var b strings.Builder
	var exprs []ast.StrPart
	for _, p := range parts {
		if p.Expr == nil {
			if strings.Contains(p.Lit, left) || strings.Contains(p.Lit, right) {
				return nil, &Error{Msg: `html literal contains the reserved sequence \0\u{1} or \u{1}\0`}
			}
			b.WriteString(p.Lit)
			continue
		}
		fmt.Fprintf(&b, "%sindex . %d%s", left, len(exprs), right)
		exprs = append(exprs, p)
	}
	t, err := template.New("html").Delims(left, right).Parse(b.String())
	if err != nil {
		return nil, describe(err, exprs)
	}
	// html/template escapes on the first execution: run it once now, so
	// context errors surface here and later executions only render.
	args := make([]any, len(exprs))
	for i := range args {
		args[i] = ""
	}
	if err := t.Execute(io.Discard, args); err != nil {
		return nil, describe(err, exprs)
	}
	return t, nil
}

var actionRe = regexp.MustCompile(`\{\{index \. (\d+)\}\}`)

// describe turns an html/template error into a message that names the
// literal's own ${...} expressions.
func describe(err error, exprs []ast.StrPart) error {
	msg := err.Error()
	code := template.ErrorCode(0)
	var te *template.Error
	if errors.As(err, &te) {
		msg, code = te.Description, te.ErrorCode
	}
	msg = actionRe.ReplaceAllStringFunc(msg, func(m string) string {
		k, _ := strconv.Atoi(actionRe.FindStringSubmatch(m)[1])
		if k < len(exprs) {
			return "${" + printer.Expr(exprs[k].Expr) + "}"
		}
		return m
	})
	hint := "keep each literal a well-formed fragment: close tags, quotes and comments inside it"
	switch code {
	case template.ErrEndContext:
		msg = "the literal ends inside a tag, attribute, comment, script or style"
	case template.ErrAmbigContext:
		hint = "quote the attribute and interpolate a whole URL or a query value: href=\"/items?id=${id}\""
	case template.ErrBadHTML:
		hint = "interpolate only attribute values (quoted) or text between tags"
	}
	return &Error{Msg: "html literal: " + msg, Hint: hint}
}

// Render executes a compiled literal with the interpolated values
// (string, int64, bool, fmt.Stringer, template.HTML, ...).
func Render(t *template.Template, args []any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, args); err != nil {
		return "", err
	}
	return b.String(), nil
}

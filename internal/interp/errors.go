package interp

import (
	"fmt"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
)

// Control-flow signals travel as Go errors through eval/exec.
type returnSig struct{ val Value }
type breakSig struct{}
type continueSig struct{}

func (*returnSig) Error() string   { return "return outside function" }
func (*breakSig) Error() string    { return "break outside loop" }
func (*continueSig) Error() string { return "continue outside loop" }

// exitSig is raised by os.exit.
type exitSig struct{ code int }

func (e *exitSig) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Failure is a recoverable error raised by `fail` (or a fallible builtin).
// It can be handled with try/catch.
type Failure struct {
	Err   *Struct // value of type Error
	Trace []string
}

func (f *Failure) Error() string {
	msg, _ := f.Err.Field("message")
	return fmt.Sprint(msg)
}

func (f *Failure) Code() string {
	c, _ := f.Err.Field("code")
	s, _ := c.(string)
	return s
}

// Panic is an unrecoverable error: a bug in the program (or a denied
// capability, an exhausted budget, ...). It aborts the program.
type Panic struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	File    string            `json:"file"`
	Line    int               `json:"line"`
	Col     int               `json:"col"`
	Hint    string            `json:"hint,omitempty"`
	Values  map[string]string `json:"values,omitempty"`
	Trace   []string          `json:"trace,omitempty"`
}

func (p *Panic) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d:%d: panic[%s]: %s", p.File, p.Line, p.Col, p.Code, p.Message)
	if len(p.Values) > 0 {
		b.WriteString("\n    values:")
		for _, k := range sortedKeys(p.Values) {
			fmt.Fprintf(&b, " %s = %s;", k, p.Values[k])
		}
	}
	if p.Hint != "" {
		fmt.Fprintf(&b, "\n    hint: %s", p.Hint)
	}
	for _, t := range p.Trace {
		fmt.Fprintf(&b, "\n    at %s", t)
	}
	return b.String()
}

// Runtime panic codes (stable, documented by `pygo explain`).
const (
	PType       = "R0001" // wrong type at runtime
	PIndex      = "R0002" // index out of range
	PKey        = "R0003" // missing map key
	PDivZero    = "R0004" // division by zero
	POverflow   = "R0005" // integer overflow
	PNil        = "R0006" // nil where a value is required
	PAssert     = "R0007" // assertion failed
	PContract   = "R0008" // requires/ensures violated
	PUnhandled  = "R0009" // failure not handled with try/catch
	PPermission = "R0010" // capability not granted (--allow)
	PBudget     = "R0011" // --max-steps exhausted
	PTimeout    = "R0012" // --timeout exceeded
	PUser       = "R0013" // panic(...) called
	PName       = "R0014" // unknown name/field/method at runtime
	PArgs       = "R0015" // wrong arguments
	PMatch      = "R0016" // no match arm matched
	PChan       = "R0017" // channel misuse
	PInternal   = "R0099" // interpreter bug
)

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

// panicAt builds a Panic at the given position with the thread's trace.
func (th *Thread) panicAt(pos ast.Pos, code, hint, format string, args ...any) *Panic {
	return &Panic{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		File:    th.file(),
		Line:    pos.Line,
		Col:     pos.Col,
		Hint:    hint,
		Trace:   th.trace(),
	}
}

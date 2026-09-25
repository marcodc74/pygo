package interp

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marcodc74/pygo/internal/ast"
)

// Exit codes (stable, for agents).
const (
	ExitOK         = 0
	ExitFailure    = 1 // main failed with an unhandled failure
	ExitPanic      = 2 // runtime panic (bug)
	ExitCompile    = 3 // lexer/parser/checker errors
	ExitPermission = 4 // capability not granted
)

// Result describes the outcome of a run.
type Result struct {
	ExitCode int    `json:"exit_code"`
	Status   string `json:"status"` // ok | failure | panic | exit
	Panic    *Panic `json:"panic,omitempty"`
	Error    *struct {
		Message string   `json:"message"`
		Code    string   `json:"code,omitempty"`
		Trace   []string `json:"trace,omitempty"`
	} `json:"error,omitempty"`
	Steps int64 `json:"steps"`
}

// Run loads the main module and calls main().
func (in *Interp) Run() *Result {
	defer in.Flush()
	defer in.ClosePython()
	res := in.guard(func() error {
		m, err := in.Load(in.prog.Main)
		if err != nil {
			return err
		}
		mainFn, ok := m.Members["main"].(*Function)
		if !ok {
			return &Panic{Code: PName, Message: "no main function", File: in.prog.Main, Hint: "add: fn main() { ... }"}
		}
		th := in.newThread(m)
		defer th.flushSteps()
		_, err = th.callFunction(mainFn, nil, nil, nil)
		return err
	})
	return res
}

func (in *Interp) guard(f func() error) (res *Result) {
	res = &Result{Status: "ok"}
	defer func() {
		if r := recover(); r != nil {
			res.ExitCode, res.Status = ExitPanic, "panic"
			res.Panic = &Panic{Code: PInternal, Message: fmt.Sprint("internal error: ", r)}
		}
		res.Steps = in.steps.Load()
	}()
	err := f()
	if err == nil {
		return res
	}
	return in.describeErr(err, res)
}

func (in *Interp) describeErr(err error, res *Result) *Result {
	switch e := err.(type) {
	case *exitSig:
		res.ExitCode, res.Status = e.code, "exit"
	case *Failure:
		res.ExitCode, res.Status = ExitFailure, "failure"
		res.Error = &struct {
			Message string   `json:"message"`
			Code    string   `json:"code,omitempty"`
			Trace   []string `json:"trace,omitempty"`
		}{e.Error(), e.Code(), e.Trace}
	case *Panic:
		res.ExitCode, res.Status = ExitPanic, "panic"
		if e.Code == PPermission {
			res.ExitCode = ExitPermission
		}
		res.Panic = e
	default:
		res.ExitCode, res.Status = ExitPanic, "panic"
		res.Panic = &Panic{Code: PInternal, Message: err.Error()}
	}
	return res
}

// TestResult is the outcome of one test block.
type TestResult struct {
	Name    string  `json:"name"`
	File    string  `json:"file"`
	Line    int     `json:"line"`
	Passed  bool    `json:"passed"`
	Millis  float64 `json:"ms"`
	Failure *Result `json:"failure,omitempty"`
	Output  string  `json:"-"`
}

// RunTests runs the test blocks of the given files (all loaded files if nil).
func (in *Interp) RunTests(files []string, filter string) []TestResult {
	defer in.Flush()
	defer in.ClosePython()
	if files == nil {
		files = []string{in.prog.Main}
	}
	var out []TestResult
	for _, file := range files {
		var m *Module
		loadRes := in.guard(func() error {
			var err error
			m, err = in.Load(file)
			return err
		})
		if loadRes.Status != "ok" {
			out = append(out, TestResult{Name: "<module init>", File: file, Failure: loadRes})
			continue
		}
		f := in.prog.Files[file]
		for _, d := range f.Decls {
			td, ok := d.(*ast.TestDecl)
			if !ok || (filter != "" && !strings.Contains(td.Name, filter)) {
				continue
			}
			start := time.Now()
			r := in.guard(func() error {
				th := in.newThread(m)
				defer th.flushSteps()
				th.frames[0].name = "test " + fmt.Sprintf("%q", td.Name)
				th.tryDepth = 1 // `try` at test top level propagates as a test failure
				if in.opt.UseVM() {
					var p *Proto
					var cerr error
					if in.opt.Compiled != nil && in.opt.Compiled.Tests[td] != nil {
						p = in.opt.Compiled.Tests[td].instance()
					} else {
						p, cerr = compileFunc(th.frames[0].name, false, nil, td.Body, nil)
					}
					if cerr == nil {
						_, err := th.runProto(&Function{Name: th.frames[0].name, Mod: m, Env: m.Env}, p, nil)
						return err
					}
					in.vmFallback(th.frames[0].name, cerr)
				}
				err := th.execBlock(m.Env, td.Body)
				if _, ok := err.(*returnSig); ok {
					return nil
				}
				return err
			})
			tr := TestResult{Name: td.Name, File: file, Line: td.Pos.Line, Passed: r.Status == "ok",
				Millis: float64(time.Since(start).Microseconds()) / 1000}
			if !tr.Passed {
				tr.Failure = r
			}
			out = append(out, tr)
		}
	}
	return out
}

// Describe renders a panic or failure for humans/agents (one block of text).
func (r *Result) Describe() string {
	switch r.Status {
	case "panic":
		return r.Panic.Error()
	case "failure":
		var b strings.Builder
		fmt.Fprintf(&b, "failure: %s", r.Error.Message)
		if r.Error.Code != "" {
			fmt.Fprintf(&b, " [%s]", r.Error.Code)
		}
		for _, t := range r.Error.Trace {
			fmt.Fprintf(&b, "\n    at %s", t)
		}
		return b.String()
	}
	return r.Status
}

// SortedAllow returns the granted capabilities (for messages).
func (in *Interp) SortedAllow() []string {
	var out []string
	for k, v := range in.opt.Allow {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

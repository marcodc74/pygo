package interp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

// runSrc runs a program and returns stdout and the result.
func runSrc(t *testing.T, src string, allow ...string) (string, *Result) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	al := map[string]bool{}
	for _, a := range allow {
		al[a] = true
	}
	return runBoth(t, prog, Options{Allow: al, MaxSteps: 1_000_000})
}

// runBoth runs a program on the interpreter, on the bytecode VM, and on
// the VM from a .pgc round trip (compile, encode, decode), and fails the
// test if they disagree in any observable way.
func runBoth(t *testing.T, prog *loader.Program, opt Options) (string, *Result) {
	t.Helper()
	run := func(p *loader.Program, o Options) (string, string, *Interp, *Result) {
		var out bytes.Buffer
		o.Stdout, o.Stderr = &out, &out
		in := New(p, o)
		res := in.Run()
		j, _ := json.MarshalIndent(res, "", "  ")
		return out.String(), string(j), in, res
	}
	optT, optV := opt, opt
	optT.Engine, optV.Engine = "tree", "vm"
	outT, resT, _, result := run(prog, optT)
	outV, resV, inV, _ := run(prog, optV)
	if fb := inV.VMFallbacks(); len(fb) > 0 {
		t.Errorf("functions not compiled to bytecode: %v", fb)
	}
	compare := func(name, out, res string) {
		t.Helper()
		if outT != out {
			t.Errorf("engines differ in output\n--- tree ---\n%s\n--- %s ---\n%s", outT, name, out)
		}
		if resT != res {
			t.Errorf("engines differ in result\n--- tree ---\n%s\n--- %s ---\n%s", resT, name, res)
		}
	}
	compare("vm", outV, resV)

	pprog, compiled := pgcRoundTrip(t, prog)
	optP := optV
	optP.Compiled = compiled
	outP, resP, _, _ := run(pprog, optP)
	compare("pgc", outP, resP)
	return outT, result
}

// pgcRoundTrip compiles prog, encodes it as .pgc (twice, checking that
// the bytes are identical) and decodes it.
func pgcRoundTrip(t *testing.T, prog *loader.Program) (*loader.Program, *Compiled) {
	t.Helper()
	c1, _ := CompileProgram(prog)
	data, err := EncodePgc(prog, c1, "test")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	c2, _ := CompileProgram(prog)
	data2, _ := EncodePgc(prog, c2, "test")
	if !bytes.Equal(data, data2) {
		t.Errorf("pgc encoding is not deterministic")
	}
	pprog, compiled, _, err := DecodePgc(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// the decoded declarations must be the keys of the decoded bytecode
	// (otherwise the runtime would silently compile again)
	for _, f := range pprog.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if compiled.Funcs[d] == nil {
					t.Errorf("pgc: no bytecode for fn %s", d.Name)
				}
			case *ast.ImplDecl:
				for _, md := range d.Methods {
					if compiled.Funcs[md] == nil {
						t.Errorf("pgc: no bytecode for %s.%s", d.Type, md.Name)
					}
				}
			case *ast.TestDecl:
				if compiled.Tests[d] == nil {
					t.Errorf("pgc: no bytecode for test %q", d.Name)
				}
			}
		}
	}
	return pprog, compiled
}

func expectOut(t *testing.T, src, want string) {
	t.Helper()
	out, res := runSrc(t, src)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(want) {
		t.Fatalf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

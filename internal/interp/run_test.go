package interp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// runBoth runs a program on the interpreter and on the bytecode VM and
// fails the test if the two engines disagree in any observable way.
func runBoth(t *testing.T, prog *loader.Program, opt Options) (string, *Result) {
	t.Helper()
	var outT, outV bytes.Buffer
	optT, optV := opt, opt
	optT.Engine, optV.Engine = "tree", "vm"
	optT.Stdout, optT.Stderr = &outT, &outT
	optV.Stdout, optV.Stderr = &outV, &outV
	resT := New(prog, optT).Run()
	inV := New(prog, optV)
	resV := inV.Run()
	if fb := inV.VMFallbacks(); len(fb) > 0 {
		t.Errorf("functions not compiled to bytecode: %v", fb)
	}
	if outT.String() != outV.String() {
		t.Errorf("engines differ in output\n--- tree ---\n%s\n--- vm ---\n%s", outT.String(), outV.String())
	}
	jt, _ := json.MarshalIndent(resT, "", "  ")
	jv, _ := json.MarshalIndent(resV, "", "  ")
	if string(jt) != string(jv) {
		t.Errorf("engines differ in result\n--- tree ---\n%s\n--- vm ---\n%s", jt, jv)
	}
	return outT.String(), resT
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

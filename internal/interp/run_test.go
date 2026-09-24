package interp

import (
	"bytes"
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
	var out bytes.Buffer
	al := map[string]bool{}
	for _, a := range allow {
		al[a] = true
	}
	in := New(prog, Options{Stdout: &out, Stderr: &out, Allow: al, MaxSteps: 1_000_000})
	res := in.Run()
	return out.String(), res
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

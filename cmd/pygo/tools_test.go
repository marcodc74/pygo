package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs fn with os.Stdout and os.Stderr redirected to files and returns
// their contents plus fn's exit code.
func capture(t *testing.T, fn func() int) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	of, err := os.CreateTemp(dir, "out")
	if err != nil {
		t.Fatal(err)
	}
	ef, err := os.CreateTemp(dir, "err")
	if err != nil {
		t.Fatal(err)
	}
	so, se := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = of, ef
	code := fn()
	os.Stdout, os.Stderr = so, se
	of.Close()
	ef.Close()
	ob, _ := os.ReadFile(of.Name())
	eb, _ := os.ReadFile(ef.Name())
	return string(ob), string(eb), code
}

// run must reject invalid flags with a usage error (exit 2) before running.
func TestRunRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--allow", "bogus", "x.pg"},
		{"--max-steps", "-5", "x.pg"},
		{"--timeout", "-1s", "x.pg"},
	} {
		if _, _, code := capture(t, func() int { return cmdRun(args) }); code != 2 {
			t.Fatalf("cmdRun(%v): exit %d, want 2", args, code)
		}
	}
}

// describe and outline always print JSON; they must accept --json as a no-op so
// an agent can pass it to every subcommand.
func TestDescribeOutlineAcceptJSONFlag(t *testing.T) {
	if _, _, code := capture(t, func() int { return cmdDescribe([]string{"--json", "json"}) }); code != 0 {
		t.Fatalf("describe --json json: exit %d", code)
	}
	hello := filepath.Join("..", "..", "examples", "hello.pg")
	if _, _, code := capture(t, func() int { return cmdOutline([]string{"--json", hello}) }); code != 0 {
		t.Fatalf("outline --json: exit %d", code)
	}
}

// fix --dry-run --json must keep stdout machine-readable: the source preview
// goes to stderr, the JSON summary to stdout.
func TestFixDryRunJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.pg")
	src := "fn f(a: Int, b: Int) -> Int => a + b\nfn main() { print(f(1, 2)) }\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := capture(t, func() int { return cmdFix([]string{"--dry-run", "--json", p}) })
	if code != 0 {
		t.Fatalf("fix exit %d", code)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("stdout is not JSON: %q", out)
	}
	if !strings.Contains(errOut, "fn main") {
		t.Fatalf("stderr should carry the dry-run source, got %q", errOut)
	}
}

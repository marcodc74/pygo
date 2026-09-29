package interp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

// Negating the minimum Int overflows; it must be a runtime R0005, not a silently
// wrapped value, in the same way for both the spaced and the parenthesised form.
func TestDoubleNegMinIntOverflows(t *testing.T) {
	for _, expr := range []string{"- -9223372036854775808", "-(-9223372036854775808)"} {
		src := "fn main() {\n    print(" + expr + ")\n}\n"
		_, res := runSrc(t, src)
		if res.Status != "panic" || res.Panic == nil || res.Panic.Code != POverflow {
			t.Fatalf("%s: want %s, got %s", expr, POverflow, res.Describe())
		}
	}
}

// runSrcOpts is runSrc with explicit options.
func runSrcOpts(t *testing.T, src string, opt Options) (string, *Result) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	return runBoth(t, prog, opt)
}

// A very deep recursion must fail with a controlled R0018 panic on every
// engine, instead of overflowing the Go goroutine stack (which is a fatal,
// uncatchable runtime error that aborts the process).
func TestCallDepthLimit(t *testing.T) {
	src := `
fn rec(n: Int) -> Int {
    if n == 0 { return 0 }
    return 1 + rec(n - 1)
}
fn main() {
    print(rec(20000))
}
`
	_, res := runSrcOpts(t, src, Options{})
	if res.Status != "panic" || res.Panic == nil || res.Panic.Code != PDepth {
		t.Fatalf("want a %s panic, got %s", PDepth, res.Describe())
	}
	if res.ExitCode != ExitPanic {
		t.Fatalf("exit code %d, want %d", res.ExitCode, ExitPanic)
	}
}

// A program blocked on a channel must be stopped by --timeout with a controlled
// R0012 panic on every engine, instead of the Go runtime aborting the process
// with "fatal error: all goroutines are asleep - deadlock!".
func TestBlockingChannelTimeout(t *testing.T) {
	opt := Options{Timeout: 250 * time.Millisecond}
	cases := map[string]string{
		"send": `
fn main() {
    let ch = chan(0)
    ch.send(1)
}
`,
		"recv": `
fn main() {
    let ch = chan(0)
    print(ch.recv() ?? 0)
}
`,
		"iterate": `
fn main() {
    let ch = chan(0)
    for v in ch { print(v) }
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, res := runSrcOpts(t, src, opt)
			if res.Status != "panic" || res.Panic == nil || res.Panic.Code != PTimeout {
				t.Fatalf("want a %s panic, got %s", PTimeout, res.Describe())
			}
		})
	}
}

// Task.wait must also unblock on --timeout. Run on a single engine: the task
// goroutine flushes steps asynchronously, so the step totals are not stable
// enough for the strict tree/vm/pgc comparison.
func TestTaskWaitTimeout(t *testing.T) {
	src := `
fn work(ch: Chan[Any]) -> Int {
    ch.recv()
    return 0
}
fn main() -> ! {
    let ch = chan(0)
    let t = spawn work(ch)
    print(try t.wait())
}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	done := make(chan *Result, 1)
	go func() {
		done <- New(prog, Options{Engine: "vm", Timeout: 250 * time.Millisecond}).Run()
	}()
	select {
	case res := <-done:
		if res.Status != "panic" || res.Panic == nil || res.Panic.Code != PTimeout {
			t.Fatalf("want a %s panic, got %s", PTimeout, res.Describe())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task wait was not stopped by the timeout")
	}
}

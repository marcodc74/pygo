package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/check"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/interp"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/parser"
	"github.com/marcodc74/pygo/internal/printer"
)

// Every example must check without errors or warnings and pass its tests;
// deterministic examples must also produce the expected output.
var golden = map[string]string{
	"hello.pg":       "hello from Pygo: [2, 4, 6]\nada   is  36 years old\nalan  is  41 years old\n",
	"errors.pg":      "accepted, total 7.50\nrejected: E_QTY: invalid quantity for B2\nrejected: $.qty: expected Int, got string\n",
	"concurrency.pg": "primes below 20000 per chunk: [669, 560, 525, 508], total 2262\nsum of squares 0..9: 285\n",
}

func TestExamples(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.pg")
	if len(files) < 5 {
		t.Fatalf("expected examples, found %v", files)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			prog, ds := loader.Load(f, nil)
			ds = append(ds, check.Check(prog)...)
			if len(ds) > 0 {
				t.Fatalf("diagnostics: %v", ds)
			}
			for _, engine := range []string{"tree", "vm"} {
				var out bytes.Buffer
				in := interp.New(prog, interp.Options{Stdout: &out, Stderr: &out, Allow: parseAllow("all"), Engine: engine})
				for _, tr := range in.RunTests(nil, "") {
					if !tr.Passed {
						t.Errorf("%s: test %q failed: %s", engine, tr.Name, tr.Failure.Describe())
					}
				}
				want, ok := golden[filepath.Base(f)]
				if !ok {
					continue
				}
				var stdout bytes.Buffer
				in = interp.New(prog, interp.Options{Stdout: &stdout, MaxSteps: 50_000_000, Engine: engine})
				res := in.Run()
				if res.Status != "ok" || stdout.String() != want {
					t.Fatalf("%s: run: %s\n got: %q\nwant: %q", engine, res.Describe(), stdout.String(), want)
				}
				if fb := in.VMFallbacks(); len(fb) > 0 {
					t.Errorf("not compiled to bytecode: %v", fb)
				}
			}
		})
	}
}

func TestFmtIsStable(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*.pg")
	for _, f := range files {
		src, _ := readFile(f)
		f1, ds := parser.ParseFile(f, src)
		if diag.HasErrors(ds) {
			t.Fatal(ds)
		}
		once := printer.File(f1)
		f2, ds := parser.ParseFile(f, once)
		if diag.HasErrors(ds) {
			t.Fatalf("%s: formatted output does not parse: %v", f, ds)
		}
		if twice := printer.File(f2); twice != once {
			t.Fatalf("%s: fmt is not idempotent", f)
		}
	}
}

func TestSnippetWrap(t *testing.T) {
	got := wrapSnippet("import \"json\"\nprint(json.encode(1))", "fs")
	if !strings.HasPrefix(got, "import \"json\"\nfn main() -> ! uses fs {") {
		t.Fatalf("got %q", got)
	}
}

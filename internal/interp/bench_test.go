package interp

import (
	"io"
	"testing"

	"github.com/marcodc74/pygo/internal/loader"
)

// Engine benchmarks on the programs in bench/ (go test -bench . ./internal/interp/).
func benchProgram(b *testing.B, name, engine string) {
	prog, ds := loader.Load("../../bench/"+name+".pg", nil)
	if len(ds) > 0 {
		b.Fatal(ds)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := New(prog, Options{Stdout: io.Discard, Engine: engine}).Run()
		if res.Status != "ok" {
			b.Fatal(res.Describe())
		}
	}
}

func BenchmarkFibTree(b *testing.B)   { benchProgram(b, "fib", "tree") }
func BenchmarkFibVM(b *testing.B)     { benchProgram(b, "fib", "vm") }
func BenchmarkLoopsTree(b *testing.B) { benchProgram(b, "loops", "tree") }
func BenchmarkLoopsVM(b *testing.B)   { benchProgram(b, "loops", "vm") }
func BenchmarkSortTree(b *testing.B)  { benchProgram(b, "sort", "tree") }
func BenchmarkSortVM(b *testing.B)    { benchProgram(b, "sort", "vm") }
func BenchmarkJSONTree(b *testing.B)  { benchProgram(b, "json", "tree") }
func BenchmarkJSONVM(b *testing.B)    { benchProgram(b, "json", "vm") }

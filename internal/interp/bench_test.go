package interp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// bigProgram generates a program with n functions (for load benchmarks).
func bigProgram(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `fn f%d(n: Int) -> Int {
    var t = 0
    for k in 0..n {
        if k %% 3 == 0 {
            t += k * %d
        } else {
            t -= 1
        }
    }
    let s = "v${t}"
    return t + s.len()
}

`, i, i%7+1)
	}
	b.WriteString("fn main() {\n    print(f1(10))\n}\n")
	return b.String()
}

func loadBig(b *testing.B) *loader.Program {
	dir := b.TempDir()
	p := filepath.Join(dir, "big.pg")
	os.WriteFile(p, []byte(bigProgram(2000)), 0o644)
	prog, ds := loader.Load(p, nil)
	if len(ds) > 0 {
		b.Fatal(ds)
	}
	return prog
}

// Loading 2000 functions: compiling from the syntax tree vs decoding .pgc.
func BenchmarkLoadCompile(b *testing.B) {
	prog := loadBig(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CompileProgram(prog)
	}
}

func BenchmarkLoadPgc(b *testing.B) {
	prog := loadBig(b)
	c, _ := CompileProgram(prog)
	data, _ := EncodePgc(prog, c, "bench")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := DecodePgc(data); err != nil {
			b.Fatal(err)
		}
	}
}

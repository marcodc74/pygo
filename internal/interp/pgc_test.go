package interp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

func loadSrc(t *testing.T, src string) *loader.Program {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatal(ds)
	}
	return prog
}

const pgcSample = `
struct P { x: Int, y: Int = 2 }
enum E {
    A(n: Int)
    B
}
impl P {
    fn sum(self) -> Int => self.x + self.y
}
fn classify(e: E) -> Str => match e {
    E.A(n) if n > 1 => "big ${n}"
    E.A(n) => "a ${n}"
    B => "b"
}
fn main() {
    let adder = fn(k: Int) => fn(v: Int) => v + k
    print(P{x: 1}.sum(), classify(E.A(n: 5)), classify(E.B), adder(2)(3))
}
test "sum" {
    assert P{x: 3}.sum() == 5
}
`

func TestPgcHeaderAndInfo(t *testing.T) {
	prog := loadSrc(t, pgcSample)
	c, fb := CompileProgram(prog)
	if len(fb) > 0 {
		t.Fatalf("fallbacks: %v", fb)
	}
	data, err := EncodePgc(prog, c, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !IsPgc(data) {
		t.Fatal("missing magic")
	}
	_, _, info, err := DecodePgc(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != pgcVersion || info.Pygo != "9.9.9" || info.SourceHash != SourceHash(prog) {
		t.Fatalf("info %+v", info)
	}
}

func TestPgcTestsRunFromBytecode(t *testing.T) {
	prog := loadSrc(t, pgcSample)
	pprog, compiled := pgcRoundTrip(t, prog)
	var out bytes.Buffer
	in := New(pprog, Options{Stdout: &out, Compiled: compiled})
	trs := in.RunTests(nil, "")
	if len(trs) != 1 || !trs[0].Passed {
		b, _ := json.Marshal(trs)
		t.Fatalf("tests: %s", b)
	}
	if fb := in.VMFallbacks(); len(fb) > 0 {
		t.Fatalf("fallbacks: %v", fb)
	}
}

// Corrupted files must be rejected with an error, never crash the decoder:
// with the checksum (flipped bytes, truncation), and without it (garbage
// that passes the checksum, to exercise the decoder's own checks).
func TestPgcCorruption(t *testing.T) {
	prog := loadSrc(t, pgcSample)
	c, _ := CompileProgram(prog)
	data, _ := EncodePgc(prog, c, "test")
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 300; i++ {
		bad := append([]byte(nil), data...)
		bad[4+rng.Intn(len(bad)-4)] ^= byte(1 + rng.Intn(255))
		if _, _, _, err := DecodePgc(bad); err == nil {
			t.Fatalf("flipped byte accepted")
		}
	}
	for n := 0; n < len(data); n += 7 {
		if _, _, _, err := DecodePgc(data[:n]); err == nil {
			t.Fatalf("truncated file (%d bytes) accepted", n)
		}
	}
	body := data[:len(data)-sha256.Size]
	for i := 0; i < 2000; i++ {
		bad := append([]byte(nil), body...)
		for k := 0; k < 1+rng.Intn(4); k++ {
			bad[8+rng.Intn(len(bad)-8)] = byte(rng.Intn(256))
		}
		if rng.Intn(3) == 0 {
			bad = bad[:8+rng.Intn(len(bad)-8)]
		}
		sum := sha256.Sum256(bad)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decoder panicked: %v", r)
				}
			}()
			DecodePgc(append(bad, sum[:]...))
		}()
	}
	if _, _, _, err := DecodePgc([]byte("hello")); err == nil || !strings.Contains(err.Error(), "PYGC") {
		t.Fatalf("got %v", err)
	}
}

// The same compiled bytecode can run in several interpreters: each keeps
// its own cache of module-level names. Regression: tests ran in one
// interpreter cached the enum type E inside make_e's bytecode, and main in
// a second interpreter then got a value of the first run's E back from
// make_e ("make_e returned E, expected E").
func TestCompiledSharedByInterps(t *testing.T) {
	prog := loadSrc(t, `
enum E {
    A(n: Int)
    B
}
fn make_e(n: Int) -> E => E.A(n: n)
fn main() {
    print(make_e(4))
}
test "make" {
    assert make_e(1) != E.B
}
`)
	pprog, compiled := pgcRoundTrip(t, prog)
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		in := New(pprog, Options{Stdout: &out, Compiled: compiled})
		if trs := in.RunTests(nil, ""); len(trs) != 1 || !trs[0].Passed {
			t.Fatalf("round %d: test failed: %+v", i, trs)
		}
		res := New(pprog, Options{Stdout: &out, Compiled: compiled}).Run()
		if res.Status != "ok" || out.String() != "E.A(n: 4)\n" {
			t.Fatalf("round %d: %s %q", i, res.Describe(), out.String())
		}
	}
}

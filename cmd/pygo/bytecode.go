package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/interp"
	"github.com/marcodc74/pygo/internal/loader"
)

// isPgcPath reports whether a file is compiled bytecode (by extension).
func isPgcPath(file string) bool { return strings.HasSuffix(file, ".pgc") }

// loadPgc reads a .pgc file; ok=false after printing the error.
func loadPgc(file string, asJSON bool) (*loader.Program, *interp.Compiled, bool) {
	data, err := os.ReadFile(file)
	if err == nil {
		var prog *loader.Program
		var c *interp.Compiled
		if prog, c, _, err = interp.DecodePgc(data); err == nil {
			return prog, c, true
		}
	}
	d := diag.Diagnostic{Code: "E0910", Severity: "error", Message: err.Error(), File: file,
		Hint: "compile the program again: pygo compile file.pg"}
	if asJSON {
		fmt.Fprintln(os.Stderr, diag.JSON([]diag.Diagnostic{d}))
	} else {
		fmt.Fprintln(os.Stderr, d.String())
	}
	return nil, nil, false
}

// loadRunnable loads a source file (checked) or a .pgc file.
func loadRunnable(file string, read loader.ReadFunc, asJSON bool) (*loader.Program, *interp.Compiled, []diag.Diagnostic, bool) {
	if isPgcPath(file) && read == nil {
		prog, c, ok := loadPgc(file, asJSON)
		return prog, c, nil, ok
	}
	prog, ds, ok := loadWith(file, read, asJSON, false)
	return prog, nil, ds, ok
}

// compileToPgc checks and compiles a source program into .pgc bytes.
func compileToPgc(file string, asJSON bool) ([]byte, []string, bool) {
	prog, ds, ok := load(file, asJSON, true)
	if !ok {
		if asJSON {
			fmt.Fprintln(os.Stderr, diag.JSON(ds))
		}
		return nil, nil, false
	}
	c, fallbacks := interp.CompileProgram(prog)
	data, err := interp.EncodePgc(prog, c, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pygo compile:", err)
		return nil, nil, false
	}
	return data, fallbacks, true
}

func cmdCompile(args []string) int {
	fs := flag.NewFlagSet("compile", flag.ExitOnError)
	out := fs.String("o", "", "output file (default: the source name with .pgc)")
	asJSON := fs.Bool("json", false, "report as JSON")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fail2("usage: pygo compile [-o app.pgc] [--json] file.pg")
	}
	file := fs.Arg(0)
	data, fallbacks, ok := compileToPgc(file, *asJSON)
	if !ok {
		return interp.ExitCompile
	}
	dst := *out
	if dst == "" {
		dst = strings.TrimSuffix(file, filepath.Ext(file)) + ".pgc"
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fail2("%v", err)
	}
	if *asJSON {
		printJSON(map[string]any{"ok": true, "output": dst, "bytes": len(data), "interpreted": nonNilStrings(fallbacks)})
	} else {
		fmt.Fprintf(os.Stderr, "compiled %s -> %s (%d bytes)\n", file, dst, len(data))
		for _, f := range fallbacks {
			fmt.Fprintf(os.Stderr, "note: %s runs on the interpreter\n", f)
		}
	}
	return 0
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func cmdDisasm(args []string) int {
	fs := flag.NewFlagSet("disasm", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the bytecode as JSON")
	fn := fs.String("fn", "", "only this function (name, Type.method or test \"name\")")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fail2("usage: pygo disasm [--json] [--fn name] file.pg|file.pgc")
	}
	file := fs.Arg(0)
	var prog *loader.Program
	var c *interp.Compiled
	if isPgcPath(file) {
		var ok bool
		if prog, c, ok = loadPgc(file, *asJSON); !ok {
			return interp.ExitCompile
		}
	} else {
		var ds []diag.Diagnostic
		var ok bool
		if prog, ds, ok = load(file, *asJSON, false); !ok {
			if *asJSON {
				fmt.Fprintln(os.Stderr, diag.JSON(ds))
			}
			return interp.ExitCompile
		}
		c, _ = interp.CompileProgram(prog)
	}
	funcs := interp.Disassemble(prog, c)
	if *fn != "" {
		var names []string
		if funcs, names = interp.FilterDisasm(funcs, *fn); funcs == nil {
			return fail2("no function %q; functions: %s", *fn, strings.Join(names, ", "))
		}
	}
	if *asJSON {
		printJSON(map[string]any{"file": file, "functions": funcs})
	} else {
		fmt.Print(interp.DisasmText(funcs))
	}
	return 0
}

// Package loader reads a program: the main file and, transitively, the
// local modules it imports ("./x", "../lib/y"). Standard modules ("json",
// "http", ...) are provided by the runtime and are not files.
package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/parser"
	"github.com/marcodc74/pygo/internal/sig"
)

// Program is a parsed main file plus its local imports.
type Program struct {
	Main    string
	Files   map[string]*ast.File
	Sources map[string]string
	// Imports[file][importPath] = resolved file key (local imports only).
	Imports map[string]map[string]string
	Order   []string // files in dependency order (dependencies first)
}

// ReadFunc reads a source file (os.ReadFile, or a bundle).
type ReadFunc func(path string) (string, error)

// IsLocal reports whether an import path refers to a local file.
func IsLocal(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/")
}

// Load reads and parses main and its local imports.
func Load(main string, read ReadFunc) (*Program, []diag.Diagnostic) {
	if read == nil {
		read = func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return string(b), err
		}
	}
	prog := &Program{
		Main:    filepath.ToSlash(filepath.Clean(main)),
		Files:   map[string]*ast.File{},
		Sources: map[string]string{},
		Imports: map[string]map[string]string{},
	}
	var diags []diag.Diagnostic
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var visit func(file string, from string, pos diag.Pos) bool
	visit = func(file, from string, pos diag.Pos) bool {
		switch state[file] {
		case 1:
			diags = append(diags, diag.Diagnostic{Code: "E0802", Severity: diag.Error, File: from, Line: pos.Line, Col: pos.Col,
				Message: fmt.Sprintf("import cycle through %s", file), Hint: "move shared declarations into a third module"})
			return false
		case 2:
			return true
		}
		state[file] = 1
		src, err := read(file)
		if err != nil {
			d := diag.Diagnostic{Code: "E0801", Severity: diag.Error, File: from, Line: pos.Line, Col: pos.Col,
				Message: fmt.Sprintf("cannot read module %s: %v", file, err)}
			if from == "" {
				d.File = file
			}
			diags = append(diags, d)
			state[file] = 2
			return false
		}
		f, ds := parser.ParseFile(file, src)
		diags = append(diags, ds...)
		prog.Files[file] = f
		prog.Sources[file] = src
		prog.Imports[file] = map[string]string{}
		for _, d := range f.Decls {
			imp, ok := d.(*ast.ImportDecl)
			if !ok {
				continue
			}
			if !IsLocal(imp.Path) {
				if sig.Get(imp.Path) == nil || imp.Path == "core" {
					hint := "standard modules: " + strings.Join(sig.Names(), ", ") + `; local modules start with "./"`
					if s := diag.Suggest(imp.Path, sig.Names()); s != "" {
						hint = fmt.Sprintf("did you mean %q?", s)
					}
					diags = append(diags, diag.Diagnostic{Code: "E0803", Severity: diag.Error, File: file, Line: imp.Pos.Line, Col: imp.Pos.Col,
						Message: fmt.Sprintf("unknown module %q", imp.Path), Hint: hint})
				}
				continue
			}
			target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(file), imp.Path)))
			if !strings.HasSuffix(target, ".pg") {
				target += ".pg"
			}
			prog.Imports[file][imp.Path] = target
			visit(target, file, imp.Pos)
		}
		state[file] = 2
		prog.Order = append(prog.Order, file)
		return true
	}
	visit(prog.Main, "", diag.Pos{})
	return prog, diags
}

// ---------- bundles (pygo build) ----------

// Bundle is the serialized form of a program appended to an executable.
type Bundle struct {
	Main  string            `json:"main"`
	Files map[string]string `json:"files"`
	Allow []string          `json:"allow,omitempty"`
}

// MakeBundle serializes the sources of prog.
func MakeBundle(prog *Program, allow []string) ([]byte, error) {
	b := Bundle{Main: prog.Main, Files: prog.Sources, Allow: allow}
	sort.Strings(b.Allow)
	return json.Marshal(b)
}

// FromBundle loads a program from a bundle.
func FromBundle(b *Bundle) (*Program, []diag.Diagnostic) {
	return Load(b.Main, func(p string) (string, error) {
		src, ok := b.Files[p]
		if !ok {
			return "", fmt.Errorf("not in bundle")
		}
		return src, nil
	})
}

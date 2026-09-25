// Package sig holds the signatures of the standard library, written in
// Pygo syntax (internal/sig/std/*.pg). The same headers drive the checker,
// the runtime argument binding, `pygo describe` and `pygo guide`.
package sig

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/parser"
)

//go:embed std/*.pg
var stdFS embed.FS

// Module is a parsed stdlib header.
type Module struct {
	Name    string
	Source  string
	Funcs   map[string]*ast.FuncDecl
	Structs map[string]*ast.StructDecl
	Consts  map[string]*ast.ConstDecl
	// Methods of builtin types (core only): "Str" -> "upper" -> decl.
	Methods map[string]map[string]*ast.FuncDecl
	Order   []string
}

var (
	once    sync.Once
	modules map[string]*Module
)

// Modules returns all stdlib modules; "core" holds the global builtins.
func Modules() map[string]*Module {
	once.Do(load)
	return modules
}

// Get returns the named stdlib module or nil.
func Get(name string) *Module { return Modules()[name] }

// Names returns the importable stdlib module names (without "core").
func Names() []string {
	var out []string
	for n := range Modules() {
		if n != "core" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func load() {
	modules = map[string]*Module{}
	entries, err := stdFS.ReadDir("std")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".pg")
		src, _ := stdFS.ReadFile("std/" + e.Name())
		f, ds := parser.ParseFile("std/"+e.Name(), string(src))
		if diag.HasErrors(ds) {
			panic(fmt.Sprintf("stdlib header %s: %v", e.Name(), ds))
		}
		m := &Module{
			Name: name, Source: string(src),
			Funcs:   map[string]*ast.FuncDecl{},
			Structs: map[string]*ast.StructDecl{},
			Consts:  map[string]*ast.ConstDecl{},
			Methods: map[string]map[string]*ast.FuncDecl{},
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				m.Funcs[d.Name] = d
				m.Order = append(m.Order, d.Name)
			case *ast.StructDecl:
				m.Structs[d.Name] = d
				m.Order = append(m.Order, d.Name)
			case *ast.ConstDecl:
				m.Consts[d.Let.Name] = d
				m.Order = append(m.Order, d.Let.Name)
			case *ast.ImplDecl:
				ms := map[string]*ast.FuncDecl{}
				for _, fd := range d.Methods {
					ms[fd.Name] = fd
				}
				m.Methods[d.Type] = ms
			}
		}
		modules[name] = m
	}
}

// Effects returns the effects declared by a stdlib function.
func Effects(fd *ast.FuncDecl) []string { return fd.Uses }

// AllEffects is the closed set of capability names.
var AllEffects = []string{"clock", "env", "fs", "net", "proc", "python", "rand"}

// IsEffect reports whether name is a known capability.
func IsEffect(name string) bool {
	for _, e := range AllEffects {
		if e == name {
			return true
		}
	}
	return false
}

package main

import (
	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/loader"
)

// mainUses returns the capabilities declared by main (its "uses" clause).
func mainUses(prog *loader.Program) []string {
	for _, d := range prog.Files[prog.Main].Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name == "main" {
			return fd.Uses
		}
	}
	return nil
}

// Package guide renders the compact language reference printed by
// `pygo guide`, followed by the stdlib signatures.
package guide

import (
	_ "embed"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

//go:embed guide.md
var reference string

// Text returns the full guide (reference + stdlib API).
func Text() string {
	var b strings.Builder
	b.WriteString(reference)
	b.WriteString("\n## Standard library (signatures)\n")
	b.WriteString(Stdlib())
	return b.String()
}

// Stdlib lists every stdlib signature compactly.
func Stdlib() string {
	var b strings.Builder
	core := sig.Get("core")
	b.WriteString("\n### builtins\n```\n")
	for _, n := range sortedFuncs(core) {
		b.WriteString(printer.Signature(core.Funcs[n]) + "\n")
	}
	b.WriteString("struct Error { message: Str, code: Str, data: Any }\n```\n")
	for _, tn := range []string{"Str", "List", "Map", "Range", "Chan", "Task"} {
		ms := core.Methods[tn]
		var names []string
		for n := range ms {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("\n### " + tn + " methods")
		switch tn {
		case "List", "Chan", "Task":
			b.WriteString(" (T = element type)")
		case "Map":
			b.WriteString(" (K = key, V = value)")
		}
		b.WriteString("\n```\n")
		for _, n := range names {
			b.WriteString(strings.Replace(printer.Signature(ms[n]), "(self, ", "(", 1))
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}
	for _, mn := range sig.Names() {
		m := sig.Get(mn)
		b.WriteString("\n### import \"" + mn + "\"\n```\n")
		for _, n := range m.Order {
			switch {
			case m.Structs[n] != nil:
				b.WriteString(structLine(mn, m, n) + "\n")
			case m.Consts[n] != nil:
				b.WriteString("let " + mn + "." + n + " = " + printer.Expr(m.Consts[n].Let.Value) + "\n")
			case m.Funcs[n] != nil:
				b.WriteString(strings.Replace(printer.Signature(m.Funcs[n]), "fn ", "fn "+mn+".", 1) + "\n")
			}
		}
		b.WriteString("```\n")
	}
	return strings.ReplaceAll(b.String(), "(self)", "()")
}

func structLine(mod string, m *sig.Module, name string) string {
	sd := m.Structs[name]
	var fs []string
	for _, f := range sd.Fields {
		s := f.Name + ": " + printer.Type(f.Type)
		if f.Default != nil {
			s += " = " + printer.Expr(f.Default)
		}
		fs = append(fs, s)
	}
	return "struct " + mod + "." + name + " { " + strings.Join(fs, ", ") + " }"
}

func sortedFuncs(m *sig.Module) []string {
	var out []string
	for n := range m.Funcs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/check"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/guide"
	"github.com/marcodc74/pygo/internal/interp"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/parser"
	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func fail2(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return 2
}

// ---------- fmt ----------

func cmdFmt(args []string) int {
	fs := flag.NewFlagSet("fmt", flag.ExitOnError)
	write := fs.Bool("w", false, "write the result back to the file")
	checkOnly := fs.Bool("check", false, "exit 1 if the file is not canonically formatted")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fail2("usage: pygo fmt [-w] [--check] file.pg")
	}
	path := fs.Arg(0)
	src, err := readFile(path)
	if err != nil {
		return fail2("%v", err)
	}
	f, ds := parser.ParseFile(path, src)
	if diag.HasErrors(ds) {
		for _, d := range ds {
			fmt.Fprintln(os.Stderr, d.String())
		}
		return interp.ExitCompile
	}
	out := printer.File(f)
	switch {
	case *checkOnly:
		if out != src {
			fmt.Println(path)
			return 1
		}
	case *write:
		if out != src {
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				return fail2("%v", err)
			}
		}
	default:
		fmt.Print(out)
	}
	return 0
}

// ---------- fix ----------

func cmdFix(args []string) int {
	fs := flag.NewFlagSet("fix", flag.ExitOnError)
	all := fs.Bool("all", false, "also apply unsafe fixes (did-you-mean guesses, inserting try)")
	dry := fs.Bool("dry-run", false, "print the fixed source instead of writing it")
	asJSON := fs.Bool("json", false, "print a JSON summary")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fail2("usage: pygo fix [--all] [--dry-run] [--json] file.pg")
	}
	path := fs.Arg(0)
	src, err := readFile(path)
	if err != nil {
		return fail2("%v", err)
	}
	orig := src
	total := 0
	var ds []diag.Diagnostic
	for round := 0; round < 5; round++ {
		prog, pds := loader.Load(path, func(p string) (string, error) {
			if p == loader.Key(path) {
				return src, nil
			}
			return readFile(p)
		})
		ds = pds
		if !diag.HasErrors(pds) {
			ds = append(ds, check.Check(prog)...)
		}
		var fixes []diag.Fix
		for _, d := range ds {
			if d.Fix != nil && d.File == prog.Main && (d.Fix.Safe || *all) {
				fixes = append(fixes, *d.Fix)
			}
		}
		if len(fixes) == 0 {
			break
		}
		var n int
		src, n = diag.ApplyFixes(src, fixes)
		total += n
		if n == 0 {
			break
		}
	}
	ds = diag.Sort(ds)
	if *dry {
		fmt.Print(src)
	} else if src != orig {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			return fail2("%v", err)
		}
	}
	if *asJSON {
		printJSON(map[string]any{"applied": total, "ok": !diag.HasErrors(ds), "diagnostics": nonNil(ds)})
	} else if !*dry {
		fmt.Fprintf(os.Stderr, "%d fix(es) applied\n", total)
		for _, d := range ds {
			fmt.Fprintln(os.Stderr, d.String())
		}
	}
	if diag.HasErrors(ds) {
		return interp.ExitCompile
	}
	return 0
}

func nonNil(ds []diag.Diagnostic) []diag.Diagnostic {
	if ds == nil {
		return []diag.Diagnostic{}
	}
	return ds
}

// ---------- explain / guide ----------

func cmdExplain(args []string) int {
	asJSON := len(args) > 0 && args[0] == "--json"
	if asJSON {
		args = args[1:]
	}
	if len(args) == 0 {
		var codes []string
		for c := range diag.Explanations {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		if asJSON {
			var all []diag.Explanation
			for _, c := range codes {
				e, _ := diag.Explain(c)
				all = append(all, e)
			}
			printJSON(all)
			return 0
		}
		for _, c := range codes {
			fmt.Printf("%s  %s\n", c, diag.Explanations[c].Title)
		}
		return 0
	}
	code := strings.ToUpper(args[0])
	e, ok := diag.Explain(code)
	if !ok {
		return fail2("unknown code %s (run `pygo explain` for the list)", code)
	}
	if asJSON {
		printJSON(e)
		return 0
	}
	fmt.Printf("%s: %s\n\n%s\n", e.Code, e.Title, e.Detail)
	if e.Wrong != "" {
		fmt.Printf("\nwrong:\n    %s\n", strings.ReplaceAll(e.Wrong, "\n", "\n    "))
	}
	if e.Right != "" {
		fmt.Printf("\nright:\n    %s\n", strings.ReplaceAll(e.Right, "\n", "\n    "))
	}
	return 0
}

func cmdGuide(args []string) int {
	if len(args) > 0 && args[0] == "--stdlib" {
		fmt.Print(guide.Stdlib())
		return 0
	}
	fmt.Print(guide.Text())
	return 0
}

// ---------- describe ----------

type paramDesc struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Default  string `json:"default,omitempty"`
	Variadic bool   `json:"variadic,omitempty"`
}

type funcDesc struct {
	Name      string      `json:"name"`
	Signature string      `json:"signature"`
	Doc       string      `json:"doc,omitempty"`
	Params    []paramDesc `json:"params"`
	Returns   string      `json:"returns,omitempty"`
	Fallible  bool        `json:"fallible"`
	Uses      []string    `json:"uses,omitempty"`
	Requires  []string    `json:"requires,omitempty"`
	Ensures   []string    `json:"ensures,omitempty"`
	Line      int         `json:"line,omitempty"`
}

type typeDesc struct {
	Kind     string                 `json:"kind"`
	Name     string                 `json:"name"`
	Doc      string                 `json:"doc,omitempty"`
	Fields   []paramDesc            `json:"fields,omitempty"`
	Variants map[string][]paramDesc `json:"variants,omitempty"`
	Methods  []funcDesc             `json:"methods,omitempty"`
	Line     int                    `json:"line,omitempty"`
}

func describeFunc(fd *ast.FuncDecl) funcDesc {
	d := funcDesc{Name: fd.Name, Signature: printer.Signature(fd), Doc: fd.Doc, Fallible: fd.Fallible, Uses: fd.Uses, Line: fd.Pos.Line, Params: []paramDesc{}}
	for _, p := range fd.Params {
		pd := paramDesc{Name: p.Name, Type: printer.Type(p.Type), Variadic: p.Variadic}
		if p.Default != nil {
			pd.Default = printer.Expr(p.Default)
		}
		d.Params = append(d.Params, pd)
	}
	if fd.Ret != nil {
		d.Returns = printer.Type(fd.Ret)
	}
	for _, r := range fd.Requires {
		d.Requires = append(d.Requires, printer.Expr(r))
	}
	for _, e := range fd.Ensures {
		d.Ensures = append(d.Ensures, printer.Expr(e))
	}
	return d
}

func fieldsDesc(fs []*ast.Field) []paramDesc {
	out := []paramDesc{}
	for _, f := range fs {
		pd := paramDesc{Name: f.Name, Type: printer.Type(f.Type)}
		if f.Default != nil {
			pd.Default = printer.Expr(f.Default)
		}
		out = append(out, pd)
	}
	return out
}

func describeDecls(decls []ast.Decl) map[string]any {
	funcs := []funcDesc{}
	types := []typeDesc{}
	var imports, tests, consts []string
	byName := map[string]int{}
	for _, d := range decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			funcs = append(funcs, describeFunc(d))
		case *ast.StructDecl:
			byName[d.Name] = len(types)
			types = append(types, typeDesc{Kind: "struct", Name: d.Name, Doc: d.Doc, Fields: fieldsDesc(d.Fields), Line: d.Pos.Line})
		case *ast.EnumDecl:
			vs := map[string][]paramDesc{}
			for _, v := range d.Variants {
				vs[v.Name] = fieldsDesc(v.Fields)
			}
			byName[d.Name] = len(types)
			types = append(types, typeDesc{Kind: "enum", Name: d.Name, Doc: d.Doc, Variants: vs, Line: d.Pos.Line})
		case *ast.ImportDecl:
			imports = append(imports, d.Path)
		case *ast.TestDecl:
			tests = append(tests, d.Name)
		case *ast.ConstDecl:
			consts = append(consts, d.Let.Name)
		}
	}
	for _, d := range decls {
		if im, ok := d.(*ast.ImplDecl); ok {
			if i, ok := byName[im.Type]; ok {
				for _, m := range im.Methods {
					types[i].Methods = append(types[i].Methods, describeFunc(m))
				}
			}
		}
	}
	return map[string]any{"imports": imports, "functions": funcs, "types": types, "constants": consts, "tests": tests}
}

func cmdDescribe(args []string) int {
	if len(args) != 1 {
		return fail2("usage: pygo describe file.pg | <stdlib module> | core")
	}
	target := args[0]
	if m := sig.Get(target); m != nil && !strings.HasSuffix(target, ".pg") {
		var decls []ast.Decl
		for _, n := range m.Order {
			switch {
			case m.Funcs[n] != nil:
				decls = append(decls, m.Funcs[n])
			case m.Structs[n] != nil:
				decls = append(decls, m.Structs[n])
			case m.Consts[n] != nil:
				decls = append(decls, m.Consts[n])
			}
		}
		out := describeDecls(decls)
		out["module"] = target
		if target == "core" {
			methods := map[string][]funcDesc{}
			for tn, ms := range m.Methods {
				var names []string
				for n := range ms {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					methods[tn] = append(methods[tn], describeFunc(ms[n]))
				}
			}
			out["methods"] = methods
		}
		printJSON(out)
		return 0
	}
	src, err := readFile(target)
	if err != nil {
		return fail2("%v (not a file nor a stdlib module: %s)", err, strings.Join(append([]string{"core"}, sig.Names()...), ", "))
	}
	f, ds := parser.ParseFile(target, src)
	out := describeDecls(f.Decls)
	out["module"] = target
	out["diagnostics"] = nonNil(ds)
	printJSON(out)
	return 0
}

// ---------- outline / edit ----------

type symbol struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Hash    string `json:"hash"`
	Summary string `json:"summary"`
}

func lineOf(runes []rune, off int) int {
	line := 1
	for i := 0; i < off && i < len(runes); i++ {
		if runes[i] == '\n' {
			line++
		}
	}
	return line
}

func outline(src string, f *ast.File) []symbol {
	runes := []rune(src)
	out := []symbol{}
	for _, d := range f.Decls {
		s, e := d.Span()
		if e > len(runes) {
			e = len(runes)
		}
		text := string(runes[s:e])
		h := sha256.Sum256([]byte(text))
		key := ast.DeclKey(d)
		sum := ""
		switch d := d.(type) {
		case *ast.FuncDecl:
			sum = printer.Signature(d)
		case *ast.StructDecl:
			sum = fmt.Sprintf("struct %s (%d fields)", d.Name, len(d.Fields))
		case *ast.EnumDecl:
			var vs []string
			for _, v := range d.Variants {
				vs = append(vs, v.Name)
			}
			sum = fmt.Sprintf("enum %s { %s }", d.Name, strings.Join(vs, ", "))
		case *ast.ImplDecl:
			var ms []string
			for _, m := range d.Methods {
				ms = append(ms, m.Name)
			}
			sum = fmt.Sprintf("impl %s { %s }", d.Type, strings.Join(ms, ", "))
		case *ast.ImportDecl:
			sum = "import " + d.Path
		case *ast.TestDecl:
			sum = "test " + d.Name
		case *ast.ConstDecl:
			sum = "let " + d.Let.Name
		}
		out = append(out, symbol{Key: key, Kind: strings.SplitN(key, ":", 2)[0], Line: lineOf(runes, s), EndLine: lineOf(runes, e), Hash: hex.EncodeToString(h[:6]), Summary: sum})
	}
	return out
}

func cmdOutline(args []string) int {
	if len(args) != 1 {
		return fail2("usage: pygo outline file.pg")
	}
	src, err := readFile(args[0])
	if err != nil {
		return fail2("%v", err)
	}
	f, ds := parser.ParseFile(args[0], src)
	printJSON(map[string]any{"file": args[0], "symbols": outline(src, f), "diagnostics": nonNil(ds)})
	return 0
}

// cmdEdit replaces, inserts or deletes whole declarations by symbol key.
func cmdEdit(args []string) int {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	replace := fs.String("replace", "", "symbol key to replace, e.g. fn:main, struct:User, impl:User, test:name")
	insertAfter := fs.String("insert-after", "", "insert the new declaration(s) after this symbol")
	appendNew := fs.Bool("append", false, "append the new declaration(s) at the end of the file")
	del := fs.String("delete", "", "symbol key to delete")
	expect := fs.String("expect-hash", "", "refuse the edit if the target symbol's hash differs (optimistic locking)")
	code := fs.String("code", "", "new code (default: read from stdin)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fail2("usage: pygo edit file.pg (--replace KEY | --insert-after KEY | --append | --delete KEY) [--expect-hash H] [--code TEXT | < code]")
	}
	path := fs.Arg(0)
	src, err := readFile(path)
	if err != nil {
		return fail2("%v", err)
	}
	f, ds := parser.ParseFile(path, src)
	if diag.HasErrors(ds) {
		printJSON(map[string]any{"ok": false, "error": "the file has syntax errors; fix them first", "diagnostics": ds})
		return interp.ExitCompile
	}
	syms := outline(src, f)
	find := func(key string) (int, bool) {
		for i, s := range syms {
			if s.Key == key {
				return i, true
			}
		}
		return -1, false
	}
	target := *replace + *insertAfter + *del
	idx := -1
	if target != "" {
		var ok bool
		idx, ok = find(target)
		if !ok {
			var keys []string
			for _, s := range syms {
				keys = append(keys, s.Key)
			}
			printJSON(map[string]any{"ok": false, "error": "symbol not found: " + target, "hint": diagHint(target, keys), "symbols": keys})
			return 2
		}
		if *expect != "" && syms[idx].Hash != *expect {
			printJSON(map[string]any{"ok": false, "error": "hash mismatch: the symbol changed since it was read", "current_hash": syms[idx].Hash})
			return 2
		}
	}
	newCode := *code
	if *del == "" && newCode == "" {
		b, _ := io.ReadAll(os.Stdin)
		newCode = string(b)
	}
	if *del == "" {
		nf, nds := parser.ParseFile("<edit>", newCode)
		if diag.HasErrors(nds) || len(nf.Decls) == 0 {
			printJSON(map[string]any{"ok": false, "error": "the new code does not parse as declarations", "diagnostics": nonNil(nds)})
			return interp.ExitCompile
		}
		newCode = strings.TrimSpace(newCode)
	}
	runes := []rune(src)
	var out string
	switch {
	case *replace != "":
		s, e := f.Decls[idx].Span()
		out = string(runes[:s]) + newCode + string(runes[e:])
	case *del != "":
		s, e := f.Decls[idx].Span()
		rest := strings.TrimLeft(string(runes[e:]), "\n")
		head := strings.TrimRight(string(runes[:s]), "\n")
		if head != "" && rest != "" {
			head += "\n\n"
		} else if head != "" {
			head += "\n"
		}
		out = head + rest
	case *insertAfter != "":
		_, e := f.Decls[idx].Span()
		out = string(runes[:e]) + "\n\n" + newCode + string(runes[e:])
	case *appendNew:
		out = strings.TrimRight(src, "\n") + "\n\n" + newCode + "\n"
	default:
		return fail2("choose one of --replace, --insert-after, --append, --delete")
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return fail2("%v", err)
	}
	prog, lds := loader.Load(path, nil)
	if !diag.HasErrors(lds) {
		lds = append(lds, check.Check(prog)...)
	}
	lds = diag.Sort(lds)
	nf, _ := parser.ParseFile(path, out)
	printJSON(map[string]any{"ok": !diag.HasErrors(lds), "diagnostics": nonNil(lds), "symbols": outline(out, nf)})
	if diag.HasErrors(lds) {
		return interp.ExitCompile
	}
	return 0
}

func diagHint(name string, cands []string) string {
	if s := diag.Suggest(name, cands); s != "" {
		return "did you mean '" + s + "'?"
	}
	return ""
}

// ---------- ast ----------

func cmdAST(args []string) int {
	if len(args) != 1 {
		return fail2("usage: pygo ast file.pg")
	}
	src, err := readFile(args[0])
	if err != nil {
		return fail2("%v", err)
	}
	f, ds := parser.ParseFile(args[0], src)
	printJSON(map[string]any{"file": args[0], "decls": ast.ToJSON(f.Decls), "diagnostics": nonNil(ds)})
	return 0
}

// ---------- build (self-contained executables) ----------

const bundleMagic = "PYGOBNDL"

func cmdBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("o", "", "output executable")
	allowS := fs.String("allow", "", "capabilities granted to the built program")
	runtimePath := fs.String("runtime", "", "pygo binary to embed (default: this one); use a dist/ binary to cross-build")
	fs.Parse(args)
	if fs.NArg() != 1 || *out == "" {
		return fail2("usage: pygo build [--allow caps] [--runtime pygo-binary] -o app file.pg")
	}
	prog, ds, ok := load(fs.Arg(0), false, false)
	if !ok {
		_ = ds
		return interp.ExitCompile
	}
	var allow []string
	for c := range parseAllow(*allowS) {
		allow = append(allow, c)
	}
	payload, err := loader.MakeBundle(prog, allow)
	if err != nil {
		return fail2("%v", err)
	}
	rt := *runtimePath
	if rt == "" {
		if rt, err = os.Executable(); err != nil {
			return fail2("%v", err)
		}
	}
	base, err := os.ReadFile(rt)
	if err != nil {
		return fail2("%v", err)
	}
	if i := bytes.LastIndex(base, []byte(bundleMagic)); i >= 0 && i == len(base)-len(bundleMagic) {
		n := binary.LittleEndian.Uint64(base[i-8 : i])
		base = base[:i-8-int(n)]
	}
	var buf bytes.Buffer
	buf.Write(base)
	buf.Write(payload)
	var lenb [8]byte
	binary.LittleEndian.PutUint64(lenb[:], uint64(len(payload)))
	buf.Write(lenb[:])
	buf.WriteString(bundleMagic)
	if err := os.WriteFile(*out, buf.Bytes(), 0o755); err != nil {
		return fail2("%v", err)
	}
	fmt.Fprintf(os.Stderr, "built %s (%d bytes, capabilities: %s)\n", *out, buf.Len(), strings.Join(allow, ","))
	return 0
}

// runBundle runs the program embedded in this executable, if any.
func runBundle() (int, bool) {
	exe, err := os.Executable()
	if err != nil {
		return 0, false
	}
	f, err := os.Open(exe)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 16 {
		return 0, false
	}
	tail := make([]byte, 16)
	if _, err := f.ReadAt(tail, st.Size()-16); err != nil || string(tail[8:]) != bundleMagic {
		return 0, false
	}
	n := int64(binary.LittleEndian.Uint64(tail[:8]))
	payload := make([]byte, n)
	if _, err := f.ReadAt(payload, st.Size()-16-n); err != nil {
		return 2, true
	}
	var b loader.Bundle
	if err := json.Unmarshal(payload, &b); err != nil {
		fmt.Fprintln(os.Stderr, "corrupted bundle:", err)
		return 2, true
	}
	prog, ds := loader.FromBundle(&b)
	if diag.HasErrors(ds) {
		for _, d := range ds {
			fmt.Fprintln(os.Stderr, d.String())
		}
		return interp.ExitCompile, true
	}
	allow := map[string]bool{}
	for _, c := range b.Allow {
		allow[c] = true
	}
	in := interp.New(prog, interp.Options{Allow: allow, Args: os.Args[1:]})
	res := in.Run()
	if res.Status == "panic" || res.Status == "failure" {
		fmt.Fprintln(os.Stderr, res.Describe())
	}
	return res.ExitCode, true
}

package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/printer"
	"github.com/marcodc74/pygo/internal/sig"
)

type Checker struct {
	prog    *loader.Program
	mods    map[string]*modInfo
	std     map[string]*modInfo
	core    *modInfo
	methods map[string]map[string]*funcInfo // builtin type -> method
	diags   []diag.Diagnostic
	cur     *modInfo
	// fallibleCalls counts fallible calls seen (to detect useless try/catch).
	fallibleCalls int
	// Effects maps "file:fn" to the effects each function declares (for tools).
	Effects map[string][]string
}

// Check type-checks a loaded program and returns its diagnostics.
func Check(prog *loader.Program) []diag.Diagnostic {
	c := New(prog)
	c.Run()
	return c.Diagnostics()
}

func New(prog *loader.Program) *Checker {
	c := &Checker{prog: prog, mods: map[string]*modInfo{}, std: map[string]*modInfo{}, methods: map[string]map[string]*funcInfo{}, Effects: map[string][]string{}}
	c.loadStd()
	return c
}

func (c *Checker) Diagnostics() []diag.Diagnostic { return diag.Sort(c.diags) }

func (c *Checker) Run() {
	for _, file := range c.prog.Order {
		c.collect(file)
	}
	for _, file := range c.prog.Order {
		c.checkModule(c.mods[file])
	}
}

// ---------- diagnostics ----------

func (c *Checker) report(sev diag.Severity, code string, pos ast.Pos, hint, format string, args ...any) {
	file := ""
	if c.cur != nil {
		file = c.cur.path
	}
	c.diags = append(c.diags, diag.Diagnostic{
		Code: code, Severity: sev, Message: fmt.Sprintf(format, args...),
		File: file, Line: pos.Line, Col: pos.Col, Hint: hint,
	})
}

func (c *Checker) errorf(code string, pos ast.Pos, hint, format string, args ...any) {
	c.report(diag.Error, code, pos, hint, format, args...)
}

func (c *Checker) warnf(code string, pos ast.Pos, hint, format string, args ...any) {
	c.report(diag.Warning, code, pos, hint, format, args...)
}

func didYouMean(name string, cands []string) string {
	if s := diag.Suggest(name, cands); s != "" {
		return fmt.Sprintf("did you mean '%s'?", s)
	}
	return ""
}

// ---------- stdlib ----------

var builtinTypeNames = []string{"Str", "List", "Map", "Range", "Chan", "Task"}

func (c *Checker) loadStd() {
	names := append([]string{"core"}, sig.Names()...)
	for _, name := range names {
		h := sig.Get(name)
		m := newMod(name, "<std/"+name+">", true)
		c.std[name] = m
		if name == "core" {
			c.core = m
		}
		f := &ast.File{Name: m.path}
		for _, sd := range h.Structs {
			f.Decls = append(f.Decls, sd)
		}
		for _, fd := range h.Funcs {
			f.Decls = append(f.Decls, fd)
		}
		m.file = f
		save := c.cur
		c.cur = m
		for _, sd := range h.Structs {
			m.structs[sd.Name] = &structInfo{name: sd.Name, decl: sd, methods: map[string]*funcInfo{}, mod: m}
		}
		for _, sd := range h.Structs {
			c.resolveStruct(m.structs[sd.Name])
		}
		for _, fd := range h.Funcs {
			fi := c.funcSig(fd, m, nil)
			fi.std = true
			if name != "core" {
				fi.name = name + "." + fd.Name
			}
			m.funcs[fd.Name] = fi
		}
		for cn, cd := range h.Consts {
			switch cd.Let.Value.(type) {
			case *ast.FloatLit:
				m.consts[cn] = tFloat
			case *ast.IntLit:
				m.consts[cn] = tInt
			default:
				m.consts[cn] = tAny
			}
		}
		c.cur = save
	}
	c.core = c.std["core"]
	save := c.cur
	c.cur = c.core
	for _, tn := range builtinTypeNames {
		ms := map[string]*funcInfo{}
		var recv *Type
		switch tn {
		case "Str":
			recv = tStr
		case "List":
			recv = listOf(paramType("T"))
		case "Map":
			recv = mapOf(paramType("K"), paramType("V"))
		case "Range":
			recv = tRange
		case "Chan":
			recv = chanOf(paramType("T"))
		case "Task":
			recv = taskOf(paramType("T"))
		}
		for mn, fd := range sig.Get("core").Methods[tn] {
			fi := c.funcSig(fd, c.core, recv)
			fi.std = true
			fi.name = tn + "." + mn
			ms[mn] = fi
		}
		c.methods[tn] = ms
	}
	c.cur = save
}

// ---------- collection ----------

func (c *Checker) collect(file string) {
	f := c.prog.Files[file]
	m := newMod(strings.TrimSuffix(pathBase(file), ".pg"), file, false)
	m.file = f
	c.mods[file] = m
	c.cur = m
	seen := map[string]ast.Pos{}
	dup := func(name string, pos ast.Pos) bool {
		if prev, ok := seen[name]; ok {
			c.errorf("E0207", pos, fmt.Sprintf("the first declaration is at line %d; rename one of them", prev.Line), "'%s' is declared twice", name)
			return true
		}
		seen[name] = pos
		return false
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.ImportDecl:
			name := d.Alias
			if name == "" {
				name = strings.TrimSuffix(pathBase(d.Path), ".pg")
			}
			if dup(name, d.Pos) {
				continue
			}
			if loader.IsLocal(d.Path) {
				if tm := c.mods[c.prog.Imports[file][d.Path]]; tm != nil {
					m.imports[name] = tm
				}
			} else if sm := c.std[d.Path]; sm != nil && d.Path != "core" {
				m.imports[name] = sm
			}
		case *ast.ExternDecl:
			if !dup(d.Name(), d.Pos) {
				m.imports[d.Name()] = c.externModule(d, file)
				c.cur = m
			}
		case *ast.StructDecl:
			if !dup(d.Name, d.Pos) {
				c.checkTypeName(d.Name, d.Pos)
				m.structs[d.Name] = &structInfo{name: d.Name, decl: d, methods: map[string]*funcInfo{}, mod: m}
			}
		case *ast.EnumDecl:
			if !dup(d.Name, d.Pos) {
				c.checkTypeName(d.Name, d.Pos)
				m.enums[d.Name] = &enumInfo{name: d.Name, methods: map[string]*funcInfo{}, mod: m}
			}
		case *ast.FuncDecl:
			dup(d.Name, d.Pos)
		case *ast.ConstDecl:
			dup(d.Let.Name, d.Pos)
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.StructDecl:
			if si := m.structs[d.Name]; si != nil && si.decl == d {
				c.resolveStruct(si)
			}
		case *ast.EnumDecl:
			ei := m.enums[d.Name]
			if ei == nil {
				continue
			}
			vseen := map[string]bool{}
			for _, v := range d.Variants {
				if vseen[v.Name] {
					c.errorf("E0207", v.Pos, "", "variant '%s' is declared twice", v.Name)
					continue
				}
				vseen[v.Name] = true
				vi := &variantInfo{name: v.Name}
				for _, fd := range v.Fields {
					vi.fields = append(vi.fields, &fieldInfo{name: fd.Name, typ: c.resolveType(fd.Type, nil), hasDefault: fd.Default != nil, pos: fd.Pos})
				}
				ei.variants = append(ei.variants, vi)
			}
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if _, exists := m.funcs[d.Name]; !exists {
				if c.core.funcs[d.Name] != nil {
					c.errorf("E0206", d.Pos, "choose another name", "function '%s' shadows the builtin %s()", d.Name, d.Name)
				}
				m.funcs[d.Name] = c.funcSig(d, m, nil)
			}
		case *ast.ImplDecl:
			var recv *Type
			var methods map[string]*funcInfo
			if si, ok := m.structs[d.Type]; ok {
				recv, methods = &Type{K: KStruct, Struct: si}, si.methods
			} else if ei, ok := m.enums[d.Type]; ok {
				recv, methods = &Type{K: KEnum, Enum: ei}, ei.methods
			} else {
				c.errorf("E0203", d.Pos, didYouMean(d.Type, m.typeNames()), "impl of unknown type '%s' (impl only works on struct/enum types of this module)", d.Type)
				continue
			}
			for _, md := range d.Methods {
				if _, exists := methods[md.Name]; exists {
					c.errorf("E0207", md.Pos, "", "method '%s.%s' is declared twice", d.Type, md.Name)
					continue
				}
				if recv.K == KStruct && recv.Struct.field(md.Name) != nil {
					c.errorf("E0207", md.Pos, "rename the method", "method '%s' has the same name as a field of %s", md.Name, d.Type)
				}
				fi := c.funcSig(md, m, recv)
				fi.name = d.Type + "." + md.Name
				methods[md.Name] = fi
			}
		}
	}
}

func (c *Checker) checkTypeName(name string, pos ast.Pos) {
	switch name {
	case "Int", "Float", "Str", "Bool", "Any", "List", "Map", "Chan", "Task", "Range", "Error", "Type", "Nil":
		c.errorf("E0207", pos, "choose another name", "'%s' is a builtin type name", name)
	}
}

func (m *modInfo) typeNames() []string {
	var out []string
	for k := range m.structs {
		out = append(out, k)
	}
	for k := range m.enums {
		out = append(out, k)
	}
	return out
}

func pathBase(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (c *Checker) resolveStruct(si *structInfo) {
	seen := map[string]bool{}
	for _, f := range si.decl.Fields {
		if seen[f.Name] {
			c.errorf("E0207", f.Pos, "", "field '%s' is declared twice", f.Name)
			continue
		}
		seen[f.Name] = true
		t := c.resolveType(f.Type, nil)
		fi := &fieldInfo{name: f.Name, typ: t, hasDefault: f.Default != nil || (t != nil && t.K == KOpt), pos: f.Pos}
		si.fields = append(si.fields, fi)
	}
}

func (c *Checker) funcSig(d *ast.FuncDecl, m *modInfo, recv *Type) *funcInfo {
	fi := &funcInfo{name: d.Name, decl: d, fallible: d.Fallible, uses: d.Uses, typeParams: d.TypeParams, hasSelf: d.HasSelf, recv: recv, mod: m}
	tps := map[string]bool{}
	for _, tp := range d.TypeParams {
		tps[tp] = true
	}
	if m.std && recv != nil {
		for _, tp := range []string{"T", "K", "V", "U"} {
			tps[tp] = true
		}
	}
	for _, u := range d.Uses {
		if !sig.IsEffect(u) {
			c.errorf("E0502", d.Pos, "effects are: "+strings.Join(sig.AllEffects, ", "), "unknown effect '%s'", u)
		}
	}
	if d.HasSelf && recv == nil {
		c.errorf("E0903", d.Pos, "", "'self' is only allowed in impl methods")
	}
	for _, p := range d.Params {
		t := c.resolveType(p.Type, tps)
		if p.Variadic {
			t = listOf(t)
		}
		fi.params = append(fi.params, paramInfo{name: p.Name, typ: t, hasDefault: p.Default != nil, variadic: p.Variadic})
	}
	if d.Ret != nil {
		fi.ret = c.resolveType(d.Ret, tps)
	} else {
		fi.ret = tVoid
	}
	return fi
}

// resolveType converts an annotation into a Type.
func (c *Checker) resolveType(te *ast.TypeExpr, tps map[string]bool) *Type {
	if te == nil {
		return tAny
	}
	t := c.resolveBase(te, tps)
	if te.Optional {
		return optOf(t)
	}
	return t
}

func (c *Checker) resolveBase(te *ast.TypeExpr, tps map[string]bool) *Type {
	arg := func(i int) *Type {
		if i < len(te.Args) {
			return c.resolveType(te.Args[i], tps)
		}
		return tAny
	}
	nargs := func(n int) {
		if len(te.Args) != n && !(len(te.Args) == 0) {
			c.errorf("E0203", te.Pos, "", "%s takes %d type arguments, got %d", te.Name, n, len(te.Args))
		}
	}
	switch te.Name {
	case "Int":
		return tInt
	case "Float":
		return tFloat
	case "Str":
		return tStr
	case "Bool":
		return tBool
	case "Any":
		return tAny
	case "Nil":
		return tNil
	case "Range":
		return tRange
	case "List":
		nargs(1)
		return listOf(arg(0))
	case "Map":
		nargs(2)
		k := arg(0)
		if !isUnknown(k) && k.K != KInt && k.K != KStr && k.K != KBool && k.K != KFloat {
			c.errorf("E0301", te.Pos, "use Int, Str, Bool or Float keys", "invalid map key type %s", k)
		}
		return mapOf(k, arg(1))
	case "Chan":
		nargs(1)
		return chanOf(arg(0))
	case "Task":
		nargs(1)
		return taskOf(arg(0))
	case "Type":
		nargs(1)
		return typeValOf(arg(0))
	case "Error":
		return &Type{K: KStruct, Struct: c.core.structs["Error"]}
	case "fn":
		t := &Type{K: KFn, Fallible: te.Fallible}
		for _, a := range te.Args {
			t.Params = append(t.Params, c.resolveType(a, tps))
		}
		if te.Ret != nil {
			t.Ret = c.resolveType(te.Ret, tps)
		} else {
			t.Ret = tVoid
		}
		return t
	}
	if tps[te.Name] {
		return paramType(te.Name)
	}
	m := c.cur
	if i := strings.IndexByte(te.Name, '.'); i >= 0 {
		alias, name := te.Name[:i], te.Name[i+1:]
		im := m.imports[alias]
		if im == nil {
			c.errorf("E0208", te.Pos, fmt.Sprintf("add: import \"%s\"", alias), "module '%s' is not imported", alias)
			return tAny
		}
		m.usedImp[alias] = true
		if si, ok := im.structs[name]; ok {
			return &Type{K: KStruct, Struct: si}
		}
		if ei, ok := im.enums[name]; ok {
			return &Type{K: KEnum, Enum: ei}
		}
		c.errorf("E0203", te.Pos, didYouMean(name, im.typeNames()), "module %s has no type '%s'", alias, name)
		return tAny
	}
	if si, ok := m.structs[te.Name]; ok {
		return &Type{K: KStruct, Struct: si}
	}
	if ei, ok := m.enums[te.Name]; ok {
		return &Type{K: KEnum, Enum: ei}
	}
	cands := append(m.typeNames(), "Int", "Float", "Str", "Bool", "Any", "List", "Map", "Chan", "Task", "Range", "Error")
	hint := didYouMean(te.Name, cands)
	if hint == "" && len(te.Name) == 1 {
		hint = fmt.Sprintf("declare the type parameter: fn name[%s](...)", te.Name)
	}
	c.errorf("E0203", te.Pos, hint, "unknown type '%s'", te.Name)
	return tAny
}

// ---------- modules ----------

func (c *Checker) checkModule(m *modInfo) {
	c.cur = m
	f := m.file
	// module-level constants, in order
	for _, d := range f.Decls {
		cd, ok := d.(*ast.ConstDecl)
		if !ok {
			continue
		}
		fc := &fnCtx{name: "<module init>", ret: tVoid, uses: map[string]bool{}, used: map[string]ast.Pos{}, moduleInit: true}
		sc := newScope(nil, c)
		t := c.expr(fc, sc, cd.Let.Value, c.maybeType(cd.Let.Type))
		if cd.Let.Type != nil {
			want := c.resolveType(cd.Let.Type, nil)
			if !assignable(t, want) {
				c.errorf("E0301", cd.Let.Value.P(), "", "cannot use %s as %s in constant '%s'", t, want, cd.Let.Name)
			}
			t = want
		}
		if t.K == KVoid {
			c.errorf("E0301", cd.Pos, "", "'%s' has no value", cd.Let.Name)
		}
		m.consts[cd.Let.Name] = t
		c.finishEffects(fc, cd.Pos)
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if fi := m.funcs[d.Name]; fi != nil && fi.decl == d {
				c.checkFunc(fi)
				if d.Name == "main" {
					c.checkMain(fi)
				}
			}
		case *ast.ImplDecl:
			var methods map[string]*funcInfo
			if si, ok := m.structs[d.Type]; ok {
				methods = si.methods
			} else if ei, ok := m.enums[d.Type]; ok {
				methods = ei.methods
			}
			for _, md := range d.Methods {
				if fi := methods[md.Name]; fi != nil && fi.decl == md {
					c.checkFunc(fi)
				}
			}
		case *ast.TestDecl:
			fc := &fnCtx{name: "test", ret: tVoid, fallible: true, isTest: true, uses: map[string]bool{}, used: map[string]ast.Pos{}}
			sc := newScope(nil, c)
			c.block(fc, sc, d.Body)
			sc.close()
		case *ast.StructDecl:
			if si := m.structs[d.Name]; si != nil {
				for i, fd := range d.Fields {
					if fd.Default != nil && i < len(si.fields) {
						fc := &fnCtx{name: "<default>", ret: tVoid, uses: map[string]bool{}, used: map[string]ast.Pos{}, moduleInit: true}
						t := c.expr(fc, newScope(nil, c), fd.Default, si.fields[i].typ)
						if !assignable(t, si.fields[i].typ) {
							c.errorf("E0301", fd.Default.P(), "", "default of field '%s' is %s, expected %s", fd.Name, t, si.fields[i].typ)
						}
					}
				}
			}
		}
	}
	for _, d := range f.Decls {
		if ex, ok := d.(*ast.ExternDecl); ok && !m.usedImp[ex.Name()] && m.imports[ex.Name()] != nil {
			c.warnf("W0202", ex.Pos, "remove the extern block", "extern module '%s' is declared but not used", ex.Name())
		}
		if imp, ok := d.(*ast.ImportDecl); ok {
			name := imp.Alias
			if name == "" {
				name = strings.TrimSuffix(pathBase(imp.Path), ".pg")
			}
			if !m.usedImp[name] && m.imports[name] != nil {
				c.warnf("W0202", imp.Pos, "remove the import", "module '%s' is imported but not used", name)
			}
		}
	}
}

func (c *Checker) checkMain(fi *funcInfo) {
	if len(fi.params) > 0 {
		c.errorf("E0904", fi.decl.Pos, "read arguments with os.args()", "main takes no parameters")
	}
	if fi.ret.K != KVoid {
		c.errorf("E0904", fi.decl.Pos, "use fn main() or fn main() -> ! ; exit codes via os.exit(n)", "main cannot return a value")
	}
}

func (c *Checker) maybeType(te *ast.TypeExpr) *Type {
	if te == nil {
		return nil
	}
	return c.resolveType(te, nil)
}

// ---------- functions ----------

type fnCtx struct {
	name       string
	fi         *funcInfo
	ret        *Type
	fallible   bool
	uses       map[string]bool
	used       map[string]ast.Pos // effect -> first use
	loop       int
	try        int
	parent     *fnCtx
	isLambda   bool
	inferRet   bool
	rets       []*Type
	isTest     bool
	moduleInit bool
	tps        map[string]bool
}

// root returns the enclosing declared function (lambdas report effects to it).
func (f *fnCtx) root() *fnCtx {
	for f.isLambda && f.parent != nil {
		f = f.parent
	}
	return f
}

func (c *Checker) useEffects(fc *fnCtx, effs []string, pos ast.Pos) {
	r := fc.root()
	for _, e := range effs {
		if _, ok := r.used[e]; !ok {
			r.used[e] = pos
		}
	}
}

func (c *Checker) finishEffects(fc *fnCtx, declPos ast.Pos) {
	if fc.isTest {
		return
	}
	var missing []string
	for e := range fc.used {
		if !fc.uses[e] {
			missing = append(missing, e)
		}
	}
	sort.Strings(missing)
	for _, e := range missing {
		if fc.moduleInit {
			c.errorf("E0501", fc.used[e], "compute it inside a function instead", "module initialization cannot use the '%s' effect", e)
			continue
		}
		all := append([]string{}, sortedKeys(fc.uses)...)
		all = append(all, missing...)
		sort.Strings(all)
		c.errorf("E0501", fc.used[e], "declare it: uses "+strings.Join(all, ", "), "%s uses the '%s' effect but does not declare it", fc.name, e)
	}
	for e := range fc.uses {
		if _, ok := fc.used[e]; !ok && fc.fi != nil && fc.fi.decl.Body != nil && !fc.fi.std {
			c.warnf("W0503", declPos, "remove it from 'uses' (least privilege)", "%s declares effect '%s' but never uses it", fc.name, e)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Checker) checkFunc(fi *funcInfo) {
	d := fi.decl
	fc := &fnCtx{name: fi.name, fi: fi, ret: fi.ret, fallible: fi.fallible, uses: map[string]bool{}, used: map[string]ast.Pos{}, tps: map[string]bool{}}
	for _, u := range d.Uses {
		fc.uses[u] = true
	}
	for _, tp := range d.TypeParams {
		fc.tps[tp] = true
	}
	sc := newScope(nil, c)
	if d.HasSelf {
		sc.declare("self", fi.recv, false, d.Pos, "param")
		sc.vars["self"].used = true
	}
	for i, p := range d.Params {
		if p.Default != nil {
			t := c.expr(fc, newScope(nil, c), p.Default, fi.params[i].typ)
			if !assignable(t, fi.params[i].typ) && !(t.K == KNil) {
				c.errorf("E0301", p.Default.P(), "", "default of '%s' is %s, expected %s", p.Name, t, fi.params[i].typ)
			}
		}
		if c.checkShadow(sc, p.Name, p.Pos) {
			continue
		}
		pt := fi.params[i].typ
		if pd, ok := p.Default.(*ast.NilLit); ok && pd != nil {
			pt = optOf(pt)
		}
		sc.declare(p.Name, pt, false, p.Pos, "param")
		sc.vars[p.Name].used = true
	}
	for _, r := range d.Requires {
		c.cond(fc, sc, r, "requires")
	}
	if len(d.Ensures) > 0 {
		es := newScope(sc, c)
		if fi.ret.K != KVoid {
			es.declare("result", fi.ret, false, d.Pos, "param")
		} else {
			c.errorf("E0308", d.Pos, "", "ensures needs a return type ('result' is undefined)")
		}
		for _, e := range d.Ensures {
			c.cond(fc, es, e, "ensures")
		}
	}
	if d.ExprBody != nil {
		t := c.expr(fc, sc, d.ExprBody, fi.ret)
		if fi.ret.K != KVoid && !assignable(t, fi.ret) {
			c.errorf("E0301", d.ExprBody.P(), "", "%s returns %s, but the body is %s", fi.name, fi.ret, t)
		}
	} else if d.Body != nil {
		c.block(fc, sc, d.Body)
		if !fi.std && fi.ret.K != KVoid && !terminates(d.Body.Stmts) {
			c.errorf("E0307", d.Body.End, "add a return statement at the end", "%s must return %s on every path", fi.name, fi.ret)
		}
	}
	sc.close()
	c.finishEffects(fc, d.Pos)
	c.Effects[c.cur.path+":"+fi.name] = d.Uses
}

func (c *Checker) cond(fc *fnCtx, sc *scope, e ast.Expr, what string) {
	t := c.expr(fc, sc, e, tBool)
	if !isUnknown(t) && t.K != KBool {
		c.errorf("E0303", e.P(), "conditions must be Bool (no truthiness): compare explicitly, e.g. x != nil, n > 0, not s.is_empty()", "%s condition is %s, not Bool", what, t)
	}
}

// terminates reports whether a statement list never falls through.
func terminates(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	switch s := stmts[len(stmts)-1].(type) {
	case *ast.Return, *ast.Fail:
		return true
	case *ast.ExprStmt:
		return exprTerminates(s.X)
	case *ast.While:
		if b, ok := s.Cond.(*ast.BoolLit); ok && b.Value && !hasBreak(s.Body) {
			return true
		}
	}
	return false
}

func exprTerminates(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.If:
		if e.Else == nil || !terminates(e.Then.Stmts) {
			return false
		}
		if b, ok := e.Else.(*ast.Block); ok {
			return terminates(b.Stmts)
		}
		return exprTerminates(e.Else)
	case *ast.Match:
		for _, a := range e.Arms {
			if b, ok := a.Body.(*ast.Block); ok {
				if !terminates(b.Stmts) {
					return false
				}
			} else if !exprTerminates(a.Body) {
				return false
			}
		}
		return len(e.Arms) > 0
	case *ast.Call:
		if id, ok := e.Fn.(*ast.Ident); ok && id.Name == "panic" {
			return true
		}
		if sel, ok := e.Fn.(*ast.Selector); ok && sel.Name == "exit" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" {
				return true
			}
		}
	case *ast.Block:
		return terminates(e.Stmts)
	}
	return false
}

func hasBreak(b *ast.Block) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Break:
			found = true
		case *ast.For, *ast.While, *ast.FuncLit:
			return n == ast.Node(b)
		}
		return !found
	})
	return found
}

// fix attaches a machine-applicable edit to the most recent diagnostic.
func (c *Checker) fix(pos ast.Pos, del int, insert string, safe bool) {
	if len(c.diags) == 0 {
		return
	}
	c.diags[len(c.diags)-1].Fix = &diag.Fix{Line: pos.Line, Col: pos.Col, Delete: del, Insert: insert, Safe: safe}
}

// suggestion extracts X from a "did you mean 'X'?" hint.
func suggestion(hint string) string {
	const pre = "did you mean '"
	if !strings.HasPrefix(hint, pre) {
		return ""
	}
	rest := hint[len(pre):]
	if i := strings.IndexByte(rest, '\''); i > 0 {
		return rest[:i]
	}
	return ""
}

// exprStart returns the position of the leftmost token of e.
func exprStart(e ast.Expr) ast.Pos {
	switch e := e.(type) {
	case *ast.Selector:
		return exprStart(e.X)
	case *ast.Call:
		return exprStart(e.Fn)
	case *ast.Index:
		return exprStart(e.X)
	case *ast.Binary:
		return exprStart(e.X)
	case *ast.Range:
		return exprStart(e.Lo)
	case *ast.Catch:
		return exprStart(e.X)
	case *ast.StructLit:
		return exprStart(e.Type)
	}
	return e.P()
}

// externModule builds the module of an `extern python "x" { ... }` block.
// Every foreign function is fallible and implicitly uses the python effect.
func (c *Checker) externModule(d *ast.ExternDecl, file string) *modInfo {
	name := d.Name()
	em := newMod(name, file, false)
	em.extern = d
	save := c.cur
	c.cur = em
	defer func() { c.cur = save }()
	for _, t := range d.Types {
		if _, dupT := em.structs[t.Name]; dupT {
			c.errorf("E0207", t.Pos, "", "type '%s' is declared twice", t.Name)
			continue
		}
		c.checkTypeName(t.Name, t.Pos)
		em.structs[t.Name] = &structInfo{opaque: true, name: t.Name, methods: map[string]*funcInfo{}, mod: em}
	}
	finish := func(fd *ast.FuncDecl, fi *funcInfo, what string) {
		if !fd.Fallible {
			ret := ""
			if fd.Ret != nil {
				ret = printer.Type(fd.Ret)
			}
			c.errorf("E0610", fd.Pos, fmt.Sprintf("write -> !%s: a foreign call can always raise an exception", ret), "%s must be declared fallible", what)
		}
		has := false
		for _, u := range fi.uses {
			has = has || u == "python"
		}
		if !has {
			fi.uses = append(append([]string{}, fi.uses...), "python")
		}
	}
	for _, t := range d.Types {
		si := em.structs[t.Name]
		if si == nil {
			continue
		}
		recv := &Type{K: KStruct, Struct: si}
		for _, md := range t.Methods {
			if !md.HasSelf {
				c.errorf("E0612", md.Pos, "write fn "+md.Name+"(self, ...)", "methods of extern types take self as first parameter")
			}
			if _, dupM := si.methods[md.Name]; dupM {
				c.errorf("E0207", md.Pos, "", "method '%s.%s' is declared twice", t.Name, md.Name)
				continue
			}
			fi := c.funcSig(md, em, recv)
			fi.name = name + "." + t.Name + "." + md.Name
			finish(md, fi, "extern method "+t.Name+"."+md.Name)
			si.methods[md.Name] = fi
		}
	}
	for _, fd := range d.Funcs {
		if _, dupF := em.funcs[fd.Name]; dupF {
			c.errorf("E0207", fd.Pos, "", "function '%s' is declared twice", fd.Name)
			continue
		}
		fi := c.funcSig(fd, em, nil)
		fi.name = name + "." + fd.Name
		finish(fd, fi, "extern function "+name+"."+fd.Name)
		em.funcs[fd.Name] = fi
	}
	return em
}

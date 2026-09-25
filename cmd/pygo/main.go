// Command pygo is the toolchain of the Pygo language: an AI-first
// programming language. Every command supports --json for agents.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/marcodc74/pygo/internal/check"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/interp"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/sig"
)

var version = "0.2.0-dev" // set by the release build (-ldflags -X main.version=...)

func init() {
	// `go install .../cmd/pygo@vX.Y.Z` builds without ldflags: take the
	// version from the module metadata instead.
	if strings.HasSuffix(version, "-dev") {
		if bi, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(bi.Main.Version, "v") {
			version = bi.Main.Version
		}
	}
}

const usage = `pygo - AI-first programming language toolchain

usage:
  pygo run      [--allow caps] [--max-steps N] [--timeout D] [--seed N] [--engine vm|tree] [--json] file.pg|app.pgc [-- args]
  pygo run      [--allow caps] -e 'code'        run a snippet (imports first, the rest becomes main)
  pygo check    [--json] file.pg                static check (diagnostics with codes, hints, fixes)
  pygo fix      [--all] [--dry-run] [--json] file.pg   apply machine fixes
  pygo test     [--json] [--filter s] [--engine vm|tree] file.pg|app.pgc|dir
  pygo compile  [-o app.pgc] [--json] file.pg   check and compile to bytecode (.pgc)
  pygo disasm   [--json] [--fn name] file.pg|app.pgc   show the bytecode
  pygo fmt      [-w] [--check] file.pg          canonical formatting
  pygo guide    [--stdlib]                      compact language reference for an AI context
  pygo explain  [--json] [CODE]                 explain a diagnostic/runtime code
  pygo describe file.pg|module                  API as JSON
  pygo outline  file.pg                         symbols with content hashes (JSON)
  pygo edit     file.pg --replace KEY | --insert-after KEY | --append | --delete KEY [--expect-hash H]
  pygo ast      file.pg                         syntax tree as JSON
  pygo build    [--allow caps] [--runtime bin] [--source] -o app file.pg   self-contained executable (bytecode)
  pygo extern   python MODULE [NAME ...]        draft extern block from Python signatures
  pygo version

capabilities (--allow): ` + "clock,env,fs,net,proc,python,rand or all" + `
Python for extern blocks: --python PATH, or PYGO_PYTHON (default python3)
exit codes: 0 ok, 1 failure, 2 panic, 3 compile error, 4 capability denied
`

func main() {
	if code, ok := runBundle(); ok {
		os.Exit(code)
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "run":
		os.Exit(cmdRun(args))
	case "check":
		os.Exit(cmdCheck(args))
	case "test":
		os.Exit(cmdTest(args))
	case "fmt":
		os.Exit(cmdFmt(args))
	case "fix":
		os.Exit(cmdFix(args))
	case "explain":
		os.Exit(cmdExplain(args))
	case "guide":
		os.Exit(cmdGuide(args))
	case "describe":
		os.Exit(cmdDescribe(args))
	case "outline":
		os.Exit(cmdOutline(args))
	case "edit":
		os.Exit(cmdEdit(args))
	case "ast":
		os.Exit(cmdAST(args))
	case "build":
		os.Exit(cmdBuild(args))
	case "compile":
		os.Exit(cmdCompile(args))
	case "disasm":
		os.Exit(cmdDisasm(args))
	case "extern":
		os.Exit(cmdExtern(args))
	case "version", "--version":
		fmt.Println("pygo", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// compactJSON renders v on one line without HTML escaping.
func compactJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// load parses and checks a program; ok=false if there are errors.
func load(file string, asJSON bool, showWarnings bool) (*loader.Program, []diag.Diagnostic, bool) {
	return loadWith(file, nil, asJSON, showWarnings)
}

func loadWith(file string, read loader.ReadFunc, asJSON bool, showWarnings bool) (*loader.Program, []diag.Diagnostic, bool) {
	prog, ds := loader.Load(file, read)
	if !diag.HasErrors(ds) {
		ds = append(ds, check.Check(prog)...)
	}
	ds = diag.Sort(ds)
	ok := !diag.HasErrors(ds)
	if !asJSON {
		for _, d := range ds {
			if d.Severity == diag.Error || showWarnings {
				fmt.Fprintln(os.Stderr, d.String())
			}
		}
	}
	return prog, ds, ok
}

func parseAllow(s string) map[string]bool {
	allow := map[string]bool{}
	for _, c := range strings.Split(s, ",") {
		c = strings.TrimSpace(c)
		if c == "all" {
			for _, e := range sig.AllEffects {
				allow[e] = true
			}
		} else if c != "" {
			allow[c] = true
		}
	}
	return allow
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print diagnostics as JSON")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pygo check [--json] file.pg")
		return 2
	}
	_, ds, ok := load(fs.Arg(0), *asJSON, true)
	if *asJSON {
		fmt.Println(diag.JSON(ds))
	}
	if !ok {
		return interp.ExitCompile
	}
	return 0
}

func validEngine(e string) bool {
	if e == "tree" || e == "vm" {
		return true
	}
	fmt.Fprintf(os.Stderr, "unknown engine %q (use tree or vm)\n", e)
	return false
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	allowS := fs.String("allow", "", "granted capabilities, comma separated (clock,env,fs,net,proc,rand|all)")
	maxSteps := fs.Int64("max-steps", 0, "abort after N steps (0 = unlimited)")
	timeout := fs.Duration("timeout", 0, "abort after this duration (0 = unlimited)")
	seed := fs.Int64("seed", 0, "seed of the rand module")
	asJSON := fs.Bool("json", false, "print diagnostics and the run result as JSON (result on stderr)")
	snippet := fs.String("e", "", "run this code instead of a file")
	python := fs.String("python", "", "Python interpreter for extern python blocks (default: PYGO_PYTHON or python3)")
	engine := fs.String("engine", "vm", "execution engine: vm (bytecode virtual machine) or tree (interpreter)")
	fs.Parse(args)
	if !validEngine(*engine) {
		return 2
	}
	if fs.NArg() < 1 && *snippet == "" {
		fmt.Fprintln(os.Stderr, "usage: pygo run [flags] file.pg [-- args]")
		return 2
	}
	var file string
	var progArgs []string
	var read loader.ReadFunc
	if *snippet != "" {
		file = "snippet.pg"
		src := wrapSnippet(*snippet, *allowS)
		read = func(p string) (string, error) {
			if p == file {
				return src, nil
			}
			return readFile(p)
		}
		progArgs = fs.Args()
	} else {
		file = fs.Arg(0)
		progArgs = fs.Args()[1:]
	}
	if len(progArgs) > 0 && progArgs[0] == "--" {
		progArgs = progArgs[1:]
	}
	prog, compiled, ds, ok := loadRunnable(file, read, *asJSON)
	if !ok {
		if *asJSON && ds != nil {
			fmt.Fprintln(os.Stderr, diag.JSON(ds))
		}
		return interp.ExitCompile
	}
	allow := parseAllow(*allowS)
	if code := preflight(prog, allow, *asJSON); code != 0 {
		return code
	}
	in := interp.New(prog, interp.Options{Allow: allow, MaxSteps: *maxSteps, Timeout: *timeout, Seed: *seed, Args: progArgs, Python: *python, Engine: *engine, Compiled: compiled})
	res := in.Run()
	if *asJSON {
		fmt.Fprintln(os.Stderr, compactJSON(res))
	} else if res.Status == "panic" || res.Status == "failure" {
		fmt.Fprintln(os.Stderr, res.Describe())
	}
	return res.ExitCode
}

// preflight refuses to start when main declares capabilities not granted.
func preflight(prog *loader.Program, allow map[string]bool, asJSON bool) int {
	var missing []string
	for _, e := range mainUses(prog) {
		if !allow[e] {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return 0
	}
	sort.Strings(missing)
	msg := fmt.Sprintf("main declares capabilities that were not granted: %s", strings.Join(missing, ", "))
	hint := "run with --allow " + strings.Join(mainUses(prog), ",")
	if asJSON {
		fmt.Fprintln(os.Stderr, compactJSON(map[string]any{"exit_code": interp.ExitPermission, "status": "denied", "message": msg, "hint": hint, "missing": missing}))
	} else {
		fmt.Fprintf(os.Stderr, "%s\n    hint: %s\n", msg, hint)
	}
	return interp.ExitPermission
}

func cmdTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print results as JSON")
	filter := fs.String("filter", "", "run only tests whose name contains this text")
	allowS := fs.String("allow", "all", "granted capabilities for tests")
	python := fs.String("python", "", "Python interpreter for extern python blocks")
	engine := fs.String("engine", "vm", "execution engine: vm or tree")
	fs.Parse(args)
	if !validEngine(*engine) {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pygo test [--json] [--filter s] file.pg|dir")
		return 2
	}
	var files []string
	target := fs.Arg(0)
	if st, err := os.Stat(target); err == nil && st.IsDir() {
		filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".pg") {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{target}
	}
	type report struct {
		OK          bool                `json:"ok"`
		Passed      int                 `json:"passed"`
		Failed      int                 `json:"failed"`
		Tests       []interp.TestResult `json:"tests"`
		Diagnostics []diag.Diagnostic   `json:"diagnostics,omitempty"`
	}
	rep := report{OK: true}
	start := time.Now()
	for _, f := range files {
		prog, compiled, ds, ok := loadRunnable(f, nil, *asJSON)
		if !ok {
			rep.OK = false
			rep.Diagnostics = append(rep.Diagnostics, ds...)
			continue
		}
		in := interp.New(prog, interp.Options{Allow: parseAllow(*allowS), Timeout: time.Minute, Python: *python, Engine: *engine, Compiled: compiled})
		for _, tr := range in.RunTests([]string{prog.Main}, *filter) {
			rep.Tests = append(rep.Tests, tr)
			if tr.Passed {
				rep.Passed++
			} else {
				rep.Failed++
				rep.OK = false
			}
			if !*asJSON {
				status := "PASS"
				if !tr.Passed {
					status = "FAIL"
				}
				fmt.Printf("%s  %s  (%s:%d, %.1fms)\n", status, tr.Name, tr.File, tr.Line, tr.Millis)
				if !tr.Passed {
					fmt.Println("    " + strings.ReplaceAll(tr.Failure.Describe(), "\n", "\n    "))
				}
			}
		}
	}
	if *asJSON {
		printJSON(rep)
	} else {
		fmt.Printf("\n%d passed, %d failed (%s)\n", rep.Passed, rep.Failed, time.Since(start).Round(time.Millisecond))
	}
	if !rep.OK {
		if rep.Failed == 0 {
			return interp.ExitCompile
		}
		return 1
	}
	return 0
}

// wrapSnippet turns loose statements into a program: import lines stay at
// the top, everything else becomes the body of a fallible main that uses
// the granted capabilities.
func wrapSnippet(code, allow string) string {
	if strings.Contains(code, "fn main(") {
		return code
	}
	var imports, body []string
	for _, line := range strings.Split(code, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "import ") {
			imports = append(imports, strings.TrimSpace(line))
		} else {
			body = append(body, "    "+line)
		}
	}
	var caps []string
	for c := range parseAllow(allow) {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	uses := ""
	if len(caps) > 0 {
		uses = " uses " + strings.Join(caps, ", ")
	}
	return strings.Join(imports, "\n") + "\nfn main() -> !" + uses + " {\n" + strings.Join(body, "\n") + "\n}\n"
}

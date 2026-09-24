// Command pygo is the toolchain of the Pygo language: an AI-first
// programming language. Every command supports --json for agents.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marcodc74/pygo/internal/check"
	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/interp"
	"github.com/marcodc74/pygo/internal/loader"
	"github.com/marcodc74/pygo/internal/sig"
)

var version = "0.1.0-dev"

const usage = `pygo - AI-first programming language toolchain

usage:
  pygo run   [--allow caps] [--max-steps N] [--timeout D] [--seed N] [--json] file.pg [-- args]
  pygo check [--json] file.pg
  pygo test  [--json] [--filter s] file.pg|dir
  pygo version

capabilities (--allow): ` + "clock,env,fs,net,proc,rand or all" + `
exit codes: 0 ok, 1 failure, 2 panic, 3 compile error, 4 capability denied
`

func main() {
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
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// load parses and checks a program; ok=false if there are errors.
func load(file string, asJSON bool, showWarnings bool) (*loader.Program, []diag.Diagnostic, bool) {
	prog, ds := loader.Load(file, nil)
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

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	allowS := fs.String("allow", "", "granted capabilities, comma separated (clock,env,fs,net,proc,rand|all)")
	maxSteps := fs.Int64("max-steps", 0, "abort after N steps (0 = unlimited)")
	timeout := fs.Duration("timeout", 0, "abort after this duration (0 = unlimited)")
	seed := fs.Int64("seed", 0, "seed of the rand module")
	asJSON := fs.Bool("json", false, "print diagnostics and the run result as JSON (result on stderr)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pygo run [flags] file.pg [-- args]")
		return 2
	}
	file := fs.Arg(0)
	progArgs := fs.Args()[1:]
	if len(progArgs) > 0 && progArgs[0] == "--" {
		progArgs = progArgs[1:]
	}
	prog, ds, ok := load(file, *asJSON, false)
	if !ok {
		if *asJSON {
			fmt.Fprintln(os.Stderr, diag.JSON(ds))
		}
		return interp.ExitCompile
	}
	allow := parseAllow(*allowS)
	if code := preflight(prog, allow, *asJSON); code != 0 {
		return code
	}
	in := interp.New(prog, interp.Options{Allow: allow, MaxSteps: *maxSteps, Timeout: *timeout, Seed: *seed, Args: progArgs})
	res := in.Run()
	if *asJSON {
		b, _ := json.Marshal(res)
		fmt.Fprintln(os.Stderr, string(b))
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
		b, _ := json.Marshal(map[string]any{"exit_code": interp.ExitPermission, "status": "denied", "message": msg, "hint": hint, "missing": missing})
		fmt.Fprintln(os.Stderr, string(b))
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
	fs.Parse(args)
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
		prog, ds, ok := load(f, *asJSON, false)
		if !ok {
			rep.OK = false
			rep.Diagnostics = append(rep.Diagnostics, ds...)
			continue
		}
		in := interp.New(prog, interp.Options{Allow: parseAllow(*allowS), Timeout: time.Minute})
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

// Package diag defines the machine-readable diagnostics emitted by every
// phase of the toolchain (lexer, parser, checker, runtime).
//
// Every diagnostic has a stable code (e.g. E0201) so that an AI agent can
// react to it programmatically, and an optional hint that proposes a fix.
package diag

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Pos is a position in a source file (1-based line and column).
type Pos struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Diagnostic is a single problem found in a program.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Col      int      `json:"col"`
	Hint     string   `json:"hint,omitempty"`
	Fix      *Fix     `json:"fix,omitempty"`
}

// Fix is a machine-applicable edit: at (Line, Col) delete Delete runes and
// insert Insert. Safe fixes preserve the evident intent and are applied by
// `pygo fix`; unsafe ones (guesses such as did-you-mean) need `pygo fix --all`.
type Fix struct {
	Line   int    `json:"line"`
	Col    int    `json:"col"`
	Delete int    `json:"delete"`
	Insert string `json:"insert"`
	Safe   bool   `json:"safe"`
}

func (d Diagnostic) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d:%d: %s[%s]: %s", d.File, d.Line, d.Col, d.Severity, d.Code, d.Message)
	if d.Hint != "" {
		fmt.Fprintf(&b, "\n    hint: %s", d.Hint)
	}
	if d.Fix != nil {
		fmt.Fprintf(&b, "\n    fix: at %d:%d delete %d insert %q (safe=%v)", d.Fix.Line, d.Fix.Col, d.Fix.Delete, d.Fix.Insert, d.Fix.Safe)
	}
	return b.String()
}

// List collects diagnostics.
type List struct {
	File  string
	Items []Diagnostic
}

func (l *List) Add(sev Severity, code string, pos Pos, hint string, format string, args ...any) {
	l.Items = append(l.Items, Diagnostic{
		Code:     code,
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
		File:     l.File,
		Line:     pos.Line,
		Col:      pos.Col,
		Hint:     hint,
	})
}

func (l *List) Errorf(code string, pos Pos, hint string, format string, args ...any) {
	l.Add(Error, code, pos, hint, format, args...)
}

func (l *List) Warnf(code string, pos Pos, hint string, format string, args ...any) {
	l.Add(Warning, code, pos, hint, format, args...)
}

func (l *List) Merge(other []Diagnostic) { l.Items = append(l.Items, other...) }

func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Sort orders diagnostics by file, line and column, and removes duplicates.
func Sort(ds []Diagnostic) []Diagnostic {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].File != ds[j].File {
			return ds[i].File < ds[j].File
		}
		if ds[i].Line != ds[j].Line {
			return ds[i].Line < ds[j].Line
		}
		return ds[i].Col < ds[j].Col
	})
	out := ds[:0]
	var prev *Diagnostic
	for i := range ds {
		if prev != nil && prev.Code == ds[i].Code && prev.File == ds[i].File && prev.Line == ds[i].Line && prev.Col == ds[i].Col && prev.Message == ds[i].Message {
			continue
		}
		out = append(out, ds[i])
		prev = &out[len(out)-1]
	}
	return out
}

// JSON renders diagnostics as a JSON document.
func JSON(ds []Diagnostic) string {
	if ds == nil {
		ds = []Diagnostic{}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(struct {
		OK          bool         `json:"ok"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}{!HasErrors(ds), ds})
	return strings.TrimRight(b.String(), "\n")
}

// Suggest returns the candidate closest to name (by edit distance), or "".
func Suggest(name string, candidates []string) string {
	best, bestD := "", 1<<30
	for _, c := range candidates {
		if c == name {
			continue
		}
		d := levenshtein(strings.ToLower(name), strings.ToLower(c))
		if d < bestD || (d == bestD && c < best) {
			best, bestD = c, d
		}
	}
	limit := len(name)/3 + 1
	if limit > 3 {
		limit = 3
	}
	if bestD <= limit {
		return best
	}
	return ""
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// ApplyFixes applies fixes to src (line/col are 1-based, in runes). Fixes
// that overlap an already-applied one are skipped. Returns the new source
// and the number of fixes applied.
func ApplyFixes(src string, fixes []Fix) (string, int) {
	lines := strings.SplitAfter(src, "\n")
	sort.SliceStable(fixes, func(i, j int) bool {
		if fixes[i].Line != fixes[j].Line {
			return fixes[i].Line > fixes[j].Line
		}
		return fixes[i].Col > fixes[j].Col
	})
	applied := 0
	lastLine, lastCol := 1<<30, 1<<30
	for _, f := range fixes {
		if f.Line < 1 || f.Line > len(lines) {
			continue
		}
		if f.Line == lastLine && f.Col+f.Delete > lastCol {
			continue
		}
		r := []rune(lines[f.Line-1])
		c := f.Col - 1
		if c < 0 || c > len(r) || c+f.Delete > len(r) {
			continue
		}
		lines[f.Line-1] = string(r[:c]) + f.Insert + string(r[c+f.Delete:])
		lastLine, lastCol = f.Line, f.Col
		applied++
	}
	return strings.Join(lines, ""), applied
}

package ast

import (
	"fmt"
	"strings"
)

// ValidFormatSpec reports whether spec is a well-formed interpolation format
// spec — [[fill]<|>|^][0][width][.prec][type] — and returns a message when it is
// not. It mirrors the runtime parser (internal/interp/ops.go) so that a
// malformed literal is caught by the checker instead of panicking at run time.
func ValidFormatSpec(spec string) (string, bool) {
	rs := []rune(spec)
	i := 0
	if len(rs) >= 2 && strings.ContainsRune("<>^", rs[1]) {
		i = 2
	} else if len(rs) >= 1 && strings.ContainsRune("<>^", rs[0]) {
		i = 1
	}
	if i < len(rs) && rs[i] == '0' {
		i++
	}
	for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
		i++
	}
	if i < len(rs) && rs[i] == '.' {
		i++
		for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
			i++
		}
	}
	typ := rune(0)
	if i < len(rs) {
		typ = rs[i]
		i++
	}
	if i != len(rs) {
		return fmt.Sprintf("invalid format spec %q", spec), false
	}
	switch typ {
	case 0, 'f', 'e', '%', 'x', 'X', 'b', 'o', 'd':
		return "", true
	}
	return fmt.Sprintf("unknown format type '%c'", typ), false
}

package parser

import (
	"testing"

	"github.com/marcodc74/pygo/internal/ast"
	"github.com/marcodc74/pygo/internal/diag"
)

// -9223372036854775808 is the magnitude 2^63 folded by a unary minus: the
// only negative Int that has no positive counterpart.
func TestMinIntLiteralAccepted(t *testing.T) {
	e, ds := ParseExpr("t.pg", "-9223372036854775808", diag.Pos{})
	if diag.HasErrors(ds) {
		t.Fatalf("unexpected diagnostics: %v", ds)
	}
	lit, ok := e.(*ast.IntLit)
	if !ok {
		t.Fatalf("got %T, want *ast.IntLit", e)
	}
	if lit.Value != -9223372036854775808 {
		t.Fatalf("value %d", lit.Value)
	}
}

func TestPositiveMinIntRejected(t *testing.T) {
	_, ds := ParseExpr("t.pg", "9223372036854775808", diag.Pos{})
	if !diag.HasErrors(ds) {
		t.Fatal("expected E0117 for a literal that does not fit Int64")
	}
}

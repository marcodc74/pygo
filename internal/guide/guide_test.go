package guide

import (
	"strings"
	"testing"
)

func TestGuide(t *testing.T) {
	g := Text()
	for _, want := range []string{"fn parse_int(text: Str) -> !Int", "fn fs.read(path: Str) -> !Str uses fs", "fn map[U](f: fn(T) -> U) -> List[U]", "struct http.Request"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
	// keep it small enough to prime a context window (~4 chars per token)
	if n := len(g) / 4; n > 6000 {
		t.Errorf("guide is ~%d tokens, keep it under 6000", n)
	}
	t.Logf("guide size: ~%d tokens", len(g)/4)
}

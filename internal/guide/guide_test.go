package guide

import (
	"strings"
	"testing"
)

func TestGuide(t *testing.T) {
	g := Text()
	for _, want := range []string{"fn parse_int(text: Str) -> !Int", "fn fs.read(path: Str) -> !Str uses fs", "fn map[U](f: fn(T) -> U) -> List[U]", "struct http.Request", "fn html.raw(text: Str) -> Html", "fn http.html(status: Int, body: Html) -> Response", "fn http.dispatch(req: Request, routes: List[Route], middleware: List[Middleware] = []) -> Response", "struct http.Cookie", "struct http.Middleware", "fn http.request_id() -> Middleware", "fn http.metrics() -> Middleware", "fn http.metrics_text() -> Str", "fn http.tracing(service: Str, endpoint: Str) -> Middleware uses net", "fn crypto.sha256(data: Str) -> Str", "fn crypto.random_bytes(n: Int) -> !Str uses crypto", "fn jwt.verify_hs256(token: Str, key: Str) -> !Map[Str, Any]"} {
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

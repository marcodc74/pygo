package guide

import (
	"strings"
	"testing"
)

func TestGuide(t *testing.T) {
	g := Text()
	for _, want := range []string{"fn parse_int(text: Str) -> !Int", "fn fs.read(path: Str) -> !Str uses fs", "fn map[U](f: fn(T) -> U) -> List[U]", "struct http.Request", "fn html.raw(text: Str) -> Html", "fn http.html(status: Int, body: Html) -> Response", "fn http.dispatch(req: Request, routes: List[Route], middleware: List[Middleware] = []) -> Response", "struct http.Cookie", "struct http.Middleware", "fn http.request_id() -> Middleware", "fn http.metrics() -> Middleware", "fn http.metrics_text() -> Str", "fn http.tracing(service: Str, endpoint: Str) -> Middleware uses net", "struct http.Tls { cert: Str, key: Str }", "fn http.serve(addr: Str, handler: fn(Request) -> Response, max_body: Int = 1048576, tls: Tls? = nil) -> ! uses net", "fn http.openapi(routes: List[Route], title: Str, version: Str = \"1.0.0\", description: Str = \"\", server: Str = \"\") -> Str", "fn crypto.sha256(data: Str) -> Str", "fn crypto.random_bytes(n: Int) -> !Str uses crypto", "fn jwt.verify_hs256(token: Str, key: Str) -> !Map[Str, Any]", "struct sql.Conn", "fn sql.open(dsn: Str, timeout_ms: Int = 5000) -> !Conn uses sql", "fn sql.query(conn: Conn, text: Sql, args: List[Any] = [], timeout_ms: Int = 30000) -> !List[Map[Str, Any]] uses sql", "fn sql.query_as[T](conn: Conn, text: Sql, schema: Type[T], args: List[Any] = [], timeout_ms: Int = 30000) -> !List[T] uses sql"} {
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

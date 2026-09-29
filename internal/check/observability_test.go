package check

import "testing"

func TestObservabilityEffects(t *testing.T) {
	// the metrics middleware is pure; tracing sends spans, so it needs net
	expectCodes(t, `
import "http"

fn mw() -> List[http.Middleware] uses net {
    return [http.metrics(), http.tracing("catalog", endpoint: "http://localhost:4318")]
}
`)

	expectCodes(t, `
import "http"

fn mw() -> List[http.Middleware] {
    return [http.tracing("catalog", endpoint: "http://localhost:4318")]
}
`, "E0501")
}

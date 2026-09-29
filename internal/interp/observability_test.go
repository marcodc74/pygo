package interp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcodc74/pygo/internal/diag"
	"github.com/marcodc74/pygo/internal/loader"
)

func TestMetricsState(t *testing.T) {
	m := newHTTPMetrics()
	m.enter()
	m.enter()
	m.observe("GET", 200, 20*time.Millisecond)
	m.observe("GET", 500, time.Second)

	if m.inFlight != 0 || m.maxFlight != 2 {
		t.Fatalf("in-flight: got %d (max %d), want 0 (max 2)", m.inFlight, m.maxFlight)
	}
	if m.byStatus["GET\x00200"] != 1 || m.byStatus["GET\x00500"] != 1 || m.durCount != 2 {
		t.Fatalf("counters: %v count %d", m.byStatus, m.durCount)
	}
	// cumulative buckets: 0.02 -> le >= 0.025, 1s -> le >= 1
	want := []int64{0, 0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2}
	for i, w := range want {
		if m.buckets[i] != w {
			t.Fatalf("bucket %d = %d, want %d (all: %v)", i, m.buckets[i], w, m.buckets)
		}
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	m := newHTTPMetrics()
	m.byStatus["GET\x00200"] = 2
	m.byStatus["GET\x00404"] = 1
	m.durSum = 0.5
	m.durCount = 3
	m.inFlight = 1
	m.maxFlight = 4
	for i := range m.buckets {
		m.buckets[i] = 3
	}
	want := `# HELP pygo_http_requests_total HTTP requests handled, by method and status.
# TYPE pygo_http_requests_total counter
pygo_http_requests_total{method="GET",status="200"} 2
pygo_http_requests_total{method="GET",status="404"} 1
# HELP pygo_http_requests_in_flight In-flight HTTP requests.
# TYPE pygo_http_requests_in_flight gauge
pygo_http_requests_in_flight 1
# HELP pygo_http_requests_max_in_flight Peak concurrent HTTP requests.
# TYPE pygo_http_requests_max_in_flight gauge
pygo_http_requests_max_in_flight 4
# HELP pygo_http_request_duration_seconds HTTP request latency in seconds.
# TYPE pygo_http_request_duration_seconds histogram
pygo_http_request_duration_seconds_bucket{le="0.005"} 3
pygo_http_request_duration_seconds_bucket{le="0.01"} 3
pygo_http_request_duration_seconds_bucket{le="0.025"} 3
pygo_http_request_duration_seconds_bucket{le="0.05"} 3
pygo_http_request_duration_seconds_bucket{le="0.1"} 3
pygo_http_request_duration_seconds_bucket{le="0.25"} 3
pygo_http_request_duration_seconds_bucket{le="0.5"} 3
pygo_http_request_duration_seconds_bucket{le="1"} 3
pygo_http_request_duration_seconds_bucket{le="2.5"} 3
pygo_http_request_duration_seconds_bucket{le="5"} 3
pygo_http_request_duration_seconds_bucket{le="10"} 3
pygo_http_request_duration_seconds_bucket{le="+Inf"} 3
pygo_http_request_duration_seconds_sum 0.5
pygo_http_request_duration_seconds_count 3
`
	if got := metricsText(m); got != want {
		t.Fatalf("metrics text mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

const metricsProgram = `
import "http"

fn routes() -> List[http.Route] {
    return [
        http.Route{method: "GET", path: "/items", handler: fn(req) => http.json(200, body: [1, 2, 3])},
        http.Route{method: "GET", path: "/metrics", handler: fn(req) => http.text(200, body: http.metrics_text())},
    ]
}

fn get(path: Str) -> http.Response => http.dispatch(
    http.Request{method: "GET", path: path, query: {}, headers: {}, body: ""},
    routes: routes(),
    middleware: [http.metrics()],
)

fn main() {
    print(get("/items").status)
    print(get("/nope").status)
    let body = get("/metrics").body
    print(body.contains("pygo_http_requests_total{method=\"GET\",status=\"200\"} 1"))
    print(body.contains("pygo_http_requests_total{method=\"GET\",status=\"404\"} 1"))
    print(body.contains("pygo_http_request_duration_seconds_count 2"))
}
`

// The metrics middleware runs identically on all three engines.
func TestMetricsMiddlewareDispatch(t *testing.T) {
	expectOut(t, metricsProgram, `200
404
true
true
true`)
}

// runOnce compiles and runs a program on one engine and returns the interpreter
// (so tests can inspect its internal state).
func runOnce(t *testing.T, src string, allow ...string) (*Interp, *Result) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "main.pg")
	os.WriteFile(p, []byte(src), 0o644)
	prog, ds := loader.Load(p, nil)
	if diag.HasErrors(ds) {
		t.Fatalf("parse errors: %v", ds)
	}
	al := map[string]bool{}
	for _, a := range allow {
		al[a] = true
	}
	var out bytes.Buffer
	in := New(prog, Options{Stdout: &out, Stderr: &out, Allow: al, MaxSteps: 1_000_000})
	return in, in.Run()
}

func TestMetricsMiddlewareRecords(t *testing.T) {
	in, res := runOnce(t, metricsProgram)
	if res.Status != "ok" {
		t.Fatalf("status %s: %s", res.Status, res.Describe())
	}
	m := in.metrics
	if m.byStatus["GET\x00200"] != 2 || m.byStatus["GET\x00404"] != 1 {
		t.Fatalf("counters: %v", m.byStatus)
	}
	if m.durCount != 3 || m.inFlight != 0 || m.maxFlight != 1 {
		t.Fatalf("histogram/in-flight: count %d in-flight %d max %d", m.durCount, m.inFlight, m.maxFlight)
	}
}

func TestOTLPURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"http://localhost:4318", "http://localhost:4318/v1/traces", true},
		{"http://localhost:4318/", "http://localhost:4318/v1/traces", true},
		{"http://localhost:4318/v1/traces", "http://localhost:4318/v1/traces", true},
		{"https://collector.example/v1/traces", "https://collector.example/v1/traces", true},
		{"", "", false},
		{"localhost:4318", "", false},
		{"/v1/traces", "", false},
		{"ftp://host", "", false},
	}
	for _, c := range cases {
		got, ok := otlpURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("otlpURL(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestTracingExport(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	src := fmt.Sprintf(`
import "http"

fn routes() -> List[http.Route] {
    return [http.Route{method: "GET", path: "/ping", handler: fn(req) => http.text(200, body: "pong")}]
}

fn main() -> ! uses net {
    let r = http.dispatch(
        http.Request{method: "GET", path: "/ping", query: {}, headers: {}, body: ""},
        routes: routes(),
        middleware: [http.tracing("catalog", endpoint: %q)],
    )
    print(r.status, r.body)
}
`, srv.URL)

	out, res := runSrc(t, src, "net")
	if res.Status != "ok" {
		t.Fatalf("status %s: %s\noutput: %s", res.Status, res.Describe(), out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 { // tree, vm and pgc each export their span
		t.Fatalf("exports: got %d, want 3", len(bodies))
	}
	for _, p := range paths {
		if p != "/v1/traces" {
			t.Fatalf("export path %q, want /v1/traces", p)
		}
	}
	var payload struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
					Name    string `json:"name"`
					Status  struct {
						Code int `json:"code"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, bodies[0])
	}
	if len(payload.ResourceSpans) != 1 || len(payload.ResourceSpans[0].ScopeSpans) != 1 || len(payload.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
		t.Fatalf("unexpected payload shape: %s", bodies[0])
	}
	attrs := payload.ResourceSpans[0].Resource.Attributes
	if len(attrs) != 1 || attrs[0].Key != "service.name" || attrs[0].Value.StringValue != "catalog" {
		t.Fatalf("service.name missing: %s", bodies[0])
	}
	sp := payload.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.Name != "GET /ping" || len(sp.TraceID) != 32 || len(sp.SpanID) != 16 || sp.Status.Code != 1 {
		t.Fatalf("span field mismatch: %+v", sp)
	}
}

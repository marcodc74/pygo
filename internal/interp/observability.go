package interp

// Observability for HTTP services: Prometheus metrics (an http.metrics()
// middleware plus http.metrics_text()) and minimal OTLP/HTTP trace export
// (an http.tracing() middleware).

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// durBounds are the histogram bucket upper bounds, in seconds.
var durBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// durBoundLabels are the matching `le` label values, precomputed so the
// exposition output is byte-for-byte stable.
var durBoundLabels = []string{"0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10"}

// httpMetrics accumulates request counters, a latency histogram and an
// in-flight gauge. One registry per Interp, shared by every thread.
type httpMetrics struct {
	mu        sync.Mutex
	byStatus  map[string]int64 // "METHOD\x00STATUS" -> count
	durSum    float64
	durCount  int64
	buckets   []int64 // len(durBounds)+1, cumulative, last is +Inf
	inFlight  int64
	maxFlight int64
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{byStatus: map[string]int64{}, buckets: make([]int64, len(durBounds)+1)}
}

func (m *httpMetrics) enter() {
	m.mu.Lock()
	m.inFlight++
	if m.inFlight > m.maxFlight {
		m.maxFlight = m.inFlight
	}
	m.mu.Unlock()
}

func (m *httpMetrics) observe(method string, status int64, d time.Duration) {
	secs := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight > 0 {
		m.inFlight--
	}
	m.byStatus[method+"\x00"+strconv.FormatInt(status, 10)]++
	m.durSum += secs
	m.durCount++
	for i, b := range durBounds {
		if secs <= b {
			m.buckets[i]++
		}
	}
	m.buckets[len(durBounds)]++
}

// metricsText renders the registry in the Prometheus text exposition format.
func (in *Interp) metricsText() string { return metricsText(in.metrics) }

func metricsText(m *httpMetrics) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP pygo_http_requests_total HTTP requests handled, by method and status.\n")
	b.WriteString("# TYPE pygo_http_requests_total counter\n")
	keys := make([]string, 0, len(m.byStatus))
	for k := range m.byStatus {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		method, status, _ := strings.Cut(k, "\x00")
		fmt.Fprintf(&b, "pygo_http_requests_total{method=%q,status=%q} %d\n", method, status, m.byStatus[k])
	}
	b.WriteString("# HELP pygo_http_requests_in_flight In-flight HTTP requests.\n")
	b.WriteString("# TYPE pygo_http_requests_in_flight gauge\n")
	fmt.Fprintf(&b, "pygo_http_requests_in_flight %d\n", m.inFlight)
	b.WriteString("# HELP pygo_http_requests_max_in_flight Peak concurrent HTTP requests.\n")
	b.WriteString("# TYPE pygo_http_requests_max_in_flight gauge\n")
	fmt.Fprintf(&b, "pygo_http_requests_max_in_flight %d\n", m.maxFlight)
	b.WriteString("# HELP pygo_http_request_duration_seconds HTTP request latency in seconds.\n")
	b.WriteString("# TYPE pygo_http_request_duration_seconds histogram\n")
	for i, le := range durBoundLabels {
		fmt.Fprintf(&b, "pygo_http_request_duration_seconds_bucket{le=%q} %d\n", le, m.buckets[i])
	}
	fmt.Fprintf(&b, "pygo_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.buckets[len(durBounds)])
	fmt.Fprintf(&b, "pygo_http_request_duration_seconds_sum %s\n", strconv.FormatFloat(m.durSum, 'g', -1, 64))
	fmt.Fprintf(&b, "pygo_http_request_duration_seconds_count %d\n", m.durCount)
	return b.String()
}

// ---------- middleware ----------

// mwMetrics records request count, latency and in-flight gauge into the
// interpreter registry (rendered by http.metrics_text()).
func (th *Thread) mwMetrics() *Struct {
	return th.httpMiddleware("metrics", func(th *Thread, next Value, req *Struct) (Value, error) {
		m := th.in.metrics
		start := time.Now()
		m.enter()
		v, err := th.call(next, req)
		status := int64(500)
		if err == nil {
			if resp, ok := v.(*Struct); ok {
				status, _ = field(resp, "status").(int64)
			}
		}
		m.observe(Str(field(req, "method")), status, time.Since(start))
		return v, err
	})
}

// newSpanIDs returns a 16-byte trace id and an 8-byte span id, both hex, from
// the interpreter's trace generator (reproducible under the seed).
func (in *Interp) newSpanIDs() (string, string) {
	in.traceMu.Lock()
	defer in.traceMu.Unlock()
	var t [16]byte
	var s [8]byte
	for i := range t {
		t[i] = byte(in.traceRng.Intn(256))
	}
	for i := range s {
		s[i] = byte(in.traceRng.Intn(256))
	}
	return hex.EncodeToString(t[:]), hex.EncodeToString(s[:])
}

// mwTracing exports one OTLP/HTTP span per request.
func (th *Thread) mwTracing(service, endpoint string) (Value, error) {
	if _, ok := otlpURL(endpoint); !ok {
		return nil, perr(PArgs, `pass an http(s) base URL, e.g. http.tracing("svc", endpoint: "http://localhost:4318")`,
			"http.tracing: invalid endpoint %q", endpoint)
	}
	return th.httpMiddleware("tracing", func(th *Thread, next Value, req *Struct) (Value, error) {
		traceID, spanID := th.in.newSpanIDs()
		start := time.Now()
		v, err := th.call(next, req)
		status := int64(500)
		if err == nil {
			if resp, ok := v.(*Struct); ok {
				status, _ = field(resp, "status").(int64)
			}
		}
		th.exportSpan(service, endpoint, traceID, spanID, Str(field(req, "method")), Str(field(req, "path")), status, start, time.Now(), err != nil)
		return v, err
	}), nil
}

// otlpURL turns a base endpoint into the OTLP/HTTP traces URL. It accepts a
// full .../v1/traces URL too, and reports whether the endpoint is usable.
func otlpURL(endpoint string) (string, bool) {
	e := strings.TrimRight(endpoint, "/")
	if e == "" || (!strings.HasPrefix(e, "http://") && !strings.HasPrefix(e, "https://")) {
		return "", false
	}
	if strings.HasSuffix(e, "/v1/traces") {
		return e, true
	}
	return e + "/v1/traces", true
}

// exportSpan POSTs one span to endpoint as OTLP/HTTP JSON. Export failures are
// logged and never break the request.
func (th *Thread) exportSpan(service, endpoint, traceID, spanID, method, path string, status int64, start, end time.Time, failed bool) {
	url, ok := otlpURL(endpoint)
	if !ok {
		return
	}
	body := otlpPayload(service, traceID, spanID, method, path, status, start, end, failed)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		th.serverLog("warn", "otlp export failed", map[string]string{"endpoint": url, "error": err.Error()})
		return
	}
	req.Header.Set("content-type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		th.serverLog("warn", "otlp export failed", map[string]string{"endpoint": url, "error": err.Error()})
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		th.serverLog("warn", "otlp export rejected", map[string]string{"endpoint": url, "status": strconv.Itoa(resp.StatusCode)})
	}
}

type otlpAttrValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
}

type otlpAttr struct {
	Key   string        `json:"key"`
	Value otlpAttrValue `json:"value"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpAttr `json:"attributes"`
	Status            otlpStatus `json:"status"`
}

// otlpPayload builds an OTLP/HTTP JSON trace payload with a single span.
func otlpPayload(service, traceID, spanID, method, path string, status int64, start, end time.Time, failed bool) []byte {
	code := 1 // STATUS_CODE_OK
	if failed || status >= 500 {
		code = 2 // STATUS_CODE_ERROR
	}
	payload := map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{
				"attributes": []otlpAttr{{Key: "service.name", Value: otlpAttrValue{StringValue: service}}},
			},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "pygo"},
				"spans": []otlpSpan{{
					TraceID:           traceID,
					SpanID:            spanID,
					Name:              method + " " + path,
					Kind:              2, // SPAN_KIND_SERVER
					StartTimeUnixNano: strconv.FormatInt(start.UnixNano(), 10),
					EndTimeUnixNano:   strconv.FormatInt(end.UnixNano(), 10),
					Attributes: []otlpAttr{
						{Key: "http.request.method", Value: otlpAttrValue{StringValue: method}},
						{Key: "url.path", Value: otlpAttrValue{StringValue: path}},
						{Key: "http.response.status_code", Value: otlpAttrValue{IntValue: strconv.FormatInt(status, 10)}},
					},
					Status: otlpStatus{Code: code},
				}},
			}},
		}},
	}
	b, _ := json.Marshal(payload)
	return b
}

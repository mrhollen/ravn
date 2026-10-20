package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	httpapi "github.com/mrhollen/ravn/internal/interface/http"
)

func TestMetricsMiddleware_CountsRequestsAndStatuses(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /created", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	})
	mux.HandleFunc("GET /error", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	})

	handler := metrics.Middleware(mux)

	// Make 2 GET /ok
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}

	// Make 1 POST /created
	{
		req := httptest.NewRequest(http.MethodPost, "/created", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d", rec.Code)
		}
	}

	// Make 1 GET /error
	{
		req := httptest.NewRequest(http.MethodGet, "/error", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	}

	// Make 1 GET /notfound (unhandled route -> 404 from mux)
	{
		req := httptest.NewRequest(http.MethodGet, "/notfound", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	}

	snap := metrics.Snapshot(context.Background())

	if snap.HTTPRequestsTotal != 5 {
		t.Errorf("expected 5 total requests, got %d", snap.HTTPRequestsTotal)
	}
	if snap.HTTPRequestsInFlight != 0 {
		t.Errorf("expected 0 in-flight requests, got %d", snap.HTTPRequestsInFlight)
	}
	if snap.HTTPRequestsByStatus["200"] != 2 {
		t.Errorf("expected 2 status 200, got %d", snap.HTTPRequestsByStatus["200"])
	}
	if snap.HTTPRequestsByStatus["201"] != 1 {
		t.Errorf("expected 1 status 201, got %d", snap.HTTPRequestsByStatus["201"])
	}
	if snap.HTTPRequestsByStatus["500"] != 1 {
		t.Errorf("expected 1 status 500, got %d", snap.HTTPRequestsByStatus["500"])
	}
	if snap.HTTPRequestsByStatus["404"] != 1 {
		t.Errorf("expected 1 status 404, got %d", snap.HTTPRequestsByStatus["404"])
	}
	if snap.HTTPRequestsByMethod["GET"] != 4 {
		t.Errorf("expected 4 GET, got %d", snap.HTTPRequestsByMethod["GET"])
	}
	if snap.HTTPRequestsByMethod["POST"] != 1 {
		t.Errorf("expected 1 POST, got %d", snap.HTTPRequestsByMethod["POST"])
	}
	if snap.HTTPRequestDurationSecondsTotal <= 0 {
		t.Errorf("expected positive total duration, got %f", snap.HTTPRequestDurationSecondsTotal)
	}
	if snap.UptimeSeconds <= 0 {
		t.Errorf("expected positive uptime, got %f", snap.UptimeSeconds)
	}
}

func TestMetricsHandler_PrometheusFormat(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	handler := metrics.Handler()

	// Seed some metrics
	mw := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test", nil))

	// Request /metrics with text/plain
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Accept", "text/plain")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "# HELP http_requests_total") {
		t.Errorf("body missing http_requests_total HELP: %s", body)
	}
	if !strings.Contains(body, "http_requests_total 1") {
		t.Errorf("body missing http_requests_total 1: %s", body)
	}
	if !strings.Contains(body, `http_requests_by_status{code="200"} 1`) {
		t.Errorf("body missing status 200 counter: %s", body)
	}
	if !strings.Contains(body, `http_requests_by_method{method="GET"} 1`) {
		t.Errorf("body missing method GET counter: %s", body)
	}
	if !strings.Contains(body, "# HELP process_uptime_seconds") {
		t.Errorf("body missing process_uptime_seconds HELP: %s", body)
	}
}

func TestMetricsHandler_JSONFormat(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	handler := metrics.Handler()

	mw := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mw.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test", nil))

	// Request with ?format=json
	req := httptest.NewRequest("GET", "/metrics?format=json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}

	var snap httpapi.MetricsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal json: %v, raw: %s", err, rec.Body.String())
	}

	if snap.HTTPRequestsTotal != 1 {
		t.Errorf("expected 1 request, got %d", snap.HTTPRequestsTotal)
	}
	if snap.HTTPRequestsByStatus["200"] != 1 {
		t.Errorf("expected status 200 count 1, got %d", snap.HTTPRequestsByStatus["200"])
	}
	if snap.HTTPRequestsByMethod["GET"] != 1 {
		t.Errorf("expected method GET count 1, got %d", snap.HTTPRequestsByMethod["GET"])
	}
}

func TestMetricsHandler_WithTaskStats(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	metrics.SetTaskStatsFunc(func(ctx context.Context) (map[string]int64, int64, error) {
		return map[string]int64{
			"todo":  5,
			"doing": 2,
			"done":  10,
		}, 2, nil
	})

	// 1. Check Prometheus output
	req := httptest.NewRequest("GET", "/metrics?format=prometheus", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `tasks_by_status{status="todo"} 5`) {
		t.Errorf("missing tasks_by_status todo: %s", body)
	}
	if !strings.Contains(body, `tasks_by_status{status="doing"} 2`) {
		t.Errorf("missing tasks_by_status doing: %s", body)
	}
	if !strings.Contains(body, `tasks_by_status{status="done"} 10`) {
		t.Errorf("missing tasks_by_status done: %s", body)
	}
	if !strings.Contains(body, `active_claims 2`) {
		t.Errorf("missing active_claims: %s", body)
	}

	// 2. Check JSON output
	reqJSON := httptest.NewRequest("GET", "/api/v1/metrics", nil)
	recJSON := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recJSON, reqJSON)

	var snap httpapi.MetricsSnapshot
	if err := json.Unmarshal(recJSON.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if snap.TasksByStatus["todo"] != 5 {
		t.Errorf("expected 5 todo tasks, got %d", snap.TasksByStatus["todo"])
	}
	if snap.ActiveClaims == nil || *snap.ActiveClaims != 2 {
		t.Errorf("expected 2 active claims, got %v", snap.ActiveClaims)
	}
}

// TestMetricsMiddleware_UnknownMethodFoldsToOther verifies that non-standard HTTP
// methods map to the fixed "OTHER" counter and never create their own map key.
func TestMetricsMiddleware_UnknownMethodFoldsToOther(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := metrics.Middleware(inner)

	for _, method := range []string{"GET", "POST", "CUSTOM", "PUT", "TRACE", "FETCH_123"} {
		req := httptest.NewRequest(method, "/x", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := metrics.Snapshot(context.Background())

	for _, standard := range []string{"GET", "POST", "PUT"} {
		if got := snap.HTTPRequestsByMethod[standard]; got != 1 {
			t.Errorf("expected 1 %s, got %d", standard, got)
		}
	}
	// CUSTOM, TRACE and FETCH_123 are all outside the tracked verb set.
	if got := snap.HTTPRequestsByMethod["OTHER"]; got != 3 {
		t.Errorf("expected 3 OTHER, got %d", got)
	}
	for _, forbidden := range []string{"CUSTOM", "TRACE", "FETCH_123"} {
		if _, ok := snap.HTTPRequestsByMethod[forbidden]; ok {
			t.Errorf("unexpected unbounded method key %q in snapshot: %+v", forbidden, snap.HTTPRequestsByMethod)
		}
	}
}

// TestMetrics_BoundedCardinalityUnderFlooding simulates an attacker sending a large
// number of distinct arbitrary method names and asserts the method cardinality stays
// bounded (only OTHER accumulates) so memory cannot be exhausted.
func TestMetrics_BoundedCardinalityUnderFlooding(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := metrics.Middleware(inner)

	const n = 5000
	for i := 0; i < n; i++ {
		method := "MALICIOUS_METHOD_" + strconv.Itoa(i) // distinct name per request
		req := httptest.NewRequest(method, "/flood", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := metrics.Snapshot(context.Background())

	if got := snap.HTTPRequestsByMethod["OTHER"]; got != n {
		t.Errorf("expected all %d malicious methods folded into OTHER, got %d", n, got)
	}
	const maxExpected = 8 // 7 tracked verbs + OTHER
	if len(snap.HTTPRequestsByMethod) > maxExpected {
		t.Errorf("method cardinality unbounded: %d keys > %d allowed; got %+v",
			len(snap.HTTPRequestsByMethod), maxExpected, snap.HTTPRequestsByMethod)
	}
}

// TestMetricsHandler_EscapesPrometheusLabelValues verifies that a label value containing
// a double quote, a backslash and a newline is escaped so it cannot break the exposition
// format or inject fake metric lines.
func TestMetricsHandler_EscapesPrometheusLabelValues(t *testing.T) {
	t.Parallel()
	metrics := httpapi.NewMetrics()
	evil := "quote\" here\nFAKE_METRIC_12345 999" // contains a quote and a real newline
	metrics.SetTaskStatsFunc(func(ctx context.Context) (map[string]int64, int64, error) {
		return map[string]int64{evil: 7}, 0, nil
	})

	req := httptest.NewRequest("GET", "/metrics?format=prometheus", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()

	if !strings.Contains(body, `tasks_by_status{status="quote\" here\nFAKE_METRIC_12345 999"} 7`) {
		t.Errorf("expected fully escaped label line, got:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "FAKE_METRIC_12345") {
			t.Fatalf("metric injection: newline not escaped; a fake metric line was produced:\n%s", body)
		}
	}
}

func BenchmarkMetricsMiddleware(b *testing.B) {
	metrics := httpapi.NewMetrics()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := metrics.Middleware(inner)

	req := httptest.NewRequest("GET", "/bench", nil)
	rec := httptest.NewRecorder()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handler.ServeHTTP(rec, req)
	}
}

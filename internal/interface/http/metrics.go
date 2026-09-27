package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatsFunc is an optional callback that returns task counts by status and active claim count.
type TaskStatsFunc func(ctx context.Context) (tasksByStatus map[string]int64, activeClaims int64, err error)

// Metrics collects and serves in-memory observability metrics for HTTP requests and tasks.
// Emitting metrics uses lock-free atomic operations, introducing no observable latency.
type Metrics struct {
	startTime       time.Time
	totalRequests   atomic.Uint64
	inFlight        atomic.Int64
	totalDurationNs atomic.Uint64
	statusCodes     [600]atomic.Uint64
	otherStatus     atomic.Uint64

	mu            sync.RWMutex
	methods       map[string]*atomic.Uint64
	taskStatsFunc TaskStatsFunc
}

// NewMetrics initializes a fresh Metrics instance.
func NewMetrics() *Metrics {
	m := &Metrics{
		startTime: time.Now(),
		methods:   make(map[string]*atomic.Uint64),
	}
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions,
	} {
		m.methods[method] = new(atomic.Uint64)
	}
	return m
}

// SetTaskStatsFunc sets an optional callback for querying task counts and active claims.
func (m *Metrics) SetTaskStatsFunc(fn TaskStatsFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStatsFunc = fn
}

func (m *Metrics) recordStatus(status int) {
	if status >= 100 && status < 600 {
		m.statusCodes[status].Add(1)
	} else {
		m.otherStatus.Add(1)
	}
}

func (m *Metrics) recordMethod(method string) {
	m.mu.RLock()
	counter, ok := m.methods[method]
	m.mu.RUnlock()
	if ok {
		counter.Add(1)
		return
	}

	m.mu.Lock()
	counter, ok = m.methods[method]
	if !ok {
		counter = new(atomic.Uint64)
		m.methods[method] = counter
	}
	m.mu.Unlock()
	counter.Add(1)
}

// Middleware wraps an http.Handler to capture status code, method, duration, and in-flight count.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inFlight.Add(1)
		defer m.inFlight.Add(-1)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		duration := time.Since(start)
		m.totalRequests.Add(1)
		m.recordStatus(sw.status)
		m.recordMethod(r.Method)
		m.totalDurationNs.Add(uint64(duration))
	})
}

// statusWriter intercepts WriteHeader and Write to record the response HTTP status code.
// It implements http.Flusher and Unwrap for SSE and streaming compatibility.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.written = true
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// MetricsSnapshot is the structured representation of metrics at a point in time.
type MetricsSnapshot struct {
	HTTPRequestsTotal               uint64            `json:"http_requests_total"`
	HTTPRequestsInFlight            int64             `json:"http_requests_in_flight"`
	HTTPRequestsByStatus            map[string]uint64 `json:"http_requests_by_status"`
	HTTPRequestsByMethod            map[string]uint64 `json:"http_requests_by_method"`
	HTTPRequestDurationSecondsTotal float64           `json:"http_request_duration_seconds_total"`
	UptimeSeconds                   float64           `json:"uptime_seconds"`
	TasksByStatus                   map[string]int64  `json:"tasks_by_status,omitempty"`
	ActiveClaims                    *int64            `json:"active_claims,omitempty"`
}

// Snapshot returns the current metrics values.
func (m *Metrics) Snapshot(ctx context.Context) MetricsSnapshot {
	byStatus := make(map[string]uint64)
	for code := 100; code < 600; code++ {
		count := m.statusCodes[code].Load()
		if count > 0 {
			byStatus[strconv.Itoa(code)] = count
		}
	}
	if other := m.otherStatus.Load(); other > 0 {
		byStatus["other"] = other
	}

	byMethod := make(map[string]uint64)
	m.mu.RLock()
	for method, counter := range m.methods {
		count := counter.Load()
		if count > 0 {
			byMethod[method] = count
		}
	}
	fn := m.taskStatsFunc
	m.mu.RUnlock()

	durationSec := float64(m.totalDurationNs.Load()) / 1e9
	uptimeSec := time.Since(m.startTime).Seconds()

	snap := MetricsSnapshot{
		HTTPRequestsTotal:               m.totalRequests.Load(),
		HTTPRequestsInFlight:            m.inFlight.Load(),
		HTTPRequestsByStatus:            byStatus,
		HTTPRequestsByMethod:            byMethod,
		HTTPRequestDurationSecondsTotal: durationSec,
		UptimeSeconds:                   uptimeSec,
	}

	if fn != nil {
		tasksByStatus, activeClaims, err := fn(ctx)
		if err == nil {
			snap.TasksByStatus = tasksByStatus
			claims := activeClaims
			snap.ActiveClaims = &claims
		}
	}

	return snap
}

// Handler returns an http.Handler that renders metrics based on Accept header or ?format= query param.
// When format=prometheus or Accept contains text/plain, it formats as Prometheus text exposition.
// When format=json or Accept contains application/json, it outputs JSON.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := m.Snapshot(r.Context())

		format := strings.ToLower(r.URL.Query().Get("format"))
		accept := strings.ToLower(r.Header.Get("Accept"))

		wantJSON := false
		if format == "json" {
			wantJSON = true
		} else if format == "prometheus" || format == "text" {
			wantJSON = false
		} else if strings.Contains(accept, "application/json") {
			wantJSON = true
		} else if strings.Contains(accept, "text/plain") || strings.Contains(accept, "application/openmetrics-text") {
			wantJSON = false
		} else if strings.HasPrefix(r.URL.Path, "/api") {
			wantJSON = true
		} else {
			wantJSON = false
		}

		if wantJSON {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(snap)
			return
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		var sb strings.Builder
		sb.WriteString("# HELP http_requests_total Total number of HTTP requests processed.\n")
		sb.WriteString("# TYPE http_requests_total counter\n")
		sb.WriteString(fmt.Sprintf("http_requests_total %d\n\n", snap.HTTPRequestsTotal))

		sb.WriteString("# HELP http_requests_in_flight Current number of HTTP requests being processed.\n")
		sb.WriteString("# TYPE http_requests_in_flight gauge\n")
		sb.WriteString(fmt.Sprintf("http_requests_in_flight %d\n\n", snap.HTTPRequestsInFlight))

		sb.WriteString("# HELP http_requests_by_status Total number of HTTP requests partitioned by status code.\n")
		sb.WriteString("# TYPE http_requests_by_status counter\n")
		statusKeys := make([]string, 0, len(snap.HTTPRequestsByStatus))
		for k := range snap.HTTPRequestsByStatus {
			statusKeys = append(statusKeys, k)
		}
		sort.Strings(statusKeys)
		for _, k := range statusKeys {
			sb.WriteString(fmt.Sprintf("http_requests_by_status{code=\"%s\"} %d\n", k, snap.HTTPRequestsByStatus[k]))
		}
		sb.WriteString("\n")

		sb.WriteString("# HELP http_requests_by_method Total number of HTTP requests partitioned by HTTP method.\n")
		sb.WriteString("# TYPE http_requests_by_method counter\n")
		methodKeys := make([]string, 0, len(snap.HTTPRequestsByMethod))
		for k := range snap.HTTPRequestsByMethod {
			methodKeys = append(methodKeys, k)
		}
		sort.Strings(methodKeys)
		for _, k := range methodKeys {
			sb.WriteString(fmt.Sprintf("http_requests_by_method{method=\"%s\"} %d\n", k, snap.HTTPRequestsByMethod[k]))
		}
		sb.WriteString("\n")

		sb.WriteString("# HELP http_request_duration_seconds_total Total duration of HTTP requests in seconds.\n")
		sb.WriteString("# TYPE http_request_duration_seconds_total counter\n")
		sb.WriteString(fmt.Sprintf("http_request_duration_seconds_total %.6f\n\n", snap.HTTPRequestDurationSecondsTotal))

		sb.WriteString("# HELP process_uptime_seconds Total process uptime in seconds.\n")
		sb.WriteString("# TYPE process_uptime_seconds gauge\n")
		sb.WriteString(fmt.Sprintf("process_uptime_seconds %.2f\n\n", snap.UptimeSeconds))

		if snap.TasksByStatus != nil {
			sb.WriteString("# HELP tasks_by_status Total number of tasks partitioned by status.\n")
			sb.WriteString("# TYPE tasks_by_status gauge\n")
			taskStatusKeys := make([]string, 0, len(snap.TasksByStatus))
			for k := range snap.TasksByStatus {
				taskStatusKeys = append(taskStatusKeys, k)
			}
			sort.Strings(taskStatusKeys)
			for _, k := range taskStatusKeys {
				sb.WriteString(fmt.Sprintf("tasks_by_status{status=\"%s\"} %d\n", k, snap.TasksByStatus[k]))
			}
			sb.WriteString("\n")
		}

		if snap.ActiveClaims != nil {
			sb.WriteString("# HELP active_claims Current number of active task claims.\n")
			sb.WriteString("# TYPE active_claims gauge\n")
			sb.WriteString(fmt.Sprintf("active_claims %d\n", *snap.ActiveClaims))
		}

		w.Write([]byte(sb.String())) //nolint:errcheck
	})
}

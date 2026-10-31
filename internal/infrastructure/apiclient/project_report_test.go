package apiclient

import (
	"context"
	"net/http"
	"testing"
)

// project_report_test.go: unit tests for Client.ProjectReport over an httptest
// upstream. It pins the query-param building (stalled_minutes / since are only
// sent when meaningful — presence-based, like ListTasks) and the GET path, plus
// the success decode and the non-2xx → *APIError surface.

const reportBody = `{
  "project_id": "PRJ1",
  "counts": {
    "by_status":   { "todo": 3, "doing": 1, "done": 5 },
    "by_priority": { "0": 0, "1": 1, "2": 0, "3": 1, "4": 0, "5": 1 }
  },
  "active_claims":       [],
  "expired_claims":      [],
  "stalled_tasks":       [],
  "bottlenecks":         [],
  "open_blocked_by":     [],
  "recently_completed":  [],
  "workload":            { "ada": 2, "": 1 }
}`

func TestProjectReport_SendsParamsAndDecodes(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodGet || cap.path != "/api/v1/projects/PRJ1/report" {
			t.Errorf("got %s %s, want GET /api/v1/projects/PRJ1/report", cap.method, cap.path)
		}
		want := map[string]string{"stalled_minutes": "120", "since": "2026-10-09T00:00:00Z"}
		for k, v := range want {
			if cap.query[k] != v {
				t.Errorf("query %s = %q, want %q (all: %v)", k, cap.query[k], v, cap.query)
			}
		}
		writeJSONTest(w, http.StatusOK, reportBody)
	}))

	rep, err := c.ProjectReport(context.Background(), "PRJ1", 120, "2026-10-09T00:00:00Z")
	if err != nil {
		t.Fatalf("ProjectReport: %v", err)
	}
	if rep.ProjectID != "PRJ1" {
		t.Errorf("rep.ProjectID = %q, want PRJ1", rep.ProjectID)
	}
	if got := rep.Counts.ByStatus["done"]; got != 5 {
		t.Errorf("counts.by_status[done] = %d, want 5 (all: %v)", got, rep.Counts.ByStatus)
	}
	if got := rep.Workload["ada"]; got != 2 {
		t.Errorf("workload[ada] = %d, want 2", got)
	}
}

// OmittedParamsWhenNotMeaningful — table: a non-positive stalled window and an
// empty since must BOTH send no query params (orNil turns the empty url.Values
// into nil so the request has no "?" at all).
func TestProjectReport_OmitsParamsWhenNotMeaningful(t *testing.T) {
	cases := []struct {
		name    string
		stalled int
		since   string
	}{
		{name: "ZeroStalled_EmptySince", stalled: 0, since: ""},
		{name: "NegativeStalled_Omitted", stalled: -5, since: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
				if len(cap.query) != 0 {
					t.Errorf("query = %v, want empty (stalled=%d, since=%q)", cap.query, tc.stalled, tc.since)
				}
				writeJSONTest(w, http.StatusOK, reportBody)
			}))
			if _, err := c.ProjectReport(context.Background(), "PRJ1", tc.stalled, tc.since); err != nil {
				t.Fatalf("ProjectReport: %v", err)
			}
		})
	}
}

func TestProjectReport_NonOKSurfacesAPIError(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		writeJSONTest(w, http.StatusNotFound, `{"error":"not found","resource":"project","id":"PRJ1","message":"project not found"}`)
	}))
	if _, err := c.ProjectReport(context.Background(), "NOPE", 0, ""); err == nil {
		t.Fatal("ProjectReport: expected error for non-2xx, got nil")
	} else if e := wantAPIError(t, err, http.StatusNotFound); e != nil && e.Op != "ProjectReport" {
		t.Errorf("APIError.Op = %q, want ProjectReport", e.Op)
	}
}

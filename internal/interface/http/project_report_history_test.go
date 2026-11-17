package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// project_report_history_test.go: end-to-end HTTP wire tests for
// GET /api/v1/projects/{id}/report/history over full production wiring
// (real SQLite, real use cases, injected clock).

func TestProjectReportHistory_EndToEnd(t *testing.T) {
	// Friday 2026-09-25T18:00:00Z; ISO week Monday is 2026-09-21T00:00:00Z.
	day1 := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) // Fri prev week (Mon 2026-09-14)
	day2 := time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC) // Wed current week (Mon 2026-09-21)
	day3 := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC) // Fri current week

	fc := &fakeClock{now: day1}
	stk := newStackWithClock(t, fc)
	p := createProject(t, stk, "HistoryE2E")

	// Day 1 (2026-09-18): create 2 tasks; complete t1 same day.
	t1 := makeTask(t, stk, p.ID, "Task 1", nil)
	_ = makeTask(t, stk, p.ID, "Task 2", nil)
	patchTask(t, stk, t1.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, t1.ID, 2, map[string]any{"status": "done"})

	// Day 2 (2026-09-23): create t3; move t3 to done then reopen to todo (completed_at clears).
	fc.now = day2
	t3 := makeTask(t, stk, p.ID, "Task 3", nil)
	patchTask(t, stk, t3.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, t3.ID, 2, map[string]any{"status": "done"})
	patchTask(t, stk, t3.ID, 3, map[string]any{"status": "todo"}) // reopened -> completed_at cleared

	// Day 3 (2026-09-25): create t4 (done) and t5 (archived -> excluded).
	fc.now = day3
	t4 := makeTask(t, stk, p.ID, "Task 4", nil)
	patchTask(t, stk, t4.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, t4.ID, 2, map[string]any{"status": "done"})

	t5 := makeTask(t, stk, p.ID, "Task 5 Archived", nil)
	patchTask(t, stk, t5.ID, 1, map[string]any{"archived": true})

	// 1. Default query (days=30, bucket=day): verify exact JSON snake_case keys and counts.
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report/history", nil)
	if code != http.StatusOK {
		t.Fatalf("GET default report/history = %d, want 200 (body %s)", code, string(body))
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal raw JSON: %v", err)
	}
	for _, key := range []string{"project_id", "days", "bucket", "series"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("top-level JSON missing key %q: %s", key, string(body))
		}
	}

	resp := mustDecode[projectReportHistoryDTO](t, body)
	if resp.ProjectID != p.ID || resp.Days != 30 || resp.Bucket != "day" {
		t.Fatalf("header = %+v, want project=%s days=30 bucket=day", resp, p.ID)
	}
	if len(resp.Series) != 30 {
		t.Fatalf("len(series) = %d, want 30", len(resp.Series))
	}

	byStart := map[string]reportHistoryBucketDTO{}
	for _, b := range resp.Series {
		byStart[b.BucketStart] = b
	}
	wantDays := map[string]reportHistoryBucketDTO{
		"2026-09-18T00:00:00Z": {BucketStart: "2026-09-18T00:00:00Z", Opened: 2, Completed: 1, CumulativeCompleted: 1},
		"2026-09-23T00:00:00Z": {BucketStart: "2026-09-23T00:00:00Z", Opened: 1, Completed: 0, CumulativeCompleted: 1},
		"2026-09-25T00:00:00Z": {BucketStart: "2026-09-25T00:00:00Z", Opened: 1, Completed: 1, CumulativeCompleted: 2},
	}
	for start, want := range wantDays {
		got, ok := byStart[start]
		if !ok {
			t.Errorf("missing bucket %s", start)
			continue
		}
		if got != want {
			t.Errorf("bucket %s = %+v, want %+v", start, got, want)
		}
	}

	// 2. Weekly buckets over 14 days (cutoff 2026-09-12 -> week Mon 2026-09-07 .. Mon 2026-09-21).
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report/history?days=14&bucket=week", nil)
	if code != http.StatusOK {
		t.Fatalf("GET weekly report/history = %d, want 200 (body %s)", code, string(body))
	}
	weekly := mustDecode[projectReportHistoryDTO](t, body)
	if weekly.Days != 14 || weekly.Bucket != "week" {
		t.Fatalf("weekly header = %+v, want days=14 bucket=week", weekly)
	}
	wantWeeks := []reportHistoryBucketDTO{
		{BucketStart: "2026-09-07T00:00:00Z", Opened: 0, Completed: 0, CumulativeCompleted: 0},
		{BucketStart: "2026-09-14T00:00:00Z", Opened: 2, Completed: 1, CumulativeCompleted: 1},
		{BucketStart: "2026-09-21T00:00:00Z", Opened: 2, Completed: 1, CumulativeCompleted: 2},
	}
	if len(weekly.Series) != len(wantWeeks) {
		t.Fatalf("weekly len(series) = %d, want %d (%+v)", len(weekly.Series), len(wantWeeks), weekly.Series)
	}
	for i, want := range wantWeeks {
		if weekly.Series[i] != want {
			t.Errorf("weekly.series[%d] = %+v, want %+v", i, weekly.Series[i], want)
		}
	}

	// 3. Days > 90 clamps silently to 90 (no error).
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report/history?days=180", nil)
	if code != http.StatusOK {
		t.Fatalf("GET days=180 = %d, want 200 (body %s)", code, string(body))
	}
	clamped := mustDecode[projectReportHistoryDTO](t, body)
	if clamped.Days != 90 || len(clamped.Series) != 90 {
		t.Errorf("clamped = (days=%d len=%d), want (90, 90)", clamped.Days, len(clamped.Series))
	}

	// 4. Unknown project -> 404 not_found (errors.go shape).
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/01A2B3C4D5E6F7G8H9J0K1M2N3/report/history", nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != ErrCodeNotFound || e.Resource != "project" {
		t.Errorf("404 body = %+v, want error=%q resource=%q", e, ErrCodeNotFound, "project")
	}

	// 5. Invalid params -> 400 invalid_input.
	for _, q := range []string{"?days=0", "?days=-5", "?days=abc", "?bucket=month", "?bucket=hour"} {
		code, b := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report/history"+q, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("GET report/history%s = %d, want 400 (body %s)", q, code, string(b))
		}
		errBody := mustDecode[errResp](t, b)
		if errBody.Error != ErrCodeInvalidInput {
			t.Errorf("GET report/history%s error = %+v, want %q", q, errBody, ErrCodeInvalidInput)
		}
	}
}

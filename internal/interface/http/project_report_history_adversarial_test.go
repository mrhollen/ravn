package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAdversarial_ProjectReportHistory_InputsAndBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	fc := &fakeClock{now: now}
	stk := newStackWithClock(t, fc)

	emptyProj := createProject(t, stk, "EmptyHistory")
	seededProj := createProject(t, stk, "BoundaryHistory")
	_ = makeTask(t, stk, seededProj.ID, "Seeded Task", nil)

	t.Run("empty project returns 200 with non-null empty series array", func(t *testing.T) {
		for _, q := range []string{"", "?days=7&bucket=day", "?days=30&bucket=week", "?days=999"} {
			code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+emptyProj.ID+"/report/history"+q, nil)
			if code != http.StatusOK {
				t.Fatalf("GET empty project%s = %d, want 200 (body %s)", q, code, string(body))
			}
			raw := string(body)
			if !strings.Contains(raw, `"series":[]`) {
				t.Errorf("GET empty project%s raw JSON missing %q: %s", q, `"series":[]`, raw)
			}
			if strings.Contains(raw, `"series":null`) {
				t.Errorf("GET empty project%s returned null series: %s", q, raw)
			}
			dto := mustDecode[projectReportHistoryDTO](t, body)
			if dto.Series == nil || len(dto.Series) != 0 {
				t.Errorf("GET empty project%s decoded series = %#v, want empty non-nil slice", q, dto.Series)
			}
		}
	})

	t.Run("hostile query parameters return 400 invalid_input", func(t *testing.T) {
		cases := []struct {
			name  string
			query string
		}{
			{"days zero", "?days=0"},
			{"days negative", "?days=-1"},
			{"days large negative", "?days=-999999"},
			{"days alpha", "?days=thirty"},
			{"days float", "?days=7.5"},
			{"days NaN", "?days=NaN"},
			{"days whitespace", "?days=%207%20"},
			{"days int overflow", "?days=99999999999999999999999999"},
			{"days sql injection", "?days=7%20OR%201=1"},
			{"bucket hour", "?bucket=hour"},
			{"bucket month", "?bucket=month"},
			{"bucket year", "?bucket=year"},
			{"bucket uppercase DAY", "?bucket=DAY"},
			{"bucket titlecase Week", "?bucket=Week"},
			{"bucket trailing space", "?bucket=day%20"},
			{"bucket injection", "?bucket=day%3Bdrop"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+seededProj.ID+"/report/history"+tc.query, nil)
				if code != http.StatusBadRequest {
					t.Fatalf("GET %s = %d, want 400 (body %s)", tc.query, code, string(body))
				}
				e := mustDecode[errResp](t, body)
				if e.Error != ErrCodeInvalidInput {
					t.Errorf("GET %s error = %q, want %q", tc.query, e.Error, ErrCodeInvalidInput)
				}
			})
		}
	})

	t.Run("malformed path project id returns 400 invalid_input", func(t *testing.T) {
		for _, badID := range []string{"not-a-ulid", "123", "01A2B3C4D5E6F7G8H9J0K1M2N!"} {
			code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+badID+"/report/history", nil)
			if code != http.StatusBadRequest {
				t.Fatalf("GET bad id %q = %d, want 400 (body %s)", badID, code, string(body))
			}
			e := mustDecode[errResp](t, body)
			if e.Error != ErrCodeInvalidInput {
				t.Errorf("GET bad id %q error = %q, want %q", badID, e.Error, ErrCodeInvalidInput)
			}
		}
	})

	t.Run("days clamping boundary 1, 90, 91, huge", func(t *testing.T) {
		cases := []struct {
			query    string
			wantDays int
		}{
			{"?days=1", 1},
			{"?days=90", 90},
			{"?days=91", 90},
			{"?days=1000000", 90},
		}
		for _, tc := range cases {
			code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+seededProj.ID+"/report/history"+tc.query, nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200 (body %s)", tc.query, code, string(body))
			}
			dto := mustDecode[projectReportHistoryDTO](t, body)
			if dto.Days != tc.wantDays || len(dto.Series) != tc.wantDays {
				t.Errorf("GET %s = (days=%d len=%d), want (%d, %d)", tc.query, dto.Days, len(dto.Series), tc.wantDays, tc.wantDays)
			}
		}
	})
}

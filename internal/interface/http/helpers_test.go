package httpapi

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestParseTimeParam(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		paramName string
		wantUTC   time.Time
		wantErr   bool
	}{
		{
			name:      "empty string returns nil",
			input:     "",
			paramName: "due_at",
			wantUTC:   time.Time{},
			wantErr:   false,
		},
		{
			name:      "standard RFC3339 UTC",
			input:     "2026-09-25T18:00:00Z",
			paramName: "due_at",
			wantUTC:   time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC),
			wantErr:   false,
		},
		{
			name:      "standard RFC3339 with timezone offset",
			input:     "2026-09-25T20:00:00+02:00",
			paramName: "due_at",
			wantUTC:   time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC),
			wantErr:   false,
		},
		{
			name:      "ISO8601 with milliseconds (JS Date.toISOString)",
			input:     "2026-09-28T18:30:00.123Z",
			paramName: "due_at",
			wantUTC:   time.Date(2026, 9, 28, 18, 30, 0, 123000000, time.UTC),
			wantErr:   false,
		},
		{
			name:      "ISO8601 with milliseconds and timezone offset",
			input:     "2026-09-28T20:30:00.123+02:00",
			paramName: "due_at",
			wantUTC:   time.Date(2026, 9, 28, 18, 30, 0, 123000000, time.UTC),
			wantErr:   false,
		},
		{
			name:      "RFC3339Nano with nanoseconds",
			input:     "2026-09-28T18:30:00.123456789Z",
			paramName: "due_before",
			wantUTC:   time.Date(2026, 9, 28, 18, 30, 0, 123456789, time.UTC),
			wantErr:   false,
		},
		{
			name:      "invalid date string",
			input:     "invalid-timestamp",
			paramName: "due_at",
			wantErr:   true,
		},
		{
			name:      "date only without time",
			input:     "2026-09-28",
			paramName: "due_at",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimeParam(tc.input, tc.paramName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseTimeParam(%q) expected error, got nil", tc.input)
				}
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("parseTimeParam(%q) err = %v, want domain.ErrInvalid", tc.input, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseTimeParam(%q) unexpected error: %v", tc.input, err)
			}

			if tc.input == "" {
				if got != nil {
					t.Errorf("parseTimeParam(%q) = %v, want nil", tc.input, got)
				}
				return
			}

			if got == nil {
				t.Fatalf("parseTimeParam(%q) returned nil, want %v", tc.input, tc.wantUTC)
			}

			if !got.Equal(tc.wantUTC) {
				t.Errorf("parseTimeParam(%q) = %v, want %v", tc.input, *got, tc.wantUTC)
			}

			if got.Location() != time.UTC {
				t.Errorf("parseTimeParam(%q) location = %v, want UTC", tc.input, got.Location())
			}
		})
	}
}

func TestHTTPTimestampParsing_FractionalSeconds(t *testing.T) {
	s := newStack(t)

	// Create a project
	proj := createProject(t, s, "Timestamp Test")

	// Create task with JS ISO8601 millisecond timestamp
	jsTimestamp := "2026-10-15T14:30:00.500Z"
	createdTask := makeTask(t, s, proj.ID, "Task with JS timestamp", map[string]any{
		"due_at": jsTimestamp,
	})
	if createdTask.DueAt == nil {
		t.Fatal("expected due_at to be set, got nil")
	}

	// Filter tasks using due_after and due_before with fractional seconds
	code, body := doReq(t, s.Handler, "GET", "/api/v1/tasks?project_id="+proj.ID+"&due_after=2026-10-15T14:00:00.000Z&due_before=2026-10-15T15:00:00.000Z", nil)
	if code != http.StatusOK {
		t.Fatalf("list tasks status = %d: %s", code, string(body))
	}
	taskList := mustDecode[struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
		Total int `json:"total"`
	}](t, body)
	if taskList.Total != 1 || len(taskList.Tasks) != 1 {
		t.Fatalf("expected 1 task matching filter, got %d", taskList.Total)
	}

	// Update task with JS ISO8601 millisecond timestamp
	code, body = doReq(t, s.Handler, "PATCH", "/api/v1/tasks/"+createdTask.ID, map[string]any{
		"actor":   actor("eve"),
		"version": 1,
		"due_at":  "2026-10-16T18:00:00.123Z",
	})
	if code != http.StatusOK {
		t.Fatalf("patch task status = %d: %s", code, string(body))
	}
}

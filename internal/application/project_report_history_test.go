package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

const historyProjectID = domain.ID("proj-history")

func TestReportHistory(t *testing.T) {
	ctx := context.Background()
	// Friday 2026-09-25T18:00:00Z; ISO week Monday is 2026-09-21T00:00:00Z.
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	minus3h := time.FixedZone("UTC-3", -3*3600)

	type taskSeed struct {
		id          string
		createdAt   time.Time
		status      domain.Status
		completedAt *time.Time
		archived    bool
	}

	type wantPoint struct {
		start      string
		opened     int
		completed  int
		cumulative int
	}

	cases := []struct {
		name       string
		now        time.Time
		projectID  domain.ID
		opts       ReportHistoryOptions
		tasks      []taskSeed
		wantErr    error
		wantDays   int
		wantBucket ReportBucket
		wantLen    int
		wantFirst  *wantPoint
		wantLast   *wantPoint
		wantAll    []wantPoint
	}{
		{
			name:      "unknown project returns ErrNotFound",
			now:       now,
			projectID: "no-such-project",
			opts:      ReportHistoryOptions{},
			wantErr:   domain.ErrNotFound,
		},
		{
			name:      "negative days returns ErrInvalid",
			now:       now,
			projectID: historyProjectID,
			opts:      ReportHistoryOptions{Days: -1},
			wantErr:   domain.ErrInvalid,
		},
		{
			name:      "invalid bucket returns ErrInvalid",
			now:       now,
			projectID: historyProjectID,
			opts:      ReportHistoryOptions{Days: 7, Bucket: "month"},
			wantErr:   domain.ErrInvalid,
		},
		{
			name:       "empty project returns non-nil empty series with defaults",
			now:        now,
			projectID:  historyProjectID,
			opts:       ReportHistoryOptions{},
			wantDays:   DefaultHistoryDays,
			wantBucket: ReportBucketDay,
			wantLen:    0,
		},
		{
			name:      "days above 90 clamped to 90",
			now:       now,
			projectID: historyProjectID,
			opts:      ReportHistoryOptions{Days: 365, Bucket: ReportBucketDay},
			tasks: []taskSeed{
				{id: "t1", createdAt: now, status: domain.StatusTodo},
			},
			wantDays:   MaxHistoryDays,
			wantBucket: ReportBucketDay,
			wantLen:    MaxHistoryDays,
			wantLast:   &wantPoint{start: "2026-09-25T00:00:00Z", opened: 1, completed: 0, cumulative: 0},
		},
		{
			name:      "day buckets: UTC midnight alignment, TZ offset normalization, prior completions, archived excluded",
			now:       now,
			projectID: historyProjectID,
			opts:      ReportHistoryOptions{Days: 3, Bucket: ReportBucketDay}, // 2026-09-23, 24, 25
			tasks: []taskSeed{
				// Completed before the 3-day window -> seeds cumulative_completed = 1
				{
					id:          "t-prior",
					createdAt:   time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
					status:      domain.StatusDone,
					completedAt: ptrTime(time.Date(2026, 9, 20, 23, 59, 59, 0, time.UTC)),
				},
				// Archived task -> completely excluded from opened and completed
				{
					id:          "t-arch",
					createdAt:   time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
					status:      domain.StatusDone,
					completedAt: ptrTime(time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)),
					archived:    true,
				},
				// Created on 2026-09-23T00:00:00Z exact boundary, completed 2026-09-24T23:59:59Z
				{
					id:          "t-edge",
					createdAt:   time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
					status:      domain.StatusDone,
					completedAt: ptrTime(time.Date(2026, 9, 24, 23, 59, 59, 0, time.UTC)),
				},
				// Created at 2026-09-24T22:00:00-03:00 == 2026-09-25T01:00:00Z -> lands on 2026-09-25
				{
					id:        "t-tz",
					createdAt: time.Date(2026, 9, 24, 22, 0, 0, 0, minus3h),
					status:    domain.StatusTodo,
				},
				// Reopened task (status doing, completedAt nil) -> opened counted, never completed
				{
					id:        "t-reopen",
					createdAt: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC),
					status:    domain.StatusDoing,
				},
			},
			wantDays:   3,
			wantBucket: ReportBucketDay,
			wantLen:    3,
			wantAll: []wantPoint{
				{start: "2026-09-23T00:00:00Z", opened: 1, completed: 0, cumulative: 1},
				{start: "2026-09-24T00:00:00Z", opened: 0, completed: 1, cumulative: 2},
				{start: "2026-09-25T00:00:00Z", opened: 2, completed: 0, cumulative: 2},
			},
		},
		{
			name:      "week buckets: ISO Monday UTC 00:00:00Z across year boundary",
			now:       time.Date(2026, 1, 4, 15, 0, 0, 0, time.UTC), // Sunday of ISO week starting Mon 2025-12-29
			projectID: historyProjectID,
			opts:      ReportHistoryOptions{Days: 10, Bucket: ReportBucketWeek}, // cutoff 2025-12-26 (Fri) -> Mon 2025-12-22 & Mon 2025-12-29
			tasks: []taskSeed{
				// Sunday 2025-12-28T23:59:59Z -> week of Mon 2025-12-22
				{
					id:          "t-w1",
					createdAt:   time.Date(2025, 12, 28, 23, 59, 59, 0, time.UTC),
					status:      domain.StatusDone,
					completedAt: ptrTime(time.Date(2025, 12, 28, 23, 59, 59, 0, time.UTC)),
				},
				// Monday 2025-12-29T00:00:00Z -> week of Mon 2025-12-29
				{
					id:        "t-w2a",
					createdAt: time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC),
					status:    domain.StatusTodo,
				},
				// Thursday 2026-01-01T12:00:00Z -> week of Mon 2025-12-29
				{
					id:          "t-w2b",
					createdAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
					status:      domain.StatusDone,
					completedAt: ptrTime(time.Date(2026, 1, 4, 20, 0, 0, 0, time.UTC)),
				},
			},
			wantDays:   10,
			wantBucket: ReportBucketWeek,
			wantLen:    2,
			wantAll: []wantPoint{
				{start: "2025-12-22T00:00:00Z", opened: 1, completed: 1, cumulative: 1},
				{start: "2025-12-29T00:00:00Z", opened: 2, completed: 1, cumulative: 2},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock(tc.now)
			fp := newFakeProjects()
			fp.byID[historyProjectID] = domain.Project{
				ID: historyProjectID, Name: "History", CreatedAt: tc.now, UpdatedAt: tc.now,
			}
			uow := newFakeUoW(fp, &fakeEvents{})
			for _, ts := range tc.tasks {
				uow.tasks.byID[domain.ID(ts.id)] = domain.Task{
					ID:          domain.ID(ts.id),
					Title:       ts.id,
					ProjectID:   historyProjectID,
					Status:      ts.status,
					Priority:    0,
					Version:     1,
					Archived:    ts.archived,
					CreatedAt:   ts.createdAt,
					UpdatedAt:   ts.createdAt,
					CompletedAt: ts.completedAt,
				}
			}
			svc := NewProjects(uow, fc, &fakeIDGen{})

			got, err := svc.ReportHistory(ctx, tc.projectID, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ReportHistory err = %v, want errors.Is(%v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReportHistory unexpected error: %v", err)
			}
			if got.ProjectID != tc.projectID {
				t.Errorf("ProjectID = %s, want %s", got.ProjectID, tc.projectID)
			}
			if got.Days != tc.wantDays {
				t.Errorf("Days = %d, want %d", got.Days, tc.wantDays)
			}
			if got.Bucket != tc.wantBucket {
				t.Errorf("Bucket = %q, want %q", got.Bucket, tc.wantBucket)
			}
			if got.Series == nil {
				t.Fatal("Series is nil, want non-nil slice")
			}
			if len(got.Series) != tc.wantLen {
				t.Fatalf("len(Series) = %d, want %d (%+v)", len(got.Series), tc.wantLen, got.Series)
			}

			assertPoint := func(idx int, pt ReportHistoryPoint, want wantPoint) {
				t.Helper()
				if gotStart := pt.BucketStart.UTC().Format(time.RFC3339); gotStart != want.start {
					t.Errorf("Series[%d].BucketStart = %s, want %s", idx, gotStart, want.start)
				}
				if pt.Opened != want.opened || pt.Completed != want.completed || pt.CumulativeCompleted != want.cumulative {
					t.Errorf("Series[%d] = {opened:%d completed:%d cumulative:%d}, want {opened:%d completed:%d cumulative:%d}",
						idx, pt.Opened, pt.Completed, pt.CumulativeCompleted,
						want.opened, want.completed, want.cumulative)
				}
			}
			if tc.wantFirst != nil {
				assertPoint(0, got.Series[0], *tc.wantFirst)
			}
			if tc.wantLast != nil {
				assertPoint(len(got.Series)-1, got.Series[len(got.Series)-1], *tc.wantLast)
			}
			for i, w := range tc.wantAll {
				assertPoint(i, got.Series[i], w)
			}
		})
	}
}

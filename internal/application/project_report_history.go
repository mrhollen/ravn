package application

import (
	"context"
	"fmt"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// ReportBucket selects the UTC time-series aggregation granularity for
// Projects.ReportHistory.
type ReportBucket string

const (
	ReportBucketDay  ReportBucket = "day"
	ReportBucketWeek ReportBucket = "week"
)

// DefaultHistoryDays and MaxHistoryDays bound the rolling window of
// Projects.ReportHistory. Values above MaxHistoryDays are clamped, not rejected.
const (
	DefaultHistoryDays = 30
	MaxHistoryDays     = 90
)

// ReportHistoryOptions tunes one project report history time-series query.
// Zero values select the defaults (Days: DefaultHistoryDays, Bucket: ReportBucketDay).
type ReportHistoryOptions struct {
	Days   int          // 0 → DefaultHistoryDays; <0 → ErrInvalid; >MaxHistoryDays → clamped to MaxHistoryDays
	Bucket ReportBucket // "" → ReportBucketDay; only "day" or "week" accepted
}

func (o ReportHistoryOptions) normalize() (days int, bucket ReportBucket, err error) {
	days = o.Days
	if days == 0 {
		days = DefaultHistoryDays
	} else if days < 0 {
		return 0, "", fmt.Errorf("%w: days must be a positive integer, got %d", domain.ErrInvalid, o.Days)
	}
	if days > MaxHistoryDays {
		days = MaxHistoryDays
	}

	bucket = o.Bucket
	if bucket == "" {
		bucket = ReportBucketDay
	}
	switch bucket {
	case ReportBucketDay, ReportBucketWeek:
	default:
		return 0, "", fmt.Errorf("%w: bucket must be %q or %q, got %q", domain.ErrInvalid, ReportBucketDay, ReportBucketWeek, o.Bucket)
	}
	return days, bucket, nil
}

// ReportHistoryPoint is one UTC-aligned bucket in ProjectReportHistory.
// Opened counts non-archived tasks created inside the bucket; Completed counts
// non-archived tasks whose completed_at falls inside the bucket;
// CumulativeCompleted is the running total of completed tasks through that bucket
// (seeded with tasks completed before the first bucket so the burn-up curve
// reflects total project progress regardless of window length).
type ReportHistoryPoint struct {
	BucketStart         time.Time
	Opened              int
	Completed           int
	CumulativeCompleted int
}

// ProjectReportHistory is the time-series activity digest for one project.
// Series is always non-nil (empty slice when the project has no non-archived tasks).
type ProjectReportHistory struct {
	ProjectID domain.ID
	Days      int
	Bucket    ReportBucket
	Series    []ReportHistoryPoint
}

// ReportHistory computes a UTC-aligned time series of task creations and
// completions for one project, in memory from the project's single
// Tasks.List(projectID) read (no new SQL query or schema change).
//
// Bucket boundaries (all in UTC):
//   - day:  00:00:00Z of the task's UTC calendar date
//   - week: ISO week Monday 00:00:00Z
//
// An unknown project returns domain.ErrNotFound. A project with no
// non-archived tasks returns an empty, non-nil Series slice.
func (s *Projects) ReportHistory(ctx context.Context, projectID domain.ID, o ReportHistoryOptions) (ProjectReportHistory, error) {
	days, bucket, err := o.normalize()
	if err != nil {
		return ProjectReportHistory{}, err
	}
	now := s.clock.Now().UTC()

	var hist ProjectReportHistory
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		if _, err := r.Projects.Get(ctx, projectID); err != nil { // 404 before touching task table
			return err
		}
		tasks, _, err := r.Tasks.List(ctx, TaskFilter{ProjectID: projectID})
		if err != nil {
			return err
		}

		hist = ProjectReportHistory{
			ProjectID: projectID,
			Days:      days,
			Bucket:    bucket,
			Series:    []ReportHistoryPoint{},
		}
		if len(tasks) == 0 {
			return nil
		}

		todayUTC := utcDayStart(now)
		cutoffDay := todayUTC.AddDate(0, 0, -(days - 1))
		firstBucket := bucketStart(cutoffDay, bucket)
		lastBucket := bucketStart(todayUTC, bucket)

		opened := make(map[time.Time]int)
		completed := make(map[time.Time]int)
		priorCompleted := 0

		for _, t := range tasks {
			cb := bucketStart(t.CreatedAt, bucket)
			if !cb.Before(firstBucket) && !cb.After(lastBucket) {
				opened[cb]++
			}
			if t.CompletedAt != nil {
				compB := bucketStart(*t.CompletedAt, bucket)
				if compB.Before(firstBucket) {
					priorCompleted++
				} else if !compB.After(lastBucket) {
					completed[compB]++
				}
			}
		}

		stepDays := 1
		if bucket == ReportBucketWeek {
			stepDays = 7
		}
		running := priorCompleted
		for cur := firstBucket; !cur.After(lastBucket); cur = cur.AddDate(0, 0, stepDays) {
			comp := completed[cur]
			running += comp
			hist.Series = append(hist.Series, ReportHistoryPoint{
				BucketStart:         cur,
				Opened:              opened[cur],
				Completed:           comp,
				CumulativeCompleted: running,
			})
		}
		return nil
	})
	if err != nil {
		return ProjectReportHistory{}, err
	}
	return hist, nil
}

// utcDayStart truncates t to 00:00:00Z of its UTC calendar day.
func utcDayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// bucketStart aligns t to its UTC bucket boundary:
//   - day:  UTC 00:00:00Z
//   - week: ISO week Monday UTC 00:00:00Z
func bucketStart(t time.Time, bucket ReportBucket) time.Time {
	day := utcDayStart(t)
	if bucket == ReportBucketWeek {
		offset := (int(day.Weekday()) + 6) % 7 // Monday=0, Tuesday=1, ..., Sunday=6
		return day.AddDate(0, 0, -offset)
	}
	return day
}

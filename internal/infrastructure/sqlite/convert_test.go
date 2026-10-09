package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestTimestampLayout_Constant(t *testing.T) {
	t.Parallel()
	const want = "2006-01-02T15:04:05.000000000Z"
	if TimestampLayout != want {
		t.Fatalf("TimestampLayout = %q, want %q", TimestampLayout, want)
	}
}

func TestFormatTS_FixedLengthAndPadding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			name: "whole second zero nanoseconds",
			in:   time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
			want: "2026-10-01T12:00:00.000000000Z",
		},
		{
			name: "fractional 500ms preserves 9 digits",
			in:   time.Date(2026, 10, 1, 12, 0, 0, 500000000, time.UTC),
			want: "2026-10-01T12:00:00.500000000Z",
		},
		{
			name: "trailing zeros in fraction preserved",
			in:   time.Date(2026, 10, 1, 12, 0, 0, 100000000, time.UTC),
			want: "2026-10-01T12:00:00.100000000Z",
		},
		{
			name: "single nanosecond leading zeros preserved",
			in:   time.Date(2026, 10, 1, 12, 0, 0, 1, time.UTC),
			want: "2026-10-01T12:00:00.000000001Z",
		},
		{
			name: "non-UTC timezone converted to UTC Z",
			in:   time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("EDT", -4*3600)),
			want: "2026-10-01T12:00:00.000000000Z",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTS(tc.in)
			if got != tc.want {
				t.Errorf("formatTS(%v) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) != 30 {
				t.Errorf("formatTS(%v) length = %d, want 30", tc.in, len(got))
			}
		})
	}
}

func TestParseTS_BackwardsCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			name: "fixed width 30-char timestamp",
			in:   "2026-10-01T12:00:00.000000000Z",
			want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "legacy unpadded RFC3339 whole second",
			in:   "2026-10-01T12:00:00Z",
			want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "legacy variable-length fractional second",
			in:   "2026-10-01T12:00:00.5Z",
			want: time.Date(2026, 10, 1, 12, 0, 0, 500000000, time.UTC),
		},
		{
			name: "legacy microsecond precision",
			in:   "2026-10-01T12:00:00.123456Z",
			want: time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTS(tc.in)
			if err != nil {
				t.Fatalf("parseTS(%q) unexpected error: %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseTS(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	if _, err := parseTS("not-a-timestamp"); err == nil {
		t.Errorf("parseTS(invalid) expected error, got nil")
	}
}

func TestFormatTS_LexicographicalOrderMatchesChronological(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sequence := []time.Time{
		base,
		base.Add(1 * time.Nanosecond),
		base.Add(100 * time.Microsecond),
		base.Add(100 * time.Millisecond), // .100000000Z
		base.Add(100*time.Millisecond + 1*time.Nanosecond),
		base.Add(500 * time.Millisecond), // .500000000Z
		base.Add(999*time.Millisecond + 999999*time.Nanosecond),
		base.Add(1 * time.Second),
	}

	for i := 0; i < len(sequence); i++ {
		for j := 0; j < len(sequence); j++ {
			tI := sequence[i]
			tJ := sequence[j]
			sI := formatTS(tI)
			sJ := formatTS(tJ)

			timeBefore := tI.Before(tJ)
			strLess := sI < sJ

			if timeBefore != strLess {
				t.Errorf("Ordering mismatch for (%s, %s): t1.Before(t2)=%v, formatTS(t1)<formatTS(t2)=%v (s1=%q, s2=%q)",
					tI, tJ, timeBefore, strLess, sI, sJ)
			}
		}
	}
}

func TestSQLite_TimestampLexicographicalOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	_, err := db.ExecContext(ctx, `CREATE TABLE ts_order_test (id TEXT PRIMARY KEY, ts TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create test table: %v", err)
	}

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	instants := []struct {
		id string
		t  time.Time
	}{
		{"whole-sec-0", base},
		{"sub-1ns", base.Add(1 * time.Nanosecond)},
		{"sub-100ms", base.Add(100 * time.Millisecond)},
		{"sub-500ms", base.Add(500 * time.Millisecond)},
		{"whole-sec-1", base.Add(1 * time.Second)},
		{"sub-1s-500ms", base.Add(1500 * time.Millisecond)},
		{"whole-sec-2", base.Add(2 * time.Second)},
	}

	// Insert in non-chronological order
	insertOrder := []int{4, 2, 0, 5, 1, 6, 3}
	for _, idx := range insertOrder {
		item := instants[idx]
		_, err := db.ExecContext(ctx, `INSERT INTO ts_order_test (id, ts) VALUES (?, ?)`, item.id, formatTS(item.t))
		if err != nil {
			t.Fatalf("insert %s: %v", item.id, err)
		}
	}

	// Query ORDER BY ts ASC
	rows, err := db.QueryContext(ctx, `SELECT id, ts FROM ts_order_test ORDER BY ts ASC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var gotIDs []string
	for rows.Next() {
		var id, tsStr string
		if err := rows.Scan(&id, &tsStr); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotIDs = append(gotIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(gotIDs) != len(instants) {
		t.Fatalf("got %d rows, want %d", len(gotIDs), len(instants))
	}
	for i, want := range instants {
		if gotIDs[i] != want.id {
			t.Errorf("row %d = %s, want %s", i, gotIDs[i], want.id)
		}
	}

	// Test comparison query: ts < formatTS(base + 500ms)
	cutoff := formatTS(base.Add(500 * time.Millisecond))
	var countLess int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ts_order_test WHERE ts < ?`, cutoff).Scan(&countLess)
	if err != nil {
		t.Fatalf("count less: %v", err)
	}
	// items before 500ms: whole-sec-0, sub-1ns, sub-100ms => 3 items
	if countLess != 3 {
		t.Errorf("countLess than 500ms = %d, want 3", countLess)
	}
}

func TestSQLite_ClaimExpirationComparison(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	// In SQLite: '2026-10-01T12:00:30.500000000Z' >= '2026-10-01T12:00:30.000000000Z'
	// should evaluate to 1 (true)
	var activeResult int
	tActive := time.Date(2026, 10, 1, 12, 0, 30, 500000000, time.UTC)
	tCheck := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	err := db.QueryRowContext(ctx, `SELECT ? >= ?`, formatTS(tActive), formatTS(tCheck)).Scan(&activeResult)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if activeResult != 1 {
		t.Errorf("active lease comparison result = %d, want 1", activeResult)
	}

	// '2026-10-01T12:00:30.500000000Z' < '2026-10-01T12:00:30.000000000Z'
	// should evaluate to 0 (false) — proving no premature expiration
	var expiredResult int
	err = db.QueryRowContext(ctx, `SELECT ? < ?`, formatTS(tActive), formatTS(tCheck)).Scan(&expiredResult)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if expiredResult != 0 {
		t.Errorf("premature expiration comparison result = %d, want 0", expiredResult)
	}
}

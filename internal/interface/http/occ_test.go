package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// occ_test.go: the TOCTOU regression suite for PATCH /tasks/{id} optimistic
// concurrency (primary 01M3MMZ2V3XBJRB4HAPVE4EM1P; this file also carries the
// acceptance criterion of duplicate 01M3MMVVJHBA4HR1Q8D78PKXQA: concurrent
// PATCHes carrying the SAME version must yield EXACTLY ONE 2xx, and every
// other racer must receive a 409 version_conflict exposing the then-current
// version). It runs against the real temp-file SQLite stack (real
// transactions), following the mandatory-race-test convention of
// uow_claim_test.go.

type raceOutcome struct {
	status int
	body   []byte
}

// patchRace fires one guarded PATCH through the handler and records what
// happened — any status is data, not a test error (this runs in goroutines, so
// it must never call t.Fatal).
func patchRace(stk stack, taskID string, version int, title string) (raceOutcome, error) {
	body, err := json.Marshal(map[string]any{
		"actor":   actor("eve"),
		"version": version,
		"title":   title, // each racer writes a DISTINCT title — all racers are real writes
	})
	if err != nil {
		return raceOutcome{}, err
	}
	req, err := http.NewRequest(http.MethodPatch, "http://ravn.test/api/v1/tasks/"+taskID, bytes.NewReader(body))
	if err != nil {
		return raceOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	return raceOutcome{status: res.Code, body: res.Body.Bytes()}, nil
}

// TestConcurrentPatchTasksSameVersion_ExactlyOneWinner is the carried
// concurrency AC. Before the fix, N same-version racers could ALL pass the
// handler's pre-check and every one of them commit (the use case rebased onto
// the row it read inside its own transaction) — a lost update with zero 409s.
// Now the version is checked INSIDE the write transaction: exactly one racer
// can win, and every loser's 409 carries the winner's fresh row.
func TestConcurrentPatchTasksSameVersion_ExactlyOneWinner(t *testing.T) {
	t.Parallel()
	const (
		rounds  = 5 // repeated a few times to shake out scheduling luck (QA requirement)
		workers = 6 // N ≥ 4
	)
	stk := newStack(t)
	p := createProject(t, stk, "OCC race")
	task := newTask(t, stk, p.ID, "race target")

	version := 1 // fresh tasks are born at version 1
	lastWinner := -1
	for round := 0; round < rounds; round++ {
		wantVersion := version + 1 // the single winner must commit exactly one bump
		var (
			wg      sync.WaitGroup
			results = make([]raceOutcome, workers)
			start   = make(chan struct{}) // barrier: release all racers at once
		)
		for i := 0; i < workers; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				out, err := patchRace(stk, task.ID, version, fmt.Sprintf("round-%d-racer-%d", round, i))
				if err != nil {
					t.Errorf("round %d racer %d: failed to issue PATCH: %v", round, i, err) // t.Errorf is goroutine-safe; Fatel would not be
					return
				}
				results[i] = out
			}()
		}
		close(start)
		wg.Wait()

		winners, conflicts := 0, 0
		for i, r := range results {
			switch {
			case r.status == http.StatusOK:
				winners++
				lastWinner = i
				got := mustDecode[taskSummaryDTO](t, r.body)
				if got.Version != wantVersion {
					t.Errorf("round %d winner %d: version = %d, want %d", round, i, got.Version, wantVersion)
				}
				wantTitle := fmt.Sprintf("round-%d-racer-%d", round, i)
				if got.Title != wantTitle {
					t.Errorf("round %d winner %d: title = %q, want %q — the committed row must carry the winner's write (lost-update check)",
						round, i, got.Title, wantTitle)
				}
			case r.status == http.StatusConflict:
				conflicts++
				e := mustDecode[errResp](t, r.body)
				if e.Error != "version_conflict" || e.CurrentVersion != wantVersion {
					t.Errorf("round %d racer %d: 409 body = %+v, want error version_conflict with current_version %d",
						round, i, e, wantVersion)
				}
				if e.Current == nil || e.Current.Version != wantVersion {
					t.Errorf("round %d racer %d: 409 `current` row = %+v, want the fresh v%d summary (the recovery instruction)",
						round, i, e.Current, wantVersion)
				}
			default:
				t.Errorf("round %d racer %d: status = %d, want 200 or 409 (body %s)", round, i, r.status, string(r.body))
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d winners out of %d same-version racers — want EXACTLY ONE", round, winners, workers)
		}
		if conflicts != workers-1 {
			t.Errorf("round %d: %d racers got 409, want %d (every loser must surface the conflict)", round, conflicts, workers-1)
		}
		version = wantVersion
	}

	// Settled state agrees with the round ledger: exactly `rounds` bumps, and
	// the final row carries the last winner's write.
	code, body := doReq(t, stk.Handler, http.MethodGet, "/api/v1/tasks/"+task.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("final GET = %d, want 200", code)
	}
	detail := mustDecode[map[string]any](t, body)
	gotVersion := int(detail["version"].(float64))
	gotTitle, _ := detail["title"].(string)
	if gotVersion != 1+rounds {
		t.Errorf("settled version = %d, want %d (exactly one bump per round)", gotVersion, 1+rounds)
	}
	wantFinal := fmt.Sprintf("round-%d-racer-%d", rounds-1, lastWinner)
	if gotTitle != wantFinal {
		t.Errorf("settled title = %q, want %q (the last winner's write must be the one that survived)", gotTitle, wantFinal)
	}
	t.Logf("race settled: version %d after %d rounds x %d same-version racers — exactly one winner per round", gotVersion, rounds, workers)
}

// TestUpdateNotesOptionalVersionGuard pins the notes endpoint's contract: no
// `version` field → the historical replace-on-write behavior; a matching
// version is accepted; a stale version → 409 exposing the then-current
// version with NOTHING written — proven by a subsequent guarded write at the
// current version still succeeding (the rejected write must have neither
// landed nor bumped, even though its blob was identical to the stored one).
func TestUpdateNotesOptionalVersionGuard(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "notes OCC")
	task := newTask(t, stk, p.ID, "notes guard")

	// No version field (the historical request shape) → replace + bump v1→v2.
	code, body := doReq(t, stk.Handler, http.MethodPut, "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("eve"), "notes": "first blob",
	})
	if code != http.StatusOK {
		t.Fatalf("unguarded PUT notes = %d, want 200 (body %s)", code, string(body))
	}
	got := mustDecode[taskSummaryDTO](t, body)
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}

	// Guarded write at the CURRENT version → accepted, bump v2→v3.
	code, body = doReq(t, stk.Handler, http.MethodPut, "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("eve"), "notes": "second blob", "version": 2,
	})
	if code != http.StatusOK {
		t.Fatalf("guarded PUT notes (matching version) = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Version != 3 {
		t.Fatalf("version = %d, want 3", got.Version)
	}

	// STALE version with an IDENTICAL blob (the no-op trap) → still 409: the
	// guard protects the caller's read state, not just field diffs.
	code, body = doReq(t, stk.Handler, http.MethodPut, "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("eve"), "notes": "second blob", "version": 1,
	})
	if code != http.StatusConflict {
		t.Fatalf("stale-version PUT notes = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "version_conflict" || e.CurrentVersion != 3 {
		t.Fatalf("409 body = %+v, want error version_conflict with current_version 3", e)
	}
	if e.Current == nil || e.Current.Version != 3 {
		t.Errorf("409 `current` row = %+v, want the fresh v3 summary (the recovery instruction)", e.Current)
	}

	// Proof the rejected write neither landed nor bumped: a guarded write at
	// the CURRENT version must now succeed.
	code, body = doReq(t, stk.Handler, http.MethodPut, "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("eve"), "notes": "third blob", "version": 3,
	})
	if code != http.StatusOK {
		t.Fatalf("write after a rejected stale notes write = %d, want 200 — the rejection must not have bumped or mutated (body %s)",
			code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Version != 4 {
		t.Errorf("version = %d, want 4", got.Version)
	}
}

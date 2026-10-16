package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// errors_test.go pins the shared error vocabulary's observable contract:
// exact messages, structured fields, and errors.Is matching across instances.
// The REST adapter (internal/interface/http/errors.go) maps these to status
// codes and the MCP layer propagates the bodies verbatim, so the message
// shapes below are part of the API contract — see web/src/moves.ts for the
// TransitionError pattern-match on the frontend.

// TestTransitionError_MessageAndFields pins that a refused status move records
// its endpoints on the struct and renders the exact message the frontend
// pattern-matches (keep the "cannot transition status from … to …" wording
// stable — web/src/moves.ts::friendlyTransitionError depends on it).
func TestTransitionError_MessageAndFields(t *testing.T) {
	e := &TransitionError{From: StatusTodo, To: StatusDone}
	if got, want := e.Error(), `cannot transition status from "todo" to "done"`; got != want {
		t.Fatalf("TransitionError.Error() = %q, want %q", got, want)
	}

	var te *TransitionError
	if !errors.As(error(e), &te) || te.From != StatusTodo || te.To != StatusDone {
		t.Fatalf("errors.As did not recover From/To: %+v", te)
	}

	// Every cell of the transition matrix is covered: legal moves produce no
	// error, and every illegal move names its endpoints.
	for _, from := range CanonicalStatuses() {
		for _, to := range CanonicalStatuses() {
			wantLegal := CanTransition(from, to)
			if wantLegal {
				continue
			}
			e := &TransitionError{From: from, To: to}
			want := fmt.Sprintf("cannot transition status from %q to %q", from, to)
			if e.Error() != want {
				t.Errorf("TransitionError{%s→%s}.Error() = %q, want %q", from, to, e.Error(), want)
			}
		}
	}
}

// TestConflictError_IsAcrossInstances pins the AC: errors.Is matches ANY two
// *ConflictError instances — including a zero-value target literal and a
// DIFFERENT instance created elsewhere — wrapped or bare. Callers check the
// KIND of conflict without knowing which row lost. (Only pointer targets exist:
// the value type has no Error method, so it never implements error.) The
// concrete instance is still recoverable via errors.As (the rebase data must
// survive).
func TestConflictError_IsAcrossInstances(t *testing.T) {
	e := &ConflictError{Current: Task{ID: "T1", Version: 7}}
	// A DIFFERENT instance, as distinct holder/expiry values would be in a real
	// conflict — identity must NOT matter, only the kind.
	foreign := &ConflictError{Current: Task{ID: "T-OTHER", Version: 9}}

	cases := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"bare instance vs zero-value target", e, &ConflictError{}, true},
		{"bare instance vs different foreign instance", e, foreign, true},
		{"wrapped in %w", fmt.Errorf("update lost the race: %w", e), &ConflictError{}, true},
		{"double wrapped", fmt.Errorf("outer: %w", fmt.Errorf("mid: %w", e)), &ConflictError{}, true},
		{"a claim conflict must NOT match", &ClaimConflictError{Holder: Actor{Type: ActorAgent, Name: "rival"}}, &ConflictError{}, false},
		{"a plain sentinel must not match", ErrNotFound, &ConflictError{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, tc.target); got != tc.want {
				t.Fatalf("errors.Is(%v, %T) = %v, want %v", tc.err, tc.target, got, tc.want)
			}
		})
	}

	// errors.As still recovers the concrete instance with its fields intact.
	var cc *ConflictError
	if !errors.As(fmt.Errorf("outer: %w", e), &cc) || cc.Current.ID != "T1" || cc.Current.Version != 7 {
		t.Fatalf("errors.As did not recover ConflictError.Current: %+v", cc)
	}
}

// TestClaimConflictError_IsAcrossInstances pins the same cross-instance
// matching for claim conflicts, plus the distinction from ConflictError: the
// two conflict kinds must not blur into each other under errors.Is.
func TestClaimConflictError_IsAcrossInstances(t *testing.T) {
	holder := Actor{Type: ActorAgent, Name: "runner-2"}
	e := &ClaimConflictError{Holder: holder, ExpiresAt: time.Date(2026, 9, 26, 15, 53, 0, 0, time.UTC)}

	cases := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"bare instance vs zero-value target", e, &ClaimConflictError{}, true},
		{"wrapped in %w", fmt.Errorf("heartbeat lost: %w", e), &ClaimConflictError{}, true},
		{"a different lease conflict also matches (kind, not identity)",
			fmt.Errorf("%w", &ClaimConflictError{Holder: Actor{Type: ActorHuman, Name: "ada"}, ExpiresAt: time.Now()}),
			&ClaimConflictError{}, true},
		{"a version conflict must NOT match", &ConflictError{Current: Task{ID: "T1", Version: 3}}, &ClaimConflictError{}, false},
		{"the no-lease sentinel must not match", ErrNotClaimed, &ClaimConflictError{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, tc.target); got != tc.want {
				t.Fatalf("errors.Is(%v, %T) = %v, want %v", tc.err, tc.target, got, tc.want)
			}
		})
	}

	// The live holder + expiry still travel with the error (the body IS the
	// recovery instruction) and are recoverable through wrapping.
	var cc *ClaimConflictError
	wrapped := fmt.Errorf("outer: %w", e)
	if !errors.As(wrapped, &cc) || cc.Holder != holder || !cc.ExpiresAt.Equal(e.ExpiresAt) {
		t.Fatalf("errors.As did not recover ClaimConflictError fields: %+v", cc)
	}
}

// TestErrNotClaimed_Sentinel pins the no-lease sentinel: its message, that
// wrapping works, and — crucially — that it is NOT a ClaimConflictError (an
// unclaimed heartbeat must never surface as "claimed by \"\" until 0001-…").
func TestErrNotClaimed_Sentinel(t *testing.T) {
	if got, want := ErrNotClaimed.Error(), "not claimed"; got != want {
		t.Fatalf("ErrNotClaimed.Error() = %q, want %q", got, want)
	}

	wrapped := fmt.Errorf("%w: task %s has no claim lease — call claim first", ErrNotClaimed, "T1")
	if !errors.Is(wrapped, ErrNotClaimed) {
		t.Fatalf("errors.Is(%q, ErrNotClaimed) = false, want true", wrapped)
	}

	var cc *ClaimConflictError
	if errors.As(wrapped, &cc) {
		t.Fatalf("ErrNotClaimed must not be a ClaimConflictError: %+v", cc)
	}
}

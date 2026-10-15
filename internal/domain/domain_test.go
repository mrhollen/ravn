package domain

import (
	"testing"
	"time"
)

func TestID(t *testing.T) {
	var zero ID
	if !zero.IsZero() || zero.String() != "" {
		t.Errorf("zero ID: IsZero=%v String=%q, want false/empty", zero.IsZero(), zero.String())
	}
	id := ID("01TEST")
	if id.IsZero() || id.String() != "01TEST" {
		t.Errorf("ID(01TEST): IsZero=%v String=%q", id.IsZero(), id.String())
	}
}

func TestStatusCanonical(t *testing.T) {
	want := []Status{StatusTodo, StatusDoing, StatusDone}
	got := CanonicalStatuses()
	if len(got) != len(want) {
		t.Fatalf("CanonicalStatuses() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] || !got[i].IsCanonical() {
			t.Errorf("status %q: order or IsCanonical mismatch (got %v)", got[i], got)
		}
	}
	for _, s := range []Status{"blocked", "DONE", ""} {
		if s.IsCanonical() {
			t.Errorf(`(%q).IsCanonical() = true, want false`, s)
		}
	}
}

func TestPriorityValid(t *testing.T) {
	for p := PriorityLowest; p <= PriorityHighest; p++ {
		if !p.Valid() {
			t.Errorf("(%d).Valid() = false, want true", p)
		}
	}
	if Priority(-1).Valid() || Priority(6).Valid() {
		t.Error("out-of-range priorities must be invalid")
	}
}

func TestActorTypeValid(t *testing.T) {
	for _, at := range []ActorType{ActorHuman, ActorAgent} {
		if !at.Valid() {
			t.Errorf("(%q).Valid() = false, want true", at)
		}
	}
	for _, bad := range []ActorType{"bot", "HUMAN", ""} {
		if bad.Valid() {
			t.Errorf(`(%q).Valid() = true, want false`, bad)
		}
	}
}

func TestKindsValid(t *testing.T) {
	check := func(name string, valid func(string) bool, ok, bad []string) {
		for _, v := range ok {
			if !valid(v) {
				t.Errorf("%s: %q reported invalid, want valid", name, v)
			}
		}
		for _, v := range bad {
			if valid(v) {
				t.Errorf("%s: %q reported valid, want invalid", name, v)
			}
		}
	}

	check("CommentKind", func(s string) bool { return CommentKind(s).Valid() },
		[]string{
			string(CommentProgress), string(CommentQuestion), string(CommentDecision),
			string(CommentHandoff), string(CommentNote),
		},
		[]string{"update", ""})

	check("ArtifactKind", func(s string) bool { return ArtifactKind(s).Valid() },
		[]string{
			string(ArtifactLog), string(ArtifactScreenshot), string(ArtifactCommandOutput),
			string(ArtifactTrace), string(ArtifactURL), string(ArtifactOther),
		},
		[]string{"file", ""})

	check("RelationKind", func(s string) bool { return RelationKind(s).Valid() },
		[]string{
			string(RelationParent), string(RelationChild), string(RelationBlockedBy),
			string(RelationDuplicateOf), string(RelationRelated),
		},
		[]string{"blocks", "parent-of", ""})
}

func TestTaskHelpers(t *testing.T) {
	unclaimed := Task{Status: StatusDoing}
	if unclaimed.Done() || unclaimed.Claimed() {
		t.Error("plain task must be neither done nor claimed")
	}

	actor := Actor{Type: ActorAgent, Name: "builder"}
	done := Task{
		Status:      StatusDone,
		ClaimedBy:   &actor,
		ClaimExpiry: timePtr(time.Now()),
	}
	if !done.Done() || !done.Claimed() {
		t.Error("claimed done task must report Done and Claimed")
	}

	// A holder without an expiry is not a real lease.
	half := Task{ClaimedBy: &actor}
	if half.Claimed() {
		t.Error("holder without expiry must not count as claimed")
	}
}

func TestTaskIsClaimActive(t *testing.T) {
	actor := Actor{Type: ActorAgent, Name: "builder"}
	now := time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC)

	// Absent lease: never active, and no lease row present.
	if (Task{}).IsClaimActive(now) || (Task{}).HasLease() {
		t.Error("unclaimed task must report neither active nor leased")
	}

	// A holder without an expiry is a half-lease: not active, not even present.
	half := Task{ClaimedBy: &actor}
	if half.IsClaimActive(now) || half.HasLease() {
		t.Error("holder without expiry must count as neither active nor leased")
	}

	// Active lease: holder + future expiry ⇒ active and claimed/present.
	active := Task{ClaimedBy: &actor, ClaimedAt: timePtr(now.Add(-time.Minute)), ClaimExpiry: timePtr(now.Add(time.Minute))}
	if !active.IsClaimActive(now) || !active.HasLease() || !active.Claimed() {
		t.Error("live lease must report active, leased and claimed")
	}

	// Boundary: at exactly the expiry instant the lease is already over —
	// presence yes, liveness no (strict now.Before).
	boundary := Task{ClaimedBy: &actor, ClaimExpiry: timePtr(now)}
	if boundary.IsClaimActive(now) {
		t.Error("lease at exactly its expiry must not be active")
	}
	if !boundary.HasLease() || !boundary.Claimed() {
		t.Error("the lease row is still attached at the expiry instant (presence)")
	}

	// Expired (zombie) lease: present but not active.
	stale := Task{ClaimedBy: &actor, ClaimExpiry: timePtr(now.Add(-time.Nanosecond))}
	if stale.IsClaimActive(now) {
		t.Error("expired lease must not be active")
	}
	if !stale.HasLease() || !stale.Claimed() {
		t.Error("zombie lease must still count as leased/claimed (presence)")
	}

	// IsClaimActive consults the passed instant, not the wall clock: the same
	// row is active for an earlier `now` and stale for a later one.
	if !active.IsClaimActive(now.Add(-2*time.Minute)) || active.IsClaimActive(now.Add(2*time.Minute)) {
		t.Error("IsClaimActive must be relative to the supplied now")
	}
}

func TestRelationKindValidVsStored(t *testing.T) {
	// Every stored kind is also acceptable at the tool boundary…
	for _, k := range []RelationKind{RelationChild, RelationBlockedBy, RelationDuplicateOf, RelationRelated} {
		if !k.Valid() || !k.Stored() {
			t.Errorf("%q: Valid=%v Stored=%v, want true/true", k, k.Valid(), k.Stored())
		}
	}
	// …but 'parent' is valid only as INPUT: the use case normalizes it to an
	// inverse child edge, so it must never be reported as storable.
	if !RelationParent.Valid() || RelationParent.Stored() {
		t.Errorf("parent: Valid=%v Stored=%v, want true/false (input alias, never persisted)",
			RelationParent.Valid(), RelationParent.Stored())
	}
	if RelationKind("blocks").Valid() || RelationKind("").Stored() {
		t.Error("unknown kinds must stay invalid / unstorable")
	}
}

func TestTaskUnsatisfiedACsError(t *testing.T) {
	err := &TaskUnsatisfiedACsError{
		TaskID:    ID("01J3ZK8WQ9X7M2P4R6S8T0U2V4"),
		UnmetSeqs: []int{1, 3},
	}
	want := "task 01J3ZK8WQ9X7M2P4R6S8T0U2V4 has 2 unsatisfied acceptance criteria (seq 1, 3)"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

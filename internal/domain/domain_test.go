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
	if err := unclaimed.Validate(); err != nil {
		t.Errorf("plain unclaimed task: Validate() = %v, want nil", err)
	}

	actor := Actor{Type: ActorAgent, Name: "builder"}
	now := time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC)
	done := Task{
		Status:      StatusDone,
		CompletedAt: timePtr(now),
		Claim:       NewClaim(actor, now.Add(-time.Minute), 120),
	}
	if !done.Done() || !done.Claimed() {
		t.Error("claimed done task must report Done and Claimed")
	}
	if err := done.Validate(); err != nil {
		t.Errorf("consistent done + live-lease task: Validate() = %v, want nil", err)
	}

	// A partial lease CANNOT be attached through any sanctioned constructor —
	// this is what makes the encapsulation meaningful. Same-package test code
	// can still fabricate one with a composite literal (unexported fields), and
	// Validate() must reject it as the backstop: holder without an expiry is
	// not a real lease.
	half := Task{Claim: Claim{by: &actor}}
	if ve := half.Validate(); ve == nil {
		t.Error("task with a holder-without-expiry lease must fail Validate()")
	} else if ie, ok := errAsInvariant(ve); !ok || ie.Entity != "claim lease" {
		t.Errorf("partial lease: want *InvariantError(entity \"claim lease\"), got %T %v", ve, ve)
	}
}

func errAsInvariant(err error) (*InvariantError, bool) {
	ie, ok := err.(*InvariantError)
	return ie, ok
}

func TestTaskIsClaimActive(t *testing.T) {
	actor := Actor{Type: ActorAgent, Name: "builder"}
	now := time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC)

	// Absent lease: never active, and no lease row present.
	if (Task{}).IsClaimActive(now) || (Task{}).HasLease() {
		t.Error("unclaimed task must report neither active nor leased")
	}

	// A partial lease cannot be attached through the sanctioned constructors —
	// ClaimFromStored degrades ANY incomplete input to the zero value, so a
	// holder-without-expiry is not even representable on read.
	if got := ClaimFromStored(&actor, timePtr(now.Add(-time.Minute)), nil, intPtr(300)); got.Present() {
		t.Error("ClaimFromStored with a missing expiry must degrade to unclaimed")
	}
	if got := ClaimFromStored(nil, timePtr(now), timePtr(now.Add(time.Second)), intPtr(1)); got.Present() {
		t.Error("ClaimFromStored with a missing holder must degrade to unclaimed")
	}

	// Active lease: holder + future expiry ⇒ active and claimed/present.
	active := Task{Claim: NewClaim(actor, now.Add(-time.Minute), 120)}
	if !active.IsClaimActive(now) || !active.HasLease() || !active.Claimed() {
		t.Error("live lease must report active, leased and claimed")
	}

	// Boundary: at exactly the expiry instant the lease is already over —
	// presence yes, liveness no (strict now.Before). ClaimFromStored is used so
	// the stored expiry lands EXACTLY on `now` (a heartbeat-style re-stamp).
	boundary := Task{Claim: ClaimFromStored(&actor, timePtr(now.Add(-time.Minute)), timePtr(now), intPtr(60))}
	if boundary.IsClaimActive(now) {
		t.Error("lease at exactly its expiry must not be active")
	}
	if !boundary.HasLease() || !boundary.Claimed() {
		t.Error("the lease row is still attached at the expiry instant (presence)")
	}

	// Expired (zombie) lease: present but not active.
	stale := Task{Claim: ClaimFromStored(&actor, timePtr(now.Add(-time.Minute)), timePtr(now.Add(-time.Nanosecond)), intPtr(60))}
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

func intPtr(i int) *int { return &i }

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

func timePtr(t time.Time) *time.Time { return &t }

func TestTaskClone_DeepCopy(t *testing.T) {
	due := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	assignee := Actor{Type: ActorAgent, Name: "worker-1"}
	claimer := Actor{Type: ActorHuman, Name: "hollen"}
	claimedAt := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

	orig := Task{
		ID:          "TASK-1",
		ProjectID:   "PROJ-1",
		Title:       "Original Task",
		Description: "Original Description",
		Status:      StatusDone,
		Priority:    3,
		DueAt:       &due,
		Assignee:    &assignee,
		CompletedAt: &completed,
		Claim:       NewClaim(claimer, claimedAt, 3600),
	}

	cloned := orig.Clone()

	// Assert equality of values initially
	if cloned.ID != orig.ID || cloned.Title != orig.Title || *cloned.DueAt != *orig.DueAt ||
		*cloned.Assignee != *orig.Assignee || *cloned.CompletedAt != *orig.CompletedAt ||
		*cloned.Claim.By() != *orig.Claim.By() {
		t.Fatalf("cloned task values mismatch original: %+v vs %+v", cloned, orig)
	}

	// Assert pointer addresses are distinct (not aliased)
	if cloned.DueAt == orig.DueAt {
		t.Errorf("cloned DueAt pointer aliases original: %p == %p", cloned.DueAt, orig.DueAt)
	}
	if cloned.Assignee == orig.Assignee {
		t.Errorf("cloned Assignee pointer aliases original: %p == %p", cloned.Assignee, orig.Assignee)
	}
	if cloned.CompletedAt == orig.CompletedAt {
		t.Errorf("cloned CompletedAt pointer aliases original: %p == %p", cloned.CompletedAt, orig.CompletedAt)
	}
	if cloned.Claim.By() == orig.Claim.By() {
		t.Errorf("cloned Claim.By pointer aliases original: %p == %p", cloned.Claim.By(), orig.Claim.By())
	}

	// Mutate pointers on cloned entity
	*cloned.DueAt = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	cloned.Assignee.Name = "mutated-assignee"
	*cloned.CompletedAt = time.Date(2029, 2, 2, 0, 0, 0, 0, time.UTC)
	cloned.Claim.By().Name = "mutated-claimer"

	// Assert original entity remains unchanged
	if orig.DueAt.Equal(time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("mutating cloned DueAt affected original: got %v", orig.DueAt)
	}
	if orig.Assignee.Name != "worker-1" {
		t.Errorf("mutating cloned Assignee affected original: got %s, want worker-1", orig.Assignee.Name)
	}
	if orig.CompletedAt.Equal(time.Date(2029, 2, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("mutating cloned CompletedAt affected original: got %v", orig.CompletedAt)
	}
	if orig.Claim.By().Name != "hollen" {
		t.Errorf("mutating cloned Claim.By affected original: got %s, want hollen", orig.Claim.By().Name)
	}

	// Test nil pointers clone cleanly
	emptyTask := Task{ID: "EMPTY"}
	emptyCloned := emptyTask.Clone()
	if emptyCloned.DueAt != nil || emptyCloned.Assignee != nil || emptyCloned.CompletedAt != nil || emptyCloned.Claim.By() != nil {
		t.Errorf("cloning task with nil pointers produced non-nil pointers: %+v", emptyCloned)
	}
}

func TestAcceptanceCriterionClone_DeepCopy(t *testing.T) {
	completed := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	actor := Actor{Type: ActorHuman, Name: "alice"}

	orig := AcceptanceCriterion{
		ID:          "AC-1",
		TaskID:      "TASK-1",
		Seq:         1,
		Text:        "Criterion 1",
		Done:        true,
		Note:        "Verified",
		CompletedBy: &actor,
		CompletedAt: &completed,
	}

	cloned := orig.Clone()

	if cloned.CompletedBy == orig.CompletedBy {
		t.Errorf("cloned CompletedBy pointer aliases original: %p == %p", cloned.CompletedBy, orig.CompletedBy)
	}
	if cloned.CompletedAt == orig.CompletedAt {
		t.Errorf("cloned CompletedAt pointer aliases original: %p == %p", cloned.CompletedAt, orig.CompletedAt)
	}

	// Mutate cloned pointers
	cloned.CompletedBy.Name = "bob"
	*cloned.CompletedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	if orig.CompletedBy.Name != "alice" {
		t.Errorf("mutating cloned CompletedBy affected original: got %s, want alice", orig.CompletedBy.Name)
	}
	if orig.CompletedAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("mutating cloned CompletedAt affected original: got %v", orig.CompletedAt)
	}

	// Nil fields
	emptyAC := AcceptanceCriterion{ID: "AC-2", Seq: 2}
	emptyCloned := emptyAC.Clone()
	if emptyCloned.CompletedBy != nil || emptyCloned.CompletedAt != nil {
		t.Errorf("cloning AC with nil pointers produced non-nil pointers: %+v", emptyCloned)
	}
}

func TestNewConflictError_ClonesCurrent(t *testing.T) {
	due := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	assignee := Actor{Type: ActorHuman, Name: "alice"}
	orig := Task{
		ID:       "T-1",
		DueAt:    &due,
		Assignee: &assignee,
	}

	err := NewConflictError(orig)
	if err.Current.DueAt == orig.DueAt {
		t.Errorf("NewConflictError DueAt aliases original")
	}
	if err.Current.Assignee == orig.Assignee {
		t.Errorf("NewConflictError Assignee aliases original")
	}
}

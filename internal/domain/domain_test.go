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

func timePtr(t time.Time) *time.Time { return &t }

package domain

import (
	"testing"
)

func TestEventKinds_AreCanonicalDistinctAndValid(t *testing.T) {
	if len(AllEventKinds) == 0 {
		t.Fatal("AllEventKinds is empty")
	}
	seen := make(map[EventKind]bool, len(AllEventKinds))
	for _, k := range AllEventKinds {
		if k == "" {
			t.Fatal("empty kind in AllEventKinds")
		}
		if !k.Valid() {
			t.Fatalf("kind %q is listed in AllEventKinds but not Valid()", k)
		}
		if seen[k] {
			t.Fatalf("kind %q appears twice in AllEventKinds", k)
		}
		seen[k] = true
	}

	// The minimum set locked by the Slice 2A ADR must be present.
	for _, want := range []EventKind{
		EventCreated, EventUpdated, EventStatusChanged, EventClaimed,
		EventReleased, EventAssigned, EventNoted, EventCommented,
		EventArtifactAdded, EventRelationChanged, EventACToggled,
	} {
		if !seen[want] {
			t.Errorf("canonical kind %q missing from AllEventKinds", want)
		}
	}
}

func TestEventKind_Invalid(t *testing.T) {
	for _, bad := range []EventKind{"", "bogus", "relation-added", "relation-removed", "project-created", "updated "} {
		if bad.Valid() {
			t.Errorf("kind %q must not be Valid (superseded or malformed)", bad)
		}
	}
}

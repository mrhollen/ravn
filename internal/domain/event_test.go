package domain

import (
	"sync"
	"testing"
)

func TestEventKinds_AreCanonicalDistinctAndValid(t *testing.T) {
	kinds := EventKinds()
	if len(kinds) == 0 {
		t.Fatal("EventKinds() is empty")
	}
	seen := make(map[EventKind]bool, len(kinds))
	for _, k := range kinds {
		if k == "" {
			t.Fatal("empty kind in EventKinds()")
		}
		if !k.Valid() {
			t.Fatalf("kind %q is listed in EventKinds() but not Valid()", k)
		}
		if seen[k] {
			t.Fatalf("kind %q appears twice in EventKinds()", k)
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
			t.Errorf("canonical kind %q missing from EventKinds()", want)
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

// TestEventKinds_CanonicalSetIsLocked pins the exact canonical set in its
// stable order, so any accidental add/remove/reorder is caught here instead
// of silently changing what EventKinds() exposes.
func TestEventKinds_CanonicalSetIsLocked(t *testing.T) {
	want := []EventKind{
		EventCreated, EventUpdated, EventStatusChanged, EventClaimed,
		EventReleased, EventHeartbeat, EventAssigned, EventNoted,
		EventCommented, EventACToggled, EventArtifactAdded,
		EventRelationChanged, EventACAdded, EventACUpdated, EventACRemoved,
	}
	got := EventKinds()
	if len(got) != len(want) {
		t.Fatalf("EventKinds() length = %d, want %d (got: %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("EventKinds()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEventKinds_MutationOfCopyIsIsolated verifies AC3: mutating the slice a
// caller gets from EventKinds() must not leak into the package's canonical set
// or into Valid(), no matter how violently the copy is abused.
func TestEventKinds_MutationOfCopyIsIsolated(t *testing.T) {
	before := EventKinds()

	copied := EventKinds()
	if len(copied) == 0 {
		t.Fatal("EventKinds() returned an empty slice")
	}
	// Overwrite every element, append junk, then nil the variable itself.
	for i := range copied {
		copied[i] = EventKind("")
	}
	copied = append(copied, EventKind("bogus"))
	copied = nil

	if after := EventKinds(); len(after) != len(before) {
		t.Fatalf("EventKinds() length = %d after copy mutation, want %d", len(after), len(before))
	}
	for i := range before {
		if got := EventKinds()[i]; got != before[i] {
			t.Errorf("EventKinds()[%d] = %q after copy mutation, want %q", i, got, before[i])
		}
	}
	// Validation must be unaffected too: the abused copies are gone, and every
	// canonical kind still validates.
	for _, k := range EventKinds() {
		if !k.Valid() {
			t.Errorf("kind %q stopped being Valid after a copy was mutated", k)
		}
	}
	if EventKind("").Valid() || EventKind("bogus").Valid() {
		t.Error("mutated copy values became Valid; validation leaked the mutation")
	}
}

// TestEventKinds_ConcurrentAccessRaceFree hammers EventKinds() and Valid() from
// many goroutines while copies are being torn apart. Under -race this proves
// the accessor and validator share no mutable state (previously both read the
// exported AllEventKinds slice, so a concurrent mutation was a data race).
func TestEventKinds_ConcurrentAccessRaceFree(t *testing.T) {
	const goroutines = 32
	var (
		mu        sync.Mutex
		badLength int // EventKinds() returned an unexpected length
		badValid  int // Valid() observed a corrupted result
	)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 512; i++ {
				kinds := EventKinds()
				if len(kinds) != 15 {
					mu.Lock()
					badLength++
					mu.Unlock()
					return
				}
				for j := range kinds {
					kinds[j] = EventKind("") // tear the private copy down
				}
				if !EventCreated.Valid() || !EventACRemoved.Valid() || EventKind("nope").Valid() {
					mu.Lock()
					badValid++
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if badLength != 0 {
		t.Errorf("EventKinds() returned a wrong length from %d goroutine(s)", badLength)
	}
	if badValid != 0 {
		t.Errorf("Valid() was corrupted under concurrency in %d goroutine(s)", badValid)
	}
}

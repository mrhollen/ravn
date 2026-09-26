package idgen

import (
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestNewIDIsUniqueAndWellFormed(t *testing.T) {
	gen := New()
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		id := gen.NewID()
		if id.IsZero() {
			t.Fatal("NewID returned the zero ID")
		}
		if len(id) != 26 {
			t.Fatalf("id %q has length %d, want 26 (ULID)", id, len(id))
		}
		if _, err := ulid.ParseStrict(string(id)); err != nil {
			t.Fatalf("id %q is not a strict ULID: %v", id, err)
		}
		if seen[string(id)] {
			t.Fatalf("duplicate id %q minted", id)
		}
		seen[string(id)] = true
	}
}

func TestNewIDIsSortable(t *testing.T) {
	gen := New()
	prev := gen.NewID()
	for i := 0; i < 100; i++ {
		cur := gen.NewID()
		if cur.String() <= prev.String() {
			t.Fatalf("ids not strictly increasing: %q followed by %q", prev, cur)
		}
		prev = cur
	}
}

package idgen

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/mrhollen/ravn/internal/domain"
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

// TestNewIDIsSortableAcrossMilliseconds mints enough IDs to span multiple
// millisecond boundaries; the time component dominates ordering there while
// monotonic entropy handles same-millisecond mints.
func TestNewIDIsSortableAcrossMilliseconds(t *testing.T) {
	gen := New()
	prev := gen.NewID()
	for i := 0; i < 2000; i++ {
		cur := gen.NewID()
		if cur.String() <= prev.String() {
			t.Fatalf("ids not strictly increasing: %q followed by %q", prev, cur)
		}
		prev = cur
	}
}

func TestConcurrentMintingIsUniqueAndMonotonicPerGoroutine(t *testing.T) {
	gen := New()
	const (
		goroutines   = 8
		perGoroutine = 500
	)
	collect := make([][]domain.ID, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				collect[g] = append(collect[g], gen.NewID())
			}
		}(g)
	}
	wg.Wait()

	seen := make(map[domain.ID]bool, goroutines*perGoroutine)
	for g, ids := range collect {
		if len(ids) != perGoroutine {
			t.Fatalf("goroutine %d minted %d ids, want %d", g, len(ids), perGoroutine)
		}
		for i, id := range ids {
			if _, err := ulid.ParseStrict(string(id)); err != nil {
				t.Fatalf("goroutine %d id %q is not a strict ULID: %v", g, id, err)
			}
			if seen[id] {
				t.Fatalf("duplicate id %q minted across goroutines", id)
			}
			seen[id] = true
			if i > 0 && string(id) <= string(ids[i-1]) {
				t.Fatalf("goroutine %d: ids not strictly increasing: %q followed by %q", g, ids[i-1], id)
			}
		}
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("minted %d unique ids, want %d", len(seen), goroutines*perGoroutine)
	}
}

// flakyMint is a mint function that fails with ErrMonotonicOverflow for its
// first `fails` calls, then mints a real ULID from crypto/rand.
type flakyMint struct {
	mu    sync.Mutex
	fails int
	done  bool
}

func (f *flakyMint) mint() (ulid.ULID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return ulid.ULID{}, ulid.ErrMonotonicOverflow
	}
	f.done = true
	return ulid.New(ulid.Now(), rand.Reader)
}

// TestNewIDRetriesMonotonicOverflow ensures transient ErrMonotonicOverflow is
// handled by bounded retry instead of panicking.
func TestNewIDRetriesMonotonicOverflow(t *testing.T) {
	m := &flakyMint{fails: 3}
	gen := newULIDGenerator(m.mint)

	id := gen.NewID()
	if !m.done {
		t.Fatal("expected a successful mint after the transient overflow")
	}
	if _, err := ulid.ParseStrict(string(id)); err != nil {
		t.Fatalf("minted %q is not a strict ULID: %v", id, err)
	}
}

// TestNewIDPanicOnlyAfterRetriesExhausted documents the terminal failure mode:
// once retry attempts are exhausted (persistent entropy-source failure), NewID
// panics with a descriptive error instead of minting an insecure ID.
func TestNewIDPanicOnlyAfterRetriesExhausted(t *testing.T) {
	gen := newULIDGenerator((&flakyMint{fails: overflowRetries + 1}).mint)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected NewID to panic after exhausting overflow retries")
		} else if s := fmt.Sprint(r); !strings.Contains(s, "failed to mint ULID") {
			t.Fatalf("unexpected panic message: %v", r)
		}
	}()
	gen.NewID()
}

// Package domain is the core of ravn's onion architecture: value types and
// entities for projects, tasks, and their collaboration data. It depends on
// the Go standard library only — no third-party modules, no other project
// packages, and no interfaces (ports live in internal/application). Nothing
// here performs I/O or knows how identifiers are minted.
package domain

import "fmt"

// ID identifies an entity by a string minted out-of-band via the
// application.IDGenerator port (ULIDs in this codebase). The domain never
// mints IDs itself and assumes nothing about their format beyond being plain,
// JSON-safe strings. A zero-value ID ("") means "unset".
type ID string

// ULIDLength is the byte length of an encoded ULID: 128 bits rendered as
// Crockford base32 — exactly the shape infrastructure/idgen emits.
const ULIDLength = 26

// String returns the raw identifier.
func (id ID) String() string { return string(id) }

// IsZero reports whether id was never minted.
func (id ID) IsZero() bool { return id == "" }

// Valid reports whether id is a well-formed, non-empty ULID: exactly 26
// Crockford base32 characters — digits or A–Z minus I, L, O and U, the same
// alphabet oklog/ulid emits (see infrastructure/idgen), so every ID this
// codebase mints passes. Matching is case-insensitive like Crockford base32
// itself: a lowercased copy of a real ULID is still well-formed (it simply 404s
// downstream, since storage compares in the uppercase form). Empty strings and
// any other length or character fail — use IsZero for the "unset" check.
func (id ID) Valid() bool {
	s := string(id)
	if len(s) != ULIDLength {
		return false
	}
	for i := 0; i < ULIDLength; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' { // Crockford is case-insensitive
			c -= 'a' - 'A'
		}
		switch {
		case c >= '0' && c <= '9':
		case (c >= 'A' && c <= 'Z') && c != 'I' && c != 'L' && c != 'O' && c != 'U':
		default:
			return false
		}
	}
	return true
}

// ParseID parses s into an ID, returning ErrInvalid wrapped with a detail when
// s is empty or fails the ULID format check. Boundary adapters (REST handlers,
// MCP tool arguments) MUST parse IDs before passing them to use cases or
// storage; layers inside the boundary trust values that already round-tripped
// through one. ParseID does not trim: call sites keep their existing whitespace
// policy and forward the value as parsed. The error detail truncates oversized
// input so a hostile multi-megabyte value cannot balloon the response body.
func ParseID(s string) (ID, error) {
	id := ID(s)
	if !id.Valid() {
		return "", fmt.Errorf("%w: malformed id %q (expected a 26-character Crockford base32 ULID)", ErrInvalid, truncated(s))
	}
	return id, nil
}

// maxIDDetail caps how much of an offending value ParseID's error message may
// echo back to the caller.
const maxIDDetail = 32

// truncated renders s for an error message, cutting oversized input at
// maxIDDetail bytes (a mid-rune cut is fine: JSON encoding maps invalid UTF-8
// to U+FFFD).
func truncated(s string) string {
	if len(s) <= maxIDDetail {
		return s
	}
	return s[:maxIDDetail] + "…"
}

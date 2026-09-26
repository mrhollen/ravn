// Package domain is the core of crowban's onion architecture: value types and
// entities for projects, tasks, and their collaboration data. It depends on
// the Go standard library only — no third-party modules, no other project
// packages, and no interfaces (ports live in internal/application). Nothing
// here performs I/O or knows how identifiers are minted.
package domain

// ID identifies an entity by a string minted out-of-band via the
// application.IDGenerator port (ULIDs in this codebase). The domain never
// mints IDs itself and assumes nothing about their format beyond being plain,
// JSON-safe strings. A zero-value ID ("") means "unset".
type ID string

// String returns the raw identifier.
func (id ID) String() string { return string(id) }

// IsZero reports whether id was never minted.
func (id ID) IsZero() bool { return id == "" }

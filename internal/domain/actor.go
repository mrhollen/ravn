package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ActorType is the kind of principal that acts on ravn data: a human or an
// AI agent. Both are stored as plain (type, name) pairs; ravn does no
// identity resolution in the MVP.
type ActorType string

const (
	ActorHuman ActorType = "human"
	ActorAgent ActorType = "agent"
)

// Valid reports whether t is one of the known actor types.
func (t ActorType) Valid() bool {
	switch t {
	case ActorHuman, ActorAgent:
		return true
	default:
		return false
	}
}

// MaxActorNameLen caps an actor name's length in RUNES after trimming. Names
// are user-visible labels for self-declared principals, not identifiers:
// anything longer is almost certainly a paste or an accident (a pasted stack
// trace, an email address with extra text) and must not be able to bloat event
// JSON or the UI. Every actor name in this repo's unit tests and E2E fixtures
// is well under the cap. Validation happens at WRITE paths only (see
// normalizeActor in the application layer), so legacy rows stored before the
// cap cannot be rejected by read-path validation.
const MaxActorNameLen = 100

// Actor names a principal as a typed (type, name) pair. It models both durable
// task ownership (Task.Assignee — stored as a pointer, nil = unassigned) and
// the actor behind comments, artifacts, relations, and events.
type Actor struct {
	Type ActorType
	Name string
}

// Validate reports whether a is a well-formed actor principal:
//
//   - Type is one of the known types (ActorHuman / ActorAgent — Type.Valid());
//   - Name is non-empty after trimming;
//   - the trimmed name is at most MaxActorNameLen runes long;
//   - the trimmed name contains no control characters — unicode.IsControl,
//     which covers C0 (\n \r \t …), DEL, and the C1 block, so line breaks can
//     never reach stored rows or single-line JSON event payloads. The name
//     must also be WELL-FORMED UTF-8 (invalid byte sequences decode to
//     U+FFFD while ranging, which would otherwise evade the control check).
//
// The check runs against the TRIMMED name; multi-byte UTF-8 is first-class —
// only the rune count is capped. Errors wrap ErrInvalid (actor input is
// caller-supplied), so use cases can return them verbatim and the REST layer
// maps them to 400. Validate is deliberately a pure predicate over its
// argument: it does not trim the receiver, because every write path trims via
// normalizeActor first and read paths must keep accepting legacy rows.
func (a Actor) Validate() error {
	if !a.Type.Valid() {
		return fmt.Errorf("%w: unknown actor type %q", ErrInvalid, a.Type)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return fmt.Errorf("%w: actor name must not be empty", ErrInvalid)
	}
	if runes := utf8.RuneCountInString(name); runes > MaxActorNameLen {
		return fmt.Errorf("%w: actor name is %d runes, max is %d", ErrInvalid, runes, MaxActorNameLen)
	}
	if !utf8.ValidString(name) {
		// Ranging over an invalid byte sequence yields U+FFFD (not a control
		// character), so garbage bytes would otherwise sail through the check
		// below: reject non-well-formed UTF-8 outright. Well-formed multi-byte
		// names are unaffected — only malformed input fails here.
		return fmt.Errorf("%w: actor name is not valid UTF-8", ErrInvalid)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: actor name contains control character U+%04X", ErrInvalid, r)
		}
	}
	return nil
}

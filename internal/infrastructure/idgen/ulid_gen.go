// Package idgen provides the application.IDGenerator implementation backed by
// oklog/ulid/v2: current-time, monotonic-per-process ULIDs — sortable, 26-char
// Crockford-base32 strings that are JSON-safe and avoid int64 precision loss
// in JS and agent clients. This is one of the only packages allowed to import
// a third-party module outside cmd/*.
package idgen

import (
	"github.com/oklog/ulid/v2"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// ULIDGenerator implements application.IDGenerator. ulid.Make uses a shared,
// thread-safe, monotonic-per-process entropy source, so sequential mints are
// strictly sortable even within the same millisecond. The generator is stateless
// and safe for concurrent use.
type ULIDGenerator struct{}

// New returns a ready-to-use ULIDGenerator.
func New() *ULIDGenerator { return &ULIDGenerator{} }

// NewID implements application.IDGenerator.
func (g *ULIDGenerator) NewID() domain.ID {
	return domain.ID(ulid.Make().String())
}

var _ application.IDGenerator = (*ULIDGenerator)(nil)

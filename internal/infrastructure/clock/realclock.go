// Package clock provides the application.Clock implementation backed by the
// system clock, always normalized to UTC.
package clock

import (
	"time"

	"github.com/mrhollen/ravn/internal/application"
)

// RealClock is the production Clock: wall-clock time, UTC-normalized. It has no
// state and is safe for concurrent use.
type RealClock struct{}

// New returns a ready-to-use RealClock.
func New() *RealClock { return &RealClock{} }

// Now implements application.Clock.
func (c *RealClock) Now() time.Time { return time.Now().UTC() }

var _ application.Clock = (*RealClock)(nil)

package sqlite

import (
	"time"
)

// Timestamps cross the storage boundary as RFC3339 UTC strings
// (ARCHITECTURE §4 — trivially readable for agents, no float issues). All repo
// code formats and parses through these two helpers so neither direction can
// drift. Formatting uses RFC3339Nano so sub-second precision survives the
// round-trip losslessly (whole-second instants render exactly like the plain
// RFC3339 form); parsing accepts fractional seconds either way.
func formatTS(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

package domain

import "time"

// ArtifactKind classifies an evidence attachment. Values match the
// `artifact.kind` CHECK constraint exactly: five fixed kinds from seed
// migration 0001 plus the `other` escape hatch added by migration 0002.
type ArtifactKind string

const (
	ArtifactLog           ArtifactKind = "log"
	ArtifactScreenshot    ArtifactKind = "screenshot"
	ArtifactCommandOutput ArtifactKind = "command-output"
	ArtifactTrace         ArtifactKind = "trace"
	ArtifactURL           ArtifactKind = "url"
	// ArtifactOther is the escape hatch for evidence types the fixed list does
	// not cover (added by migration 0002_artifact_other.sql).
	ArtifactOther ArtifactKind = "other"
)

// Valid reports whether k is one of the allowed artifact kinds.
func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactLog, ArtifactScreenshot, ArtifactCommandOutput, ArtifactTrace, ArtifactURL, ArtifactOther:
		return true
	default:
		return false
	}
}

// Artifact is a typed reference to evidence for a task: a URL or filesystem
// path plus a one-line summary. No uploads in the MVP. Artifacts are
// append-only (no updates in the MVP).
type Artifact struct {
	ID        ID
	TaskID    ID
	Kind      ArtifactKind
	Ref       string // URL or filesystem path
	Summary   string // one line; empty allowed
	Actor     Actor
	CreatedAt time.Time
}

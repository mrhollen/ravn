package domain

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

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

// MaxArtifactSummaryLen caps an artifact's summary in RUNES. Summaries render
// as single lines in task drawers and logs; anything longer belongs in a comment.
const MaxArtifactSummaryLen = 500

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

// Validate reports whether the artifact entity is structurally valid:
//   - Kind must be valid
//   - Ref must be non-empty after trimming
//   - If Kind == ArtifactURL, Ref must be a valid HTTP or HTTPS URL with a non-empty host
//   - Summary must not contain newline characters (\r or \n) and must not exceed MaxArtifactSummaryLen runes
func (a Artifact) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown artifact kind %q", ErrInvalid, a.Kind)
	}
	ref := strings.TrimSpace(a.Ref)
	if ref == "" {
		return fmt.Errorf("%w: artifact ref must not be empty", ErrInvalid)
	}
	if a.Kind == ArtifactURL {
		u, err := url.Parse(ref)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: artifact ref for url kind must be a valid http or https URL, got %q", ErrInvalid, a.Ref)
		}
	}
	if strings.ContainsAny(a.Summary, "\r\n") {
		return fmt.Errorf("%w: artifact summary must be a single line, found newline", ErrInvalid)
	}
	if runes := utf8.RuneCountInString(a.Summary); runes > MaxArtifactSummaryLen {
		return fmt.Errorf("%w: artifact summary is %d runes, max is %d", ErrInvalid, runes, MaxArtifactSummaryLen)
	}
	return nil
}

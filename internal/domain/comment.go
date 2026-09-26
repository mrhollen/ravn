package domain

import "time"

// CommentKind classifies a task comment so agents can filter the thread tail
// by intent. Values match the `comment.kind` CHECK constraint exactly.
type CommentKind string

const (
	CommentProgress CommentKind = "progress"
	CommentQuestion CommentKind = "question"
	CommentDecision CommentKind = "decision"
	CommentHandoff  CommentKind = "handoff"
	CommentNote     CommentKind = "note"
)

// Valid reports whether k is one of the allowed comment kinds.
func (k CommentKind) Valid() bool {
	switch k {
	case CommentProgress, CommentQuestion, CommentDecision, CommentHandoff, CommentNote:
		return true
	default:
		return false
	}
}

// Comment is a typed, immutable note attached to a task with its actor and a
// timestamp. Comments are append-only (no updates in the MVP).
type Comment struct {
	ID        ID
	TaskID    ID
	Kind      CommentKind
	Body      string // markdown
	Actor     Actor
	CreatedAt time.Time
}

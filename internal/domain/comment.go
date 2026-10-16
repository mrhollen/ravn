package domain

import (
	"fmt"
	"time"
)

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

// MaxCommentBodyLen caps a comment body's size in BYTES. Bodies are markdown
// thread entries streamed over SSE and scanned out of SQLite for every task
// detail read: byte-bounded like descriptions so a multi-MB paste cannot bloat
// the thread tail or an event frame. Comments are append-only, so this is
// checked exactly once — at add (write path only).
const MaxCommentBodyLen = 64 * 1024

// ValidateCommentBody reports whether body is within MaxCommentBodyLen bytes
// (empty allowed — emptiness itself is rejected by the caller). WRITE paths
// only (Comments.Add); see Task.ValidateTaskTitle for the legacy-row
// convention. Wraps ErrInvalid.
func ValidateCommentBody(body string) error {
	if n := len(body); n > MaxCommentBodyLen {
		return fmt.Errorf("%w: comment body is %d bytes, max is %d", ErrInvalid, n, MaxCommentBodyLen)
	}
	return nil
}

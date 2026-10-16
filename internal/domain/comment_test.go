package domain

import (
	"errors"
	"strings"
	"testing"
)

// comment_test.go: unit tests for the comment body bound. Comments are
// append-only, so the cap is enforced exactly once — at add; this pins that
// gate plus the byte (not rune) measurement.

func TestValidateCommentBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "progress: moved the row", true},
		{"empty allowed (emptiness is the caller's rule)", "", true},
		{"exactly at cap", strings.Repeat("a", MaxCommentBodyLen), true},
		{"multi-byte string measured in bytes: half the cap in runes = at cap", strings.Repeat("é", MaxCommentBodyLen/2), true},
		{"one byte over cap", strings.Repeat("a", MaxCommentBodyLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommentBody(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateCommentBody() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateCommentBody() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

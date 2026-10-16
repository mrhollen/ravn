package domain

import (
	"errors"
	"strings"
	"testing"
)

// acceptance_test.go: unit tests for the acceptance-criterion text/note
// bounds. The caps are enforced at WRITE paths only (add / reword / toggle),
// so these tests pin the per-field validators — Validate (the completion
// invariant) must keep accepting legacy rows over the caps.

func TestValidateACText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "Server returns 200 for GET /projects", true},
		{"exactly at cap", strings.Repeat("a", MaxACTextLen), true},
		{"multi-byte at cap counts runes not bytes", strings.Repeat("é", MaxACTextLen), true},
		{"one over cap", strings.Repeat("a", MaxACTextLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateACText(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateACText() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateACText() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateACNote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "verified via e2e suite (57 passed)", true},
		{"empty allowed (check-off without a note)", "", true},
		{"exactly at cap", strings.Repeat("a", MaxACNoteLen), true},
		{"one over cap", strings.Repeat("a", MaxACNoteLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateACNote(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateACNote() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateACNote() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

// TestAcceptanceCriterionValidate_ToleratesLegacyOversizedText pins the
// write-path-only contract: the completion invariant does not re-litigate
// legacy text/note values.
func TestAcceptanceCriterionValidate_ToleratesLegacyOversizedText(t *testing.T) {
	c := AcceptanceCriterion{
		ID:     "01LEGACYACID",
		TaskID: "01TASKID",
		Seq:    3,
		Text:   strings.Repeat("a", MaxACTextLen*2),
		Note:   strings.Repeat("n", MaxACNoteLen*2),
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() on legacy oversized criterion = %v, want nil (write-path-only caps)", err)
	}
}

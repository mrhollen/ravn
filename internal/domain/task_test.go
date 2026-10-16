package domain

import (
	"errors"
	"strings"
	"testing"
)

// task_test.go: unit tests for the Task string-field bounds. The caps are
// enforced at WRITE paths only, so these tests pin the per-field validators
// (what the create/update/notes use cases call) — including that a task whose
// stored values already exceed the caps still passes Validate() untouched
// (legacy-row tolerance; see ValidateTaskTitle).

func TestValidateTaskTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "Ship the hardening task", true},
		{"empty allowed (emptiness is the caller's rule)", "", true},
		{"exactly at cap", strings.Repeat("a", MaxTaskTitleLen), true},
		{"multi-byte at cap counts runes not bytes", strings.Repeat("é", MaxTaskTitleLen), true},
		{"one over cap", strings.Repeat("a", MaxTaskTitleLen+1), false},
		{"multi-byte one rune over cap", strings.Repeat("é", MaxTaskTitleLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTaskTitle(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateTaskTitle() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateTaskTitle() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateTaskDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short markdown", "## Plan\n- step one", true},
		{"empty allowed", "", true},
		{"exactly at cap", strings.Repeat("a", MaxTaskDescriptionLen), true},
		{"multi-byte string measured in bytes: half the cap in runes = at cap", strings.Repeat("é", MaxTaskDescriptionLen/2), true},
		{"one byte over cap", strings.Repeat("a", MaxTaskDescriptionLen+1), false},
		{"multi-byte one rune over byte cap", strings.Repeat("é", MaxTaskDescriptionLen/2+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTaskDescription(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateTaskDescription() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateTaskDescription() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateTaskNotes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "- found the cause\n- next: fix it", true},
		{"empty allowed (clearing notes)", "", true},
		{"exactly at cap", strings.Repeat("n", MaxTaskNotesLen), true},
		{"one byte over cap", strings.Repeat("n", MaxTaskNotesLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTaskNotes(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateTaskNotes() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateTaskNotes() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

// TestTaskValidate_ToleratesLegacyOversizedRows pins the read/mutate contract:
// Task.Validate checks structural invariants only. A task stored before the
// caps (oversized title/description/notes) must still pass Validate, because
// Tasks.Update merges onto the stored row and then validates — re-litigating
// legacy values here would make unrelated updates fail.
func TestTaskValidate_ToleratesLegacyOversizedRows(t *testing.T) {
	tk := Task{
		ID:          "01LEGACYTASKID",
		ProjectID:   "01PROJECTID",
		Title:       strings.Repeat("a", MaxTaskTitleLen*2),
		Description: strings.Repeat("d", MaxTaskDescriptionLen*2),
		Status:      StatusTodo,
		Notes:       strings.Repeat("n", MaxTaskNotesLen*2),
		Version:     7,
	}
	if err := tk.Validate(); err != nil {
		t.Fatalf("Validate() on legacy oversized row = %v, want nil (write-path-only caps)", err)
	}
}

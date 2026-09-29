package domain

import "testing"

func TestAllowedTransitionsMap(t *testing.T) {
	expected := map[Status][]Status{
		StatusTodo:  {StatusDoing},
		StatusDoing: {StatusDone, StatusTodo},
		StatusDone:  {StatusTodo},
	}

	if len(AllowedTransitions) != len(expected) {
		t.Fatalf("AllowedTransitions has %d keys, want %d", len(AllowedTransitions), len(expected))
	}

	for from, wantTargets := range expected {
		gotTargets, ok := AllowedTransitions[from]
		if !ok {
			t.Errorf("AllowedTransitions missing key %q", from)
			continue
		}
		if len(gotTargets) != len(wantTargets) {
			t.Errorf("AllowedTransitions[%q] len = %d, want %d", from, len(gotTargets), len(wantTargets))
			continue
		}
		for i, tgt := range wantTargets {
			if gotTargets[i] != tgt {
				t.Errorf("AllowedTransitions[%q][%d] = %q, want %q", from, i, gotTargets[i], tgt)
			}
		}
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from Status
		to   Status
		want bool
	}{
		// Valid transitions
		{StatusTodo, StatusDoing, true},
		{StatusDoing, StatusDone, true},
		{StatusDoing, StatusTodo, true},
		{StatusDone, StatusTodo, true},

		// Invalid transitions
		{StatusTodo, StatusDone, false},  // skipping doing
		{StatusDone, StatusDoing, false}, // must reopen to todo first

		// Self-transitions
		{StatusTodo, StatusTodo, false},
		{StatusDoing, StatusDoing, false},
		{StatusDone, StatusDone, false},

		// Unknown / empty statuses
		{"", StatusTodo, false},
		{StatusTodo, "", false},
		{"unknown", "unknown", false},
		{StatusDoing, "invalid", false},
	}

	for _, tc := range cases {
		got := CanTransition(tc.from, tc.to)
		if got != tc.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

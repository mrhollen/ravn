package domain

import (
	"testing"
	"time"
)

// identity_test.go covers the resolved-identity value types (AuthMethod, Identity,
// Subject and the status vocabulary) that scoping.go's policies reason about. It pins
// the enabled/disabled split and the unknown/zero "unresolved" forms so the storage
// boundary (GetByName → SubjectFromIdentity/UnknownSubject) cannot drift.

func TestAuthMethodValid(t *testing.T) {
	valid := []AuthMethod{AuthPassword, AuthAPI, AuthSharedToken, AuthNone}
	for _, m := range valid {
		if !m.Valid() {
			t.Errorf("AuthMethod %q should be valid", m)
		}
	}
	invalid := []AuthMethod{"", "basic", "oauth2", "PASSWORD"} // case-sensitive storage values
	for _, m := range invalid {
		if m.Valid() {
			t.Errorf("AuthMethod %q should NOT be valid", m)
		}
	}
}

func TestIdentityEnabled(t *testing.T) {
	base := Identity{ID: ID("OWNR0ID00000000000000000"), Name: "alice", Kind: ActorHuman, AuthMethod: AuthSharedToken, CreatedAt: time.Now()}
	if !base.Enabled() {
		t.Errorf("identity with nil disabled_at should be enabled")
	}
	disabled := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base.DisabledAt = &disabled
	if base.Enabled() {
		t.Errorf("identity with a disabled_at stamp should NOT be enabled")
	}
}

func TestIdentityValidate(t *testing.T) {
	ok := Identity{ID: ID("OWNR0ID00000000000000000"), Name: "alice", Kind: ActorHuman, AuthMethod: AuthSharedToken}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	cases := []struct {
		name string
		mod  func(*Identity)
	}{
		{"zero id", func(i *Identity) { i.ID = ID("") }},
		{"empty name", func(i *Identity) { i.Name = "   " }},
		{"invalid kind", func(i *Identity) { i.Kind = ActorType("robot") }},
		{"invalid auth method", func(i *Identity) { i.AuthMethod = AuthMethod("magic") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := ok
			c.mod(&i)
			if err := i.Validate(); err == nil {
				t.Errorf("%s: expected an error wrapping ErrInvalid", c.name)
			}
		})
	}
}

func TestSubjectFromIdentity(t *testing.T) {
	enabled := Identity{ID: ID("OWNR0ID00000000000000000"), Name: "alice", Kind: ActorHuman, AuthMethod: AuthPassword}
	s := SubjectFromIdentity(enabled)
	if s.Status != SubjectEnabled || !s.Enabled() || s.ID != enabled.ID || s.Name != "alice" {
		t.Errorf("SubjectFromIdentity(enabled) = %+v; want enabled subject carrying id+name", s)
	}

	disabledAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	disabled := Identity{ID: ID("OWNR0ID00000000000000000"), Name: "alice", Kind: ActorHuman, AuthMethod: AuthPassword, DisabledAt: &disabledAt}
	ds := SubjectFromIdentity(disabled)
	if ds.Status != SubjectDisabled || ds.Enabled() {
		t.Errorf("SubjectFromIdentity(disabled) = %+v; want disabled status, not enabled", ds)
	}
	// A disabled subject still carries its id so policies can attribute it.
	if ds.ID != disabled.ID {
		t.Errorf("disabled subject lost its id: got %q", ds.ID)
	}
}

func TestUnknownSubject(t *testing.T) {
	s := UnknownSubject("ghost")
	if s.Status != SubjectUnknown || s.Enabled() || !s.ID.IsZero() {
		t.Errorf("UnknownSubject = %+v; want unknown status, no id", s)
	}
	// A NAMED unknown had something asserted, so it is NOT the "unresolved" zero form —
	// it simply has no backing identity. Policies branch on Status, not Unresolved, here.
	if s.Unresolved() {
		t.Errorf("a named unknown subject should not report Unresolved (a name WAS asserted)")
	}
}

func TestSubjectUnresolved(t *testing.T) {
	var zero Subject // the zero form: nothing asserted, nothing found
	if zero.Status != SubjectUnknown || !zero.Unresolved() {
		t.Errorf("zero Subject = %+v; want unknown + unresolved", zero)
	}
	if zero.Enabled() {
		t.Errorf("zero Subject must not be enabled")
	}
	// An enabled subject is never "unresolved" even with an empty name (defensive).
	en := Subject{Name: "", Status: SubjectEnabled, ID: ID("OWNR0ID00000000000000000")}
	if en.Unresolved() {
		t.Errorf("enabled subject must not report unresolved")
	}
}

func TestIdentityStatusString(t *testing.T) {
	cases := map[IdentityStatus]string{
		SubjectUnknown:  "unknown",
		SubjectDisabled: "disabled",
		SubjectEnabled:  "enabled",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("status %v String() = %q; want %q", int(s), got, want)
		}
	}
	// An out-of-range value must not panic and falls back to unknown.
	if got := IdentityStatus(99).String(); got != "unknown" {
		t.Errorf("out-of-range status String() = %q; want fallback %q", got, "unknown")
	}
}

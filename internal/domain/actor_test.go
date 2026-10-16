package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestActorValidate covers every accepted and rejected shape of Actor.Validate
// — known type, non-empty trimmed name, the rune-count length cap (byte count
// must NOT matter for multi-byte names), and control-character rejection. This
// is the unit behind every application write-path actor rejection.
func TestActorValidate(t *testing.T) {
	maxRunes := strings.Repeat("a", MaxActorNameLen) // exactly at the cap → valid
	overCap := strings.Repeat("a", MaxActorNameLen+1)
	// Rune count, not byte count: 100 × '€' is 300 bytes but only 100 runes.
	euroAtCap := strings.Repeat("€", MaxActorNameLen)  // valid
	euroOver := strings.Repeat("€", MaxActorNameLen+1) // invalid

	cases := []struct {
		name string
		a    Actor
		ok   bool
	}{
		// Accepted.
		{"valid human", Actor{Type: ActorHuman, Name: "ada"}, true},
		{"valid agent", Actor{Type: ActorAgent, Name: "worker-7"}, true},
		{"surrounding whitespace trimmed before checking", Actor{Type: ActorHuman, Name: "  ada  "}, true},
		{"multiline-whitespace name trims to a valid one", Actor{Type: ActorAgent, Name: "\n\tbob\n"}, true},
		{"utf8 name with multibyte runes", Actor{Type: ActorAgent, Name: "böb-日本語の作業"}, true},
		{"name exactly at rune cap", Actor{Type: ActorHuman, Name: maxRunes}, true},
		{"multibyte name at rune cap (bytes overflow, runes do not)", Actor{Type: ActorAgent, Name: euroAtCap}, true},

		// Rejected: type.
		{"unknown actor type", Actor{Type: "invalid", Name: "ada"}, false},
		{"empty actor type", Actor{Type: "", Name: "ada"}, false},
		{"actor type is case-sensitive", Actor{Type: "HUMAN", Name: "ada"}, false},

		// Rejected: name emptiness.
		{"empty name", Actor{Type: ActorHuman, Name: ""}, false},
		{"whitespace-only name", Actor{Type: ActorAgent, Name: " \t\n  "}, false},

		// Rejected: rune-length cap.
		{"ascii name one rune over the cap", Actor{Type: ActorHuman, Name: overCap}, false},
		{"multibyte name one rune over the cap", Actor{Type: ActorAgent, Name: euroOver}, false},

		// Rejected: control characters (C0, DEL, C1).
		{"newline inside name", Actor{Type: ActorHuman, Name: "bad\nname"}, false},
		{"carriage return inside name", Actor{Type: ActorAgent, Name: "bad\rname"}, false},
		{"tab inside name", Actor{Type: ActorHuman, Name: "a\tb"}, false},
		{"DEL (U+007F) inside name", Actor{Type: ActorAgent, Name: "a\x7fb"}, false},
		{"C1 control U+0080 (well-formed UTF-8) inside name", Actor{Type: ActorHuman, Name: "a\xc2\x80b"}, false},
		{"invalid UTF-8 byte in name", Actor{Type: ActorHuman, Name: "a\x80b"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if tc.ok {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error for %+v", tc.a)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %q (%T), want wrapped domain.ErrInvalid (so the REST layer maps it to 400)", err, err)
			}
		})
	}
}

// TestActorValidate_ReadOnlyPredicate pins down that Validate never mutates its
// argument and only trims for the purpose of checking — a padded name passes
// but is not rewritten in place (normalization is normalizeActor's job).
func TestActorValidate_IsPurePredicate(t *testing.T) {
	a := Actor{Type: ActorHuman, Name: "  ada  "}
	if err := a.Validate(); err != nil {
		t.Fatalf("padded name must validate, got %v", err)
	}
	if a.Name != "  ada  " {
		t.Errorf("Validate mutated its receiver: Name = %q, want unchanged %q", a.Name, "  ada  ")
	}
}

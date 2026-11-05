package domain

import (
	"errors"
	"strings"
	"testing"
)

// validULID is a fixed, hand-verified well-formed ULID (26 Crockford base32
// characters, no I/L/O/U). A second constant varies the value so tests that
// need two distinct IDs do not compare an ID against itself.
const (
	validULID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	validULIDTwo = "01B2C3D4E5F6A7B8C9D0E1F2A3"
)

func TestIDValid(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		want bool
	}{
		{"canonical ULID", ID(validULID), true},
		{"second well-formed ULID", ID(validULIDTwo), true},
		{"all digits (26)", ID("00000000000000000000000000"), true},
		{"lowercase copy of a real ULID", ID(strings.ToLower(validULID)), true},
		{"mixed case", ID("01arz3ndektsv4rrffq69g5fav"), true},
		{"empty", ID(""), false},
		{"whitespace only (26 spaces)", ID(strings.Repeat(" ", 26)), false},
		{"25 chars (truncated)", ID(validULID[:25]), false},
		{"27 chars (extended)", ID(validULID + "0"), false},
		{"excluded I at position 0", ID("I" + validULID[1:]), false},
		{"excluded L in the middle", ID(validULID[:13] + "L" + validULID[14:]), false},
		{"excluded O near the end", ID(validULID[:20] + "O" + validULID[21:]), false},
		{"excluded U at the end", ID(validULID[:25] + "U"), false},
		{"lowercase excluded i", ID(strings.ToLower("I" + validULID[1:])), false},
		{"path traversal", ID("../../etc/passwd"), false},
		{"slash inside (right length)", ID(validULID[:13] + "/" + validULID[14:]), false},
		{"dot instead of base32 char", ID(validULID[:5] + "." + validULID[6:]), false},
		{"hyphen instead of base32 char", ID(validULID[:7] + "-" + validULID[8:]), false},
		{"embedded NUL byte", ID(validULID[:10] + "\x00" + validULID[11:]), false},
		{"short non-base32 word", ID("abc"), false},
		{"single char", ID("A"), false},
		{"oversized (1 MiB of A)", ID(strings.Repeat("A", 1<<20)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.Valid(); got != tc.want {
				t.Errorf("ID(%q).Valid() = %v, want %v", tc.id.String(), got, tc.want)
			}
		})
	}
}

func TestParseID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		want    ID
	}{
		{"valid canonical", validULID, false, ID(validULID)},
		{"valid lowercase (well-formed, no normalization)", strings.ToLower(validULID), false, ID(strings.ToLower(validULID))},
		{"empty", "", true, ""},
		{"spaces", "   ", true, ""},
		{"too short", validULID[:25], true, ""},
		{"too long", validULID + "0", true, ""},
		{"bad char I", "I" + validULID[1:], true, ""},
		{"path traversal", "../../etc/passwd", true, ""},
		{"oversized (1 MiB)", strings.Repeat("A", 1<<20), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseID(%q...) = %v, want domain.ErrInvalid", tc.input[:min(len(tc.input), 16)], got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("ParseID error = %v, want errors.Is(err, ErrInvalid)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseID(%q...) unexpected error: %v", tc.input[:min(len(tc.input), 16)], err)
			}
			if got != tc.want {
				t.Errorf("ParseID(...) = %q, want %q", got.String(), tc.want.String())
			}
		})
	}
}

// TestParseID_OversizedMessageStaysBounded guards the hostile-payload case: a
// multi-megabyte id must not be echoed back verbatim in the error body.
func TestParseID_OversizedMessageStaysBounded(t *testing.T) {
	_, err := ParseID(strings.Repeat("A", 1<<20))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want wrapped domain.ErrInvalid, got %v", err)
	}
	if n := len(err.Error()); n > 512 {
		t.Errorf("error message is %d bytes — the oversized input leaked into it: %q...", n, err.Error()[:min(n, 80)])
	}
}

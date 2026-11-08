package pwhash

import (
	"strings"
	"testing"
)

// FastParams keeps these hashes in a few milliseconds so the package suite stays well
// under budget while still exercising the real encode/verify round-trip.

func TestHashAndCheck_RoundTrip(t *testing.T) {
	h := New(FastParams())
	stored, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if !strings.HasPrefix(stored, "$argon2id$v=19,m=") {
		t.Fatalf("hash missing argon2id PHC prefix, got: %q", stored)
	}
	if !h.Check(stored, "correct horse battery staple") {
		t.Errorf("Check(correct password) = false, want true")
	}
}

func TestCheck_WrongPasswordIsFalse(t *testing.T) {
	h := New(FastParams())
	stored, err := h.Hash("secret-one")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if h.Check(stored, "secret-two") {
		t.Errorf("Check(wrong password) = true, want false")
	}
	if h.Check(stored, "") {
		t.Errorf("Check(empty password) = true, want false")
	}
}

func TestHash_SaltsDiffer(t *testing.T) {
	h := New(FastParams())
	a, err := h.Hash("same-password")
	if err != nil {
		t.Fatalf("Hash(a) error: %v", err)
	}
	b, err := h.Hash("same-password")
	if err != nil {
		t.Fatalf("Hash(b) error: %v", err)
	}
	if a == b {
		t.Errorf("two hashes of the same password are identical — salt is not random")
	}
	// ...but both still verify.
	if !h.Check(a, "same-password") || !h.Check(b, "same-password") {
		t.Errorf("a freshly-salted hash failed to verify")
	}
}

func TestParsePHC_Structure(t *testing.T) {
	h := New(FastParams())
	stored, err := h.Hash("pw")
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	parts := strings.SplitN(stored, "$", 5)
	if len(parts) != 5 {
		t.Fatalf("expected 5 '$'-separated segments, got %d: %q", len(parts), stored)
	}
	if parts[1] != "argon2id" {
		t.Errorf("tag = %q, want argon2id", parts[1])
	}
	// salt + key decode to the configured byte lengths.
	salt := mustB64(t, parts[3])
	key := mustB64(t, parts[4])
	if len(salt) != FastParams().SaltLen {
		t.Errorf("salt len = %d, want %d", len(salt), FastParams().SaltLen)
	}
	if len(key) != FastParams().KeyLen {
		t.Errorf("key len = %d, want %d", len(key), FastParams().KeyLen)
	}
}

func TestCheck_MalformedInputsReturnFalse(t *testing.T) {
	h := New(FastParams())
	cases := []string{
		"",                       // empty
		"not a hash at all",      // no prefix
		"$bcrypt$v=1$x$y",        // wrong tag
		"$argon2id$v=18$m=1,t=1,p=1$c2FsdA==$aGVsbG8=", // unsupported version
		"$argon2id$v=19$m=1$t=1$p=1$c2FsdA==$aGVsbG8=", // missing commas
		"$argon2id$v=19,m=1,t=1,p=1,x=9$c2FsdA==$aGVsbG8=", // unknown field
		"$argon2id$v=19$m=1,t=1,p=1$!!!not-base64==$$",     // bad salt base64
	}
	for i, c := range cases {
		if h.Check(c, "pw") {
			t.Errorf("Check(%d) malformed %q = true, want false", i, c)
		}
	}
}

func TestParsePHC_RejectsTruncated(t *testing.T) {
	if _, _, _, _, _, ok := parsePHC("$argon2id$v=19,m=1,t=1,p=1$short"); ok {
		t.Errorf("parsePHC accepted a hash missing its key segment")
	}
}

// TestCheck_VerifiesAcrossParameterChanges guards the "stored params win" rule: a hash
// minted under one cost set must still verify even when checked by a hasher with
// different parameters (we re-derive using the embedded m/t/p + digest length).
func TestCheck_VerifiesAcrossParameterChanges(t *testing.T) {
	mint := New(Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16})
	stored, err := mint.Hash("rolling password")
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	checker := New(Params{MemoryKiB: 16 * 1024, Time: 2, Threads: 2, KeyLen: 32, SaltLen: 16})
	if !checker.Check(stored, "rolling password") {
		t.Errorf("Check under different params = false, want true (stored params should win)")
	}
	if checker.Check(stored, "wrong") {
		t.Errorf("Check(wrong) under different params = true, want false")
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	// base64 decode without importing the encoder again — reuse b64.
	out, err := b64.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return out
}

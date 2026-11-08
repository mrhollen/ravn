// Package pwhash implements ravn's credential hashing with Argon2id, exposed to the
// rest of the system through application.PasswordHasher so the application layer stays
// stdlib-only (ARCHITECTURE: third-party crypto lives in infrastructure). It emits and
// verifies PHC-format strings ("$argon2id$v=19$m=…,t=…,p=…$<salt>$<hash>") that embed
// their own cost parameters, so a hash keeps verifying with the parameters it was minted
// under even if the production defaults later change.
package pwhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params configures an Argon2id hash. Memory is in KiB (the unit argon2.IDKey takes);
// Time is the number of passes; Threads the parallelism; KeyLen/SaltLen are byte counts.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	KeyLen    int
	SaltLen   int
}

// DefaultParams are the production cost parameters: 64 MiB, 3 passes, 4 threads. They
// keep a single hash well under ~100ms on a modern core while staying comfortably above
// OWASP-recommended Argon2id floors.
func DefaultParams() Params {
	return Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 4, KeyLen: 32, SaltLen: 16}
}

// FastParams are for tests only: a low cost so the suite hashes quickly. Never use for
// production credentials — they exist to keep TestXxx fast while exercising the full
// encode/verify path.
func FastParams() Params {
	return Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}
}

// Hasher hashes and verifies passwords with Argon2id using fixed cost parameters. It is
// safe for concurrent use (stateless apart from its immutable Params).
type Hasher struct {
	p Params
}

// New returns a Hasher configured with p. A zero value is tolerated: it behaves like
// DefaultParams() so a misconfigured caller still gets sane (if slow) security rather
// than an unusable hash.
func New(p Params) *Hasher {
	if p.MemoryKiB == 0 || p.Time == 0 || p.Threads == 0 || p.KeyLen == 0 || p.SaltLen == 0 {
		return &Hasher{p: DefaultParams()}
	}
	return &Hasher{p: p}
}

var (
	b64 = base64.RawURLEncoding // PHC strings use unpadded base64url for salt and hash

	// prefix is the fixed tag of a ravn credential hash. We only ever accept our own
	// format — no BCRYPT/BLAKE2/other-argon2 variants are understood, so parsePHC
	// rejects anything else outright.
	prefix = "$argon2id$"
)

var errMalformed = errors.New("pwhash: malformed hash")

// Hash returns a PHC-format string encoding the Argon2id digest of password under the
// hasher's parameters, with a fresh random salt each call (so equal passwords produce
// different strings).
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("pwhash: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.p.Time, h.p.MemoryKiB, h.p.Threads, uint32(h.p.KeyLen))
	return h.encode(salt, key), nil
}

// Check reports whether password verifies against storedHash (a PHC string produced by
// Hash). It is constant-time over the derived key and returns false — not an error — for
// any malformed or non-argon2id input, so callers get a uniform "wrong password" answer.
func (h *Hasher) Check(storedHash, password string) bool {
	salt, key, memKiB, t, threads, ok := parsePHC(storedHash)
	if !ok {
		return false
	}
	// Derive with the parameters stored in the hash (not h.p), using the stored digest
	// length — so a hash minted under different cost/length settings still verifies.
	candidate := argon2.IDKey([]byte(password), salt, t, memKiB, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1
}

func (h *Hasher) encode(salt, key []byte) string {
	return prefix +
		"v=" + strconv.Itoa(int(argon2.Version)) +
		",m=" + strconv.FormatUint(uint64(h.p.MemoryKiB), 10) +
		",t=" + strconv.FormatUint(uint64(h.p.Time), 10) +
		",p=" + strconv.Itoa(int(h.p.Threads)) +
		"$" + b64.EncodeToString(salt) +
		"$" + b64.EncodeToString(key)
}

// parsePHC decodes a "$argon2id$v=19$m=…,t=…,p=…$<salt>$<hash>" string into its salt,
// key and the cost parameters needed to re-derive it. Any deviation from that exact
// shape (wrong tag, wrong version, missing/extra fields, bad base64) yields ok=false.
func parsePHC(stored string) (salt, key []byte, memKiB, time uint32, threads uint8, ok bool) {
	parts := strings.SplitN(stored, "$", 5)
	if len(parts) != 5 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, false
	}

	var version int
	fields := map[string]string{}
	for _, kv := range strings.Split(parts[2], ",") {
		eq := strings.SplitN(kv, "=", 2)
		if len(eq) != 2 || eq[0] == "" || eq[1] == "" {
			return nil, nil, 0, 0, 0, false
		}
		switch eq[0] {
		case "v":
			n, err := strconv.Atoi(eq[1])
			if err != nil || n != int(argon2.Version) {
				return nil, nil, 0, 0, 0, false
			}
			version = n
		case "m":
			fields["m"] = eq[1]
		case "t":
			fields["t"] = eq[1]
		case "p":
			fields["p"] = eq[1]
		default:
			return nil, nil, 0, 0, 0, false // unknown field — not a format we produced
		}
	}
	if version == 0 {
		return nil, nil, 0, 0, 0, false
	}
	mm, err := strconv.ParseUint(fields["m"], 10, 32)
	if err != nil {
		return nil, nil, 0, 0, 0, false
	}
	tt, err := strconv.ParseUint(fields["t"], 10, 32)
	if err != nil || tt == 0 {
		return nil, nil, 0, 0, 0, false
	}
	pp, err := strconv.ParseUint(fields["p"], 10, 8)
	if err != nil || pp == 0 {
		return nil, nil, 0, 0, 0, false
	}

	salt, err = b64.DecodeString(parts[3])
	if err != nil || len(salt) == 0 {
		return nil, nil, 0, 0, 0, false
	}
	key, err = b64.DecodeString(parts[4])
	if err != nil || len(key) == 0 {
		return nil, nil, 0, 0, 0, false
	}
	return salt, key, uint32(mm), uint32(tt), uint8(pp), true
}

// ensure the sentinel is referenced even if a build trims error wrapping.
var _ = errMalformed

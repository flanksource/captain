package captaintoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrMalformed is the single answer to every unparseable credential. Callers
// map it to 401 without echoing which part was wrong.
var ErrMalformed = errors.New("malformed captain token")

// argon2id parameters. Tuned down from the interactive-login defaults on
// purpose: the secret being protected is 256 bits of crypto/rand, not a
// human-chosen password, so the KDF exists to slow an attacker who has already
// stolen the database rather than to resist guessing. Verification also sits on
// the request path — git smart-HTTP makes several requests per push — so the
// cost has to stay bounded. The verification cache in verify.go is what keeps a
// burst from paying this repeatedly.
const (
	argonTime    = 1
	argonMemory  = 19 * 1024 // 19 MiB — the OWASP minimum for argon2id
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// hashPrefix identifies the encoding, so a future parameter change can be
// recognized and rehashed rather than silently mis-verified.
const hashPrefix = "$argon2id$v=19"

// HashSecret derives the stored form of a token secret.
//
// The encoding carries its own parameters, so a row hashed under today's cost
// still verifies after the constants above change.
func HashSecret(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate token salt: %w", err)
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("%s$m=%d,t=%d,p=%d$%s$%s",
		hashPrefix, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifySecret checks a secret against an encoded hash in constant time.
//
// A malformed hash verifies as false rather than erroring: it can only come
// from a corrupt row, and a caller that treated "unreadable" differently from
// "wrong" would turn database damage into an authentication bypass.
func VerifySecret(secret, encoded string) bool {
	memory, time, threads, salt, want, ok := parseHash(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(secret), salt, time, memory, threads, uint32(len(want)))
	return constantTimeEqual(string(got), string(want))
}

func parseHash(encoded string) (memory, time uint32, threads uint8, salt, key []byte, ok bool) {
	if !strings.HasPrefix(encoded, hashPrefix+"$") {
		return 0, 0, 0, nil, nil, false
	}
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 {
		return 0, 0, 0, nil, nil, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return 0, 0, 0, nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return 0, 0, 0, nil, nil, false
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 || memory == 0 || time == 0 || threads == 0 {
		return 0, 0, 0, nil, nil, false
	}
	return memory, time, threads, salt, key, true
}

// fastDigest is a non-reversible key for the in-memory verification cache. It
// is SHA-256 rather than argon2 precisely because it must be cheap — it never
// leaves the process and never reaches storage, so it protects only against a
// heap dump exposing the plaintext.
func fastDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

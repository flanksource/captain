// Package captaintoken mints and verifies the bearer credentials that reach a
// captain server over the network.
//
// A token is durable rather than single-use. The git-agent join token it
// replaces was burned on first redemption, which meant a restarting sidecar
// replayed a spent token and crash-looped for the life of the workload — the
// reason pkg/cli/gitagent_serve.go carries a joinOnce guard at all. Bounding a
// credential by expiry and revocation, instead of by one use, removes the whole
// class of problem.
//
// Only the hash is ever stored. Verification is deliberately two-step: an
// indexed lookup on the public id, then a constant-time KDF check of the
// secret. A scan comparing secrets with == would leak timing information.
package captaintoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/clicky/text"
)

// Prefix marks a captain token in logs, config files and secret scanners. It is
// part of the wire format so a leaked credential is recognizable on sight.
const Prefix = "cptn"

// separator divides the public id from the secret.
const separator = "."

const (
	idBytes     = 9  // 12 base64 chars: enough to be unique, short enough to read
	secretBytes = 32 // 256 bits; the KDF guards a stolen database, not guessing
)

// Scope is what a token may reach. It mirrors the captain_api_token_scope enum.
type Scope string

const (
	// ScopeGit authorizes pushing to a served repository and nothing else. An
	// agent token gets this, so a leaked one cannot reach the /api/v1 executor
	// and run arbitrary captain commands.
	ScopeGit Scope = "git"
	// ScopeAPI authorizes the HTTP API.
	ScopeAPI Scope = "api"
)

// ParseScope validates a scope selector, naming the alternatives.
func ParseScope(value string) (Scope, error) {
	switch scope := Scope(strings.ToLower(strings.TrimSpace(value))); scope {
	case ScopeGit, ScopeAPI:
		return scope, nil
	case "":
		return "", fmt.Errorf("a token scope is required; want one of: %s, %s", ScopeGit, ScopeAPI)
	default:
		return "", fmt.Errorf("invalid token scope %q; want one of: %s, %s", value, ScopeGit, ScopeAPI)
	}
}

// Valid reports whether s names a scope.
func (s Scope) Valid() bool { return s == ScopeGit || s == ScopeAPI }

// nameRe is the shape a token name must take. It is the §3.2 ref-segment shape
// deliberately, not by coincidence: a git-scoped token's name becomes an agent
// name, and an agent name becomes a path segment in the ref namespace that R8.3
// uses to keep one agent out of another's refs. A name that cannot be a ref
// segment would be rejected far downstream, at push time.
var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// ValidateName checks a token or agent name against that shape.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name %q must match %s", name, nameRe)
	}
	return nil
}

// Minted is a freshly created token: the public id to store, the hash to store,
// and the one-time plaintext to hand the operator.
type Minted struct {
	// ID is the public half. It is stored, indexed, logged, and shown in
	// listings.
	ID string
	// Hash is the argon2id encoding of the secret half, safe at rest.
	Hash string
	// Secret is the whole credential, id included. It exists only in this
	// struct and is never stored — reveal it once or it is lost.
	Secret text.SensitiveString
}

// Mint generates a token. The returned Secret is the only time the plaintext
// exists; nothing derived from it can reconstruct it.
func Mint() (Minted, error) {
	id, err := randomBase64(idBytes)
	if err != nil {
		return Minted{}, fmt.Errorf("generate token id: %w", err)
	}
	secret, err := randomBase64(secretBytes)
	if err != nil {
		return Minted{}, fmt.Errorf("generate token secret: %w", err)
	}
	hash, err := HashSecret(secret)
	if err != nil {
		return Minted{}, err
	}
	return Minted{
		ID:     id,
		Hash:   hash,
		Secret: text.NewSensitiveString(Prefix + "_" + id + separator + secret),
	}, nil
}

// Presented is a token as it arrived from a client, split but not yet verified.
type Presented struct {
	ID     string
	secret string
}

// Parse splits a presented credential into its public and secret halves.
//
// It reports a single generic error for every malformed shape: telling a caller
// which part of a credential was wrong is a probing aid, and none of the
// distinctions help a legitimate client that simply has the token.
func Parse(raw string) (Presented, error) {
	trimmed := strings.TrimSpace(raw)
	body, ok := strings.CutPrefix(trimmed, Prefix+"_")
	if !ok {
		return Presented{}, ErrMalformed
	}
	id, secret, ok := strings.Cut(body, separator)
	if !ok || id == "" || secret == "" {
		return Presented{}, ErrMalformed
	}
	return Presented{ID: id, secret: secret}, nil
}

// Verify checks the presented secret against a stored hash in constant time.
func (p Presented) Verify(storedHash string) bool {
	return VerifySecret(p.secret, storedHash)
}

// CacheKey derives a stable, non-reversible key for a verification cache, so a
// repeated request can skip the KDF without the cache holding the secret.
func (p Presented) CacheKey() string { return p.ID + separator + fastDigest(p.secret) }

// BearerFromHeader extracts the credential from an Authorization header,
// reporting whether one was present at all.
func BearerFromHeader(header string) (string, bool) {
	const scheme = "bearer "
	trimmed := strings.TrimSpace(header)
	if len(trimmed) < len(scheme) || !strings.EqualFold(trimmed[:len(scheme)], scheme) {
		return "", false
	}
	value := strings.TrimSpace(trimmed[len(scheme):])
	return value, value != ""
}

func randomBase64(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// constantTimeEqual compares two strings without leaking their contents through
// timing. Length is not secret here — both sides are fixed-width encodings.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

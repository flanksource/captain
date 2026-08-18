// The credential a sidecar issues to its supervisor.
//
// Over ssh the supervisor authenticates by public key, and the sidecar
// authorizes it by recording the fingerprint the enrollment handed back. Over
// https there is no key exchange to authenticate anyone, so the same trust has
// to be carried by a bearer token — and it points the same way: the sidecar is
// the party that will verify it, so the sidecar is the party that mints it.

package gitagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/clicky/text"
)

// DispatchCredentialName is the verifier a sidecar keeps beside its keys.
const DispatchCredentialName = "dispatch_token.json"

// DispatchCredential is the verifier half of the bearer token a sidecar issues
// to its supervisor at enrollment.
//
// Only the argon2id hash is stored. The plaintext exists exactly once, in the
// enrollment request, and nothing on this host can reconstruct it — the same
// guarantee the supervisor's own token store makes about the tokens it mints.
type DispatchCredential struct {
	TokenID    string    `json:"tokenId"`
	SecretHash string    `json:"secretHash"`
	IssuedAt   time.Time `json:"issuedAt"`
}

// MintDispatchCredential issues the token, persists the verifier at path, and
// returns the plaintext for the caller to hand over.
//
// The verifier is written BEFORE the caller may speak, so this host never names
// a credential it has not already committed to accepting. The mirrored order
// would leave a supervisor holding a token this endpoint rejects, surfacing as a
// 403 on the first dispatch rather than here.
func MintDispatchCredential(path string) (text.SensitiveString, error) {
	minted, err := captaintoken.Mint()
	if err != nil {
		return "", err
	}
	credential := DispatchCredential{
		TokenID:    minted.ID,
		SecretHash: minted.Hash,
		IssuedAt:   time.Now().UTC(),
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, append(encoded, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("persist the dispatch credential at %s: %w", path, err)
	}
	return minted.Secret, nil
}

// LoadDispatchCredential reads the verifier.
//
// A missing or unreadable file is an error naming the path, never an empty
// credential: an endpoint that verified nothing would accept every push.
func LoadDispatchCredential(path string) (*DispatchCredential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the dispatch credential %s: %w", path, err)
	}
	var credential DispatchCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		return nil, fmt.Errorf("the dispatch credential %s is not readable JSON: %w", path, err)
	}
	if credential.TokenID == "" || credential.SecretHash == "" {
		return nil, fmt.Errorf("the dispatch credential %s names no token, so nothing could be verified against it", path)
	}
	return &credential, nil
}

// Verifier resolves a presented bearer against this credential, as the agent.
//
// It reuses the supervisor's own credential machinery over a single in-memory
// record — nothing in captaintoken.Verifier is database-bound. That buys three
// things a hand-rolled compare would not: the public id is matched first, so a
// flood of random credentials against an endpoint now reachable from outside the
// cluster costs a string compare rather than 19 MiB and ~60ms of argon2 apiece;
// the KDF cache means the several HTTP requests git makes for one push pay it
// once; and the secret compare is the same constant-time path the supervisor
// uses.
//
// No expiry is set. This credential is reissued on every enrollment, which is
// every restart of the workload, so an expiry could only brick a pod that had
// been running longer than someone guessed it would.
func (c *DispatchCredential) Verifier(agent string) *captaintoken.Verifier {
	record := captaintoken.Record{
		ID:         c.TokenID,
		SecretHash: c.SecretHash,
		Name:       agent,
		Agent:      agent,
		Scope:      captaintoken.ScopeGit,
	}
	return captaintoken.NewVerifier(func(_ context.Context, id string) (captaintoken.Record, error) {
		if id != c.TokenID {
			return captaintoken.Record{}, captaintoken.ErrUnknown
		}
		return record, nil
	})
}

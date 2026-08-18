// Where an agent keeps the captain token it authenticates with.
//
// The token lives on the host that presents it and never travels in dispatch
// state, exactly like the SSH key it parallels — hooks.json carries the path,
// not the credential.

package gitagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky/text"
)

// TokenFileName is the agent's own credential, beside its keys.
const TokenFileName = "token"

// DefaultTokenPath is where an agent stores the token it was enrolled with.
func DefaultTokenPath() (string, error) {
	keysDir, err := DefaultKeysDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(keysDir, TokenFileName), nil
}

// WriteTokenFile stores a credential readable only by its owner.
func WriteTokenFile(path string, token text.SensitiveString) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("a token path is required")
	}
	if token.IsEmpty() {
		return fmt.Errorf("refusing to write an empty captain token to %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(token.Value()+"\n"), 0o600)
}

// ReadTokenFile loads the credential at path.
//
// An empty path is an error rather than an empty token: a push that reached
// this point over https needs a credential, and continuing without one would
// fail at the server as a 401 that looks like a revocation.
func ReadTokenFile(path string) (text.SensitiveString, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("this endpoint is reached over https, which needs a captain token, but none is configured for it")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read captain token %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("captain token file %s is empty", path)
	}
	return text.NewSensitiveString(token), nil
}

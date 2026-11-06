package credproxy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewSecret returns a random credential for one proxy: what a client must
// present before a loopback proxy lends it the credentials it holds.
func NewSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("proxy secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

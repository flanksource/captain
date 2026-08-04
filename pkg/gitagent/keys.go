// Key material (§8): each agent authenticates with its own keypair, generated
// where it will live and never transiting a terminal, clipboard or log (R8.2).
package gitagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	gossh "golang.org/x/crypto/ssh"
)

// EnsureKeyPair loads the OpenSSH private key at path, generating an ed25519
// pair (0600, parent 0700) on first use. It returns the signer and the
// SHA256: fingerprint of the public half.
func EnsureKeyPair(path string) (gossh.Signer, string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return generateKeyPair(path)
	}
	if err != nil {
		return nil, "", err
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return nil, "", fmt.Errorf("parse key %s: %w", path, err)
	}
	return signer, gossh.FingerprintSHA256(signer.PublicKey()), nil
}

func generateKeyPair(path string) (gossh.Signer, string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, "", err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := gossh.MarshalPrivateKey(priv, "captain-gitagent")
	if err != nil {
		return nil, "", err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, "", err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", err
	}
	pub := gossh.MarshalAuthorizedKey(signer.PublicKey())
	if err := writeFileAtomic(path+".pub", pub, 0o644); err != nil {
		return nil, "", err
	}
	return signer, gossh.FingerprintSHA256(signer.PublicKey()), nil
}

package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	SourceVault       = "captain-vault"
	SourceEnvironment = "environment"
)

type Resolved struct {
	Token  string
	Source string
	Detail string
}

type Vault struct {
	path string
}

var pathOverride string

func SetPathForTesting(path string) {
	pathOverride = path
}

func Path() (string, error) {
	if pathOverride != "" {
		return pathOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "captain", "vault"), nil
}

func DefaultVault() (Vault, error) {
	path, err := Path()
	if err != nil {
		return Vault{}, err
	}
	return NewVault(path), nil
}

func NewVault(path string) Vault {
	return Vault{path: path}
}

func (v Vault) Path() string {
	return v.path
}

func (v Vault) Load() (map[string]string, error) {
	data, err := os.ReadFile(v.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credential vault %s: %w", v.path, err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parse credential vault %s: %w", v.path, err)
	}
	return values, nil
}

func (v Vault) Resolve(provider string, envVars []string, getenv func(string) string) (Resolved, error) {
	values, err := v.Load()
	if err != nil {
		return Resolved{}, err
	}
	if token := strings.TrimSpace(values[provider]); token != "" {
		return Resolved{Token: token, Source: SourceVault}, nil
	}
	for _, envVar := range envVars {
		if token := strings.TrimSpace(getenv(envVar)); token != "" {
			return Resolved{Token: token, Source: SourceEnvironment, Detail: envVar}, nil
		}
	}
	return Resolved{}, nil
}

func (v Vault) Set(provider, token string) error {
	provider = strings.TrimSpace(provider)
	token = strings.TrimSpace(token)
	if provider == "" {
		return fmt.Errorf("credential provider cannot be empty")
	}
	if token == "" {
		return fmt.Errorf("credential token cannot be empty")
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(v.path), 0o700); err != nil {
		return fmt.Errorf("secure credential directory: %w", err)
	}

	lock, err := os.OpenFile(v.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open credential vault lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock credential vault: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	values, err := v.Load()
	if err != nil {
		return err
	}
	values[provider] = token
	return v.write(values)
}

func (v Vault) write(values map[string]string) error {
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential vault: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(v.path), ".vault-*")
	if err != nil {
		return fmt.Errorf("create credential vault temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure credential vault temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credential vault: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync credential vault: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close credential vault: %w", err)
	}
	if err := os.Rename(tmpPath, v.path); err != nil {
		return fmt.Errorf("replace credential vault: %w", err)
	}
	return nil
}

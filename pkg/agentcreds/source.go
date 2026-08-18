package agentcreds

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// keychainService is the generic-password item Claude Code writes on macOS.
// On every other platform the same JSON document lives in a file instead.
const keychainService = "Claude Code-credentials"

// Reader resolves raw credential documents from the host. The function fields
// exist so tests can drive redaction and expiry without a Keychain or a real
// home directory; OSReader wires them to the host.
type Reader struct {
	Home string
	// ReadFile reads a credential file.
	ReadFile func(path string) ([]byte, error)
	// ReadKeychain reads the Claude Code Keychain item. Nil on platforms that
	// have no Keychain, which is how Read chooses the file path instead.
	ReadKeychain func(ctx context.Context) ([]byte, error)
	// Now supplies the clock for credentials whose expiry is relative.
	Now func() time.Time
}

// OSReader wires a Reader to this host.
func OSReader() (Reader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Reader{}, fmt.Errorf("resolve home directory: %w", err)
	}
	reader := Reader{Home: home, ReadFile: os.ReadFile, Now: time.Now}
	if runtime.GOOS == "darwin" {
		reader.ReadKeychain = readKeychainItem
	}
	return reader, nil
}

// ClaudePath is where Claude Code keeps its credential file when it is not
// using a Keychain.
func (r Reader) ClaudePath() string {
	return filepath.Join(r.Home, ".claude", ".credentials.json")
}

// CodexPath is where codex keeps its credential file on every platform.
func (r Reader) CodexPath() string {
	return filepath.Join(r.Home, ".codex", "auth.json")
}

// Read returns one provider's redacted credential.
//
// A missing or unreadable source is an error rather than an empty result: the
// callers publish credentials into sandboxes, and a silently-skipped provider
// would surface as an unexplained 401 inside an agent hours later.
func (r Reader) Read(ctx context.Context, provider Provider) (Credential, error) {
	switch provider {
	case ProviderClaude:
		raw, err := r.readClaude(ctx)
		if err != nil {
			return Credential{}, err
		}
		return RedactClaude(raw)
	case ProviderCodex:
		raw, err := r.ReadFile(r.CodexPath())
		if err != nil {
			return Credential{}, describeMissing(err, provider, r.CodexPath(), "codex login")
		}
		return RedactCodex(raw, r.now())
	default:
		return Credential{}, fmt.Errorf("unknown credential provider %q", provider)
	}
}

// ReadAll returns the redacted credentials for every requested provider,
// failing on the first that cannot be read.
func (r Reader) ReadAll(ctx context.Context, providers []Provider) ([]Credential, error) {
	out := make([]Credential, 0, len(providers))
	for _, provider := range providers {
		credential, err := r.Read(ctx, provider)
		if err != nil {
			return nil, err
		}
		out = append(out, credential)
	}
	return out, nil
}

func (r Reader) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// readClaude prefers the Keychain where one exists and falls back to the file,
// because a single machine can have either: macOS Claude Code uses the
// Keychain, while a Linux or container install writes the same document to
// ~/.claude/.credentials.json.
func (r Reader) readClaude(ctx context.Context) ([]byte, error) {
	if r.ReadKeychain != nil {
		raw, keychainErr := r.ReadKeychain(ctx)
		if keychainErr == nil {
			return raw, nil
		}
		raw, fileErr := r.ReadFile(r.ClaudePath())
		if fileErr == nil {
			return raw, nil
		}
		return nil, fmt.Errorf(
			"no claude login found: keychain item %q: %w; %s: %v",
			keychainService, keychainErr, r.ClaudePath(), fileErr)
	}
	raw, err := r.ReadFile(r.ClaudePath())
	if err != nil {
		// `claude`, not `claude login`: Claude Code has no login subcommand, it
		// starts the login flow when run unauthenticated.
		return nil, describeMissing(err, ProviderClaude, r.ClaudePath(), "claude")
	}
	return raw, nil
}

// describeMissing turns a bare "no such file" into something that names the fix.
func describeMissing(err error, provider Provider, path, loginCommand string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no %s credential at %s; run `%s` on this host", provider, path, loginCommand)
	}
	return fmt.Errorf("read %s credential %s: %w", provider, path, err)
}

// readKeychainItem shells out to /usr/bin/security, which is the only supported
// way to read a generic-password item without linking a Cgo Keychain binding.
//
// The value reaches captain on stdout, so it never lands in argv where another
// process could read it from /proc or `ps`.
func readKeychainItem(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security",
		"find-generic-password", "-s", keychainService, "-w")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("read keychain item %q: %s", keychainService, detail)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, fmt.Errorf("keychain item %q is empty", keychainService)
	}
	return []byte(trimmed), nil
}

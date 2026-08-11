package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// modelCacheTTL bounds how long a persisted resolve is reused before the
// resolver re-queries live model lists.
const modelCacheTTL = 24 * time.Hour

// ModelCache is one persisted merged model view. KeyFingerprint is a canonical,
// non-secret identity covering the resolution schema, backend, effective model
// endpoint, and a machine-keyed HMAC of each exact credential used.
type ModelCache struct {
	Timestamp      time.Time       `json:"timestamp"`
	KeyFingerprint string          `json:"keyFingerprint"`
	Models         []ResolvedModel `json:"models"`
}

func (c *ModelCache) expired() bool {
	return time.Since(c.Timestamp) >= modelCacheTTL
}

const modelCacheHMACKeySize = 32

var (
	modelCacheKeyMu   sync.Mutex
	modelCacheHMACKey = loadOrCreateModelCacheHMACKey
)

func modelCacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "captain")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	// Never reuse the legacy single-slot cache because it cannot be associated
	// with one credential. Tighten it in place so private model IDs left by an
	// older Captain are no longer world-readable.
	legacy := filepath.Join(dir, "models.json")
	if info, statErr := os.Lstat(legacy); statErr == nil && info.Mode().IsRegular() {
		_ = os.Chmod(legacy, 0o600)
	}
	return dir, nil
}

func modelCacheDir() (string, error) {
	root, err := modelCacheRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "models")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func modelCachePath(fingerprint string) (string, error) {
	dir, err := modelCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fingerprint+".json"), nil
}

// lockModelCache serializes one cache identity across goroutines and Captain
// processes. The lock spans cache re-check, live fetch, and publish so a slow
// stale fetch cannot overwrite a newer refresh with a fresh timestamp.
func lockModelCache(ctx context.Context, fingerprint string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := modelCacheDir()
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, fingerprint+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = lock.Close()
			return nil, err
		}
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				_ = lock.Close()
				return nil, err
			}
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func loadModelCache(fingerprint string) (*ModelCache, error) {
	path, err := modelCachePath(fingerprint)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("model cache entry is not a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c ModelCache
	if err := json.Unmarshal(data, &c); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &c, nil
}

// saveModelCache atomically writes the merged view (tmp + rename).
func saveModelCache(fingerprint string, models []ResolvedModel) error {
	path, err := modelCachePath(fingerprint)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ModelCache{
		Timestamp:      time.Now(),
		KeyFingerprint: fingerprint,
		Models:         models,
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteModelCache(path, data)
}

func atomicWriteModelCache(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func loadOrCreateModelCacheHMACKey() ([]byte, error) {
	modelCacheKeyMu.Lock()
	defer modelCacheKeyMu.Unlock()
	root, err := modelCacheRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "model-cache.key")
	if key, err := readModelCacheHMACKey(path); err == nil {
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	key := make([]byte, modelCacheHMACKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate model cache identity key: %w", err)
	}
	tmp, err := os.CreateTemp(root, ".model-cache-key-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// A hard link publishes a fully written key without replacing a key another
	// Captain process may have created concurrently. If the filesystem cannot
	// provide this guarantee, callers disable token-bearing disk caching.
	if err := os.Link(tmpPath, path); err != nil {
		if key, readErr := readModelCacheHMACKey(path); readErr == nil {
			return key, nil
		}
		return nil, fmt.Errorf("publish model cache identity key: %w", err)
	}
	return readModelCacheHMACKey(path)
}

func readModelCacheHMACKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("model cache identity key is not a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != modelCacheHMACKeySize {
		return nil, fmt.Errorf("model cache identity key has invalid length")
	}
	return key, nil
}

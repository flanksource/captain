package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// modelCacheTTL bounds how long a persisted resolve is reused before the
// resolver re-queries live model lists.
const modelCacheTTL = 24 * time.Hour

// ModelCache is the persisted merged model view written to
// ~/.config/captain/models.json. KeyFingerprint records which resolve produced
// it (backend filter + token use + which API keys were present) so a changed
// environment re-resolves instead of serving a stale live view.
type ModelCache struct {
	Timestamp      time.Time       `json:"timestamp"`
	KeyFingerprint string          `json:"keyFingerprint"`
	Models         []ResolvedModel `json:"models"`
}

func (c *ModelCache) expired() bool {
	return time.Since(c.Timestamp) >= modelCacheTTL
}

// modelCachePath returns ~/.config/captain/models.json, creating the directory.
func modelCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "captain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "models.json"), nil
}

func loadModelCache() (*ModelCache, error) {
	path, err := modelCachePath()
	if err != nil {
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
	path, err := modelCachePath()
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

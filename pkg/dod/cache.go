package dod

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

type CommandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Passed   bool   `json:"passed"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type LastRun struct {
	At      time.Time       `json:"at"`
	Results []CommandResult `json:"results"`
}

type DodFile struct {
	Commands  []string  `json:"commands"`
	Workdir   string    `json:"workdir"`
	Timeout   int       `json:"timeout"`
	CreatedAt time.Time `json:"created_at"`
	LastRun   *LastRun  `json:"last_run,omitempty"`
}

func GetDodDir() string {
	return filepath.Join(claude.GetClaudeHome(), "dod")
}

func CachePath(sessionID string) string {
	return filepath.Join(GetDodDir(), sessionID+".json")
}

func Read(sessionID string) (*DodFile, error) {
	data, err := os.ReadFile(CachePath(sessionID))
	if err != nil {
		return nil, err
	}
	var dod DodFile
	if err := json.Unmarshal(data, &dod); err != nil {
		return nil, fmt.Errorf("corrupt dod file: %w", err)
	}
	return &dod, nil
}

func Write(sessionID string, dod *DodFile) error {
	dir := GetDodDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating dod dir: %w", err)
	}

	data, err := json.MarshalIndent(dod, "", "  ")
	if err != nil {
		return err
	}

	tmp := CachePath(sessionID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, CachePath(sessionID))
}

func Delete(sessionID string) error {
	path := CachePath(sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Exists(sessionID string) bool {
	_, err := os.Stat(CachePath(sessionID))
	return err == nil
}

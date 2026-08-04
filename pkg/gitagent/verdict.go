// Verdicts (§7.1): the machine record of one tier's decision. A verdict is
// persisted outside git before pre-receive exits non-zero, because quarantine
// discards every object of a rejected push (R6.9).
package gitagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// VerdictStatus is accepted | rejected | error. error means the tier could
// not reach a verdict — and an indeterminate verdict rejects (R7.5).
type VerdictStatus string

const (
	StatusAccepted VerdictStatus = "accepted"
	StatusRejected VerdictStatus = "rejected"
	StatusError    VerdictStatus = "error"
)

// Finding is one hook's contribution to a verdict.
type Finding struct {
	Hook     string `json:"hook"`
	Kind     string `json:"kind"` // exec | commit | prompt | fixture
	Path     string `json:"path,omitempty"`
	Message  string `json:"message,omitempty"`
	Feedback string `json:"feedback,omitempty"`
}

// TierVerdict is the verdict.json payload.
type TierVerdict struct {
	V        int           `json:"v"`
	Task     string        `json:"task"`
	Attempt  int           `json:"attempt"`
	Status   VerdictStatus `json:"status"`
	Tier     string        `json:"tier"` // "sidecar" | "supervisor"
	Findings []Finding     `json:"findings,omitempty"`
}

// Rejects reports whether the push must be refused. Only an explicit accept
// passes: error is indeterminate and indeterminate rejects (R7.5).
func (v TierVerdict) Rejects() bool { return v.Status != StatusAccepted }

func verdictPath(repo, task string, attempt int) string {
	return filepath.Join(taskStateDir(repo, task), "verdicts", strconv.Itoa(attempt)+".json")
}

// SaveVerdict persists v under <repo>/captain/tasks/<task>/verdicts/, keyed
// by attempt, atomically — the out-of-band record a rejected push leaves
// behind and the channel async relay reports through (R6.9).
func SaveVerdict(repo string, v TierVerdict) error {
	if err := ValidateTaskID(v.Task); err != nil {
		return err
	}
	if v.Attempt < 1 {
		return fmt.Errorf("verdict attempt %d must be positive", v.Attempt)
	}
	path := verdictPath(repo, v.Task, v.Attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// LoadVerdict reads a persisted verdict; ok is false when none exists.
func LoadVerdict(repo, task string, attempt int) (*TierVerdict, bool, error) {
	if err := ValidateTaskID(task); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(verdictPath(repo, task, attempt))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var v TierVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, false, fmt.Errorf("verdict %s/%d: %w", task, attempt, err)
	}
	return &v, true, nil
}

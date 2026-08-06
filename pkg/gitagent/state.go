// Receiver-side task state, kept under <repo>/captain/ — outside the object
// store, because a rejected push must leave zero new objects and refs while
// the verdict still has to survive (R6.9).

package gitagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Policy is the policy.json payload referenced by a control ref (§4).
type Policy struct {
	Paths       []string `json:"paths,omitempty"`
	MaxAttempts int      `json:"maxAttempts,omitempty"`
	MaxBlobSize int64    `json:"maxBlobSize,omitempty"`
}

// TaskState records what a receiver knows about a dispatched task.
type TaskState struct {
	Task           string    `json:"task"`
	Agent          string    `json:"agent,omitempty"`
	Base           string    `json:"base"`
	DispatchCommit string    `json:"dispatchCommit"`
	ControlCommit  string    `json:"controlCommit,omitempty"` // the dispatched control payloads
	Attempts       int       `json:"attempts"`                // highest attempt seen
	Relay          RelayMode `json:"relay,omitempty"`
	Policy         Policy    `json:"policy"`
	Hooks          *HookSets `json:"hooks,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func taskStateDir(repo, task string) string {
	return filepath.Join(repo, "captain", "tasks", task)
}

// LoadTaskState reads a task's state; ok is false when the task is unknown.
func LoadTaskState(repo, task string) (*TaskState, bool, error) {
	if err := ValidateTaskID(task); err != nil {
		return nil, false, err
	}
	return loadTaskStateUnlocked(repo, task)
}

func loadTaskStateUnlocked(repo, task string) (*TaskState, bool, error) {
	data, err := os.ReadFile(filepath.Join(taskStateDir(repo, task), "state.json"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var st TaskState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, false, fmt.Errorf("task %s state: %w", task, err)
	}
	return &st, true, nil
}

// SaveTaskState persists st atomically.
func SaveTaskState(repo string, st *TaskState) error {
	if err := ValidateTaskID(st.Task); err != nil {
		return err
	}
	dir := taskStateDir(repo, st.Task)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return withFileLock(filepath.Join(dir, "state.lock"), 0o600, func() error {
		return saveTaskStateUnlocked(repo, st)
	})
}

// UpdateTaskState holds the task lock across load, mutation, and save. The
// callback returns false when it inspected state but intentionally made no
// durable change.
func UpdateTaskState(repo, task string, update func(*TaskState) (bool, error)) (*TaskState, error) {
	if err := ValidateTaskID(task); err != nil {
		return nil, err
	}
	dir := taskStateDir(repo, task)
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	var result *TaskState
	err := withFileLock(filepath.Join(dir, "state.lock"), 0o600, func() error {
		st, ok, err := loadTaskStateUnlocked(repo, task)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("task %s state is missing", task)
		}
		save, err := update(st)
		if err != nil {
			return err
		}
		result = st
		if !save {
			return nil
		}
		return saveTaskStateUnlocked(repo, st)
	})
	return result, err
}

func saveTaskStateUnlocked(repo string, st *TaskState) error {
	st.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(taskStateDir(repo, st.Task), "state.json"), append(data, '\n'), 0o644)
}

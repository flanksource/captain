// Verdicts (§7.1): the machine record of one tier's decision. A verdict is
// persisted outside git before pre-receive exits non-zero, because quarantine
// discards every object of a rejected push (R6.9).
package gitagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	Terminal bool          `json:"terminal,omitempty"`
	Findings []Finding     `json:"findings,omitempty"`
}

// Rejects reports whether the push must be refused. Only an explicit accept
// passes: error is indeterminate and indeterminate rejects (R7.5).
func (v TierVerdict) Rejects() bool { return v.Status != StatusAccepted }

// readRelayedVerdict decodes the only verdict a worker may send directly:
// a terminal sidecar error for a run that ended before it could push code.
func readRelayedVerdict(ctx context.Context, repo string, env []string, commit string, info RefInfo) (TierVerdict, error) {
	var verdict TierVerdict
	parents, err := runGit(ctx, repo, env, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return verdict, err
	}
	if fields := strings.Fields(parents); len(fields) != 1 || fields[0] != commit {
		return verdict, fmt.Errorf("relayed verdict must be a parentless control commit")
	}
	tree, err := runGitRaw(ctx, repo, env, nil, "ls-tree", "-z", commit)
	if err != nil {
		return verdict, err
	}
	if !strings.HasPrefix(tree, "100644 blob ") || !strings.HasSuffix(tree, "\t"+ControlVerdictFile+"\x00") || strings.Count(tree, "\x00") != 1 {
		return verdict, fmt.Errorf("relayed verdict control commit must contain only %s", ControlVerdictFile)
	}
	sizeText, err := runGit(ctx, repo, env, "cat-file", "-s", commit+":"+ControlVerdictFile)
	if err != nil {
		return verdict, fmt.Errorf("read relayed verdict size: %w", err)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return verdict, fmt.Errorf("read relayed verdict size %q: %w", sizeText, err)
	}
	if size > MaxFeedbackBytes {
		return verdict, fmt.Errorf("relayed verdict is %d bytes, over the %d-byte cap", size, MaxFeedbackBytes)
	}
	raw, err := ReadControlPayload(ctx, repo, env, commit, ControlVerdictFile)
	if err != nil {
		return verdict, fmt.Errorf("read relayed verdict: %w", err)
	}
	if len(raw) > MaxFeedbackBytes {
		return verdict, fmt.Errorf("relayed verdict is %d bytes, over the %d-byte cap", len(raw), MaxFeedbackBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&verdict); err != nil {
		return verdict, fmt.Errorf("decode relayed verdict: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return verdict, fmt.Errorf("decode relayed verdict: %w", err)
	}
	if verdict.V != ProtocolVersion {
		return verdict, fmt.Errorf("relayed verdict version %d does not match protocol version %d", verdict.V, ProtocolVersion)
	}
	if verdict.Task != info.Task || verdict.Attempt != info.Attempt {
		return verdict, fmt.Errorf("relayed verdict %s attempt %d disagrees with ref %s attempt %d",
			verdict.Task, verdict.Attempt, info.Task, info.Attempt)
	}
	if verdict.Tier != string(RoleSidecar) || verdict.Status != StatusError || !verdict.Terminal {
		return verdict, fmt.Errorf("relayed verdict must be a terminal sidecar error")
	}
	if len(verdict.Findings) == 0 {
		return verdict, fmt.Errorf("relayed verdict carries no failure finding")
	}
	return verdict, nil
}

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

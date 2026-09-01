// Snapshot reads of a receiver's task tree, for consumers that persist history
// rather than tail it.
//
// This is deliberately separate from the sidecar's log monitor
// (pkg/cli/gitagent_task_logs.go), which is incremental and stateful: it holds
// byte offsets so it can emit only newly-appended output to a terminal. An
// ingest pass wants the opposite — a complete, stateless picture it can upsert
// idempotently, so a re-scan after a crash converges instead of replaying. One
// shape cannot serve both without carrying the other's baggage.

package gitagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// TaskSnapshot is everything a receiver knows about one task at one instant.
type TaskSnapshot struct {
	Task string
	// State is nil when the directory exists but state.json has not landed yet
	// (or is being rewritten), which is normal mid-dispatch.
	State *TaskState
	// Verdicts are every tier decision found, ordered by attempt then tier.
	Verdicts []TierVerdict
}

// Concluded reports the terminal outcome of a task, if it has one.
//
// The filesystem never records "this task is over", so it is derived: an
// accepted verdict at the supervisor tier ends the task, as does an explicitly
// terminal worker error or a non-accepted verdict once the attempt budget is
// spent. Anything else is still in flight — a rejection with attempts
// remaining is explicitly not terminal (SPEC-git-agent-protocol §6.3,
// "rejection is not termination").
func (s TaskSnapshot) Concluded() (VerdictStatus, bool) {
	final, ok := s.finalVerdict()
	if !ok {
		return "", false
	}
	if final.Status == StatusAccepted {
		return StatusAccepted, true
	}
	if final.Terminal {
		return final.Status, true
	}
	budget := 0
	if s.State != nil {
		budget = s.State.Policy.MaxAttempts
	}
	if budget > 0 && final.Attempt >= budget {
		return final.Status, true
	}
	return "", false
}

// finalVerdict is the highest-attempt supervisor decision, falling back to the
// highest-attempt verdict of any tier when the supervisor has not spoken.
func (s TaskSnapshot) finalVerdict() (TierVerdict, bool) {
	var best TierVerdict
	found := false
	for _, verdict := range s.Verdicts {
		if found && verdict.Attempt < best.Attempt {
			continue
		}
		// At equal attempt the supervisor's decision is the one that counts:
		// it is the tier that integrates.
		if found && verdict.Attempt == best.Attempt && best.Tier == "supervisor" {
			continue
		}
		best, found = verdict, true
	}
	return best, found
}

// IntegratedBranch is the branch an accepted task was integrated onto, taken
// from the integrate hook's finding.
func (s TaskSnapshot) IntegratedBranch() string {
	for _, verdict := range s.Verdicts {
		for _, finding := range verdict.Findings {
			if finding.Hook == "integrate" && finding.Path != "" {
				return finding.Path
			}
		}
	}
	return ""
}

// Feedback is the first message the concluding verdict carried, for display.
func (v TierVerdict) Feedback() string {
	for _, finding := range v.Findings {
		if finding.Feedback != "" {
			return finding.Feedback
		}
		if finding.Message != "" {
			return finding.Message
		}
	}
	return ""
}

// ScanTasks reads every task recorded under a receiver repository. A repository
// with no task tree yields no snapshots and no error — that is the normal state
// of a mailbox nothing has been dispatched to yet.
func ScanTasks(repo string) ([]TaskSnapshot, error) {
	root := filepath.Join(repo, "captain", "tasks")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshots := make([]TaskSnapshot, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// A directory whose name is not a valid task id is not ours; skip it
		// rather than failing the whole scan.
		if err := ValidateTaskID(entry.Name()); err != nil {
			continue
		}
		snapshot, err := ScanTask(repo, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Task < snapshots[j].Task })
	return snapshots, nil
}

// ScanTask reads one task's state and every verdict recorded for it.
func ScanTask(repo, task string) (TaskSnapshot, error) {
	if err := ValidateTaskID(task); err != nil {
		return TaskSnapshot{}, err
	}
	snapshot := TaskSnapshot{Task: task}
	state, found, err := LoadTaskState(repo, task)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if found {
		snapshot.State = state
	}
	verdicts, err := scanVerdictDir(repo, task)
	if err != nil {
		return TaskSnapshot{}, err
	}
	snapshot.Verdicts = verdicts
	return snapshot, nil
}

func scanVerdictDir(repo, task string) ([]TierVerdict, error) {
	entries, err := os.ReadDir(filepath.Join(taskStateDir(repo, task), "verdicts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	verdicts := make([]TierVerdict, 0, len(entries))
	for _, entry := range entries {
		attempt, ok := VerdictAttempt(entry.Name())
		if !ok {
			continue
		}
		verdict, found, err := LoadVerdict(repo, task, attempt)
		if err != nil {
			return nil, err
		}
		if !found || verdict == nil {
			continue
		}
		verdicts = append(verdicts, *verdict)
	}
	sort.Slice(verdicts, func(i, j int) bool {
		if verdicts[i].Attempt != verdicts[j].Attempt {
			return verdicts[i].Attempt < verdicts[j].Attempt
		}
		return verdicts[i].Tier < verdicts[j].Tier
	})
	return verdicts, nil
}

// VerdictAttempt parses "3.json" into the attempt number it records.
func VerdictAttempt(name string) (int, bool) {
	trimmed := strings.TrimSuffix(name, ".json")
	if trimmed == name {
		return 0, false
	}
	attempt, err := strconv.Atoi(trimmed)
	if err != nil || attempt < 1 {
		return 0, false
	}
	return attempt, true
}

// The fixed on-disk layout every git-agent host uses, so enrollment, dispatch
// and ingest agree on where key material and repositories live without
// configuration.
const (
	// KeysDirName is the per-host directory beside the config file.
	KeysDirName = ".captain"
	// SandboxDirName holds this host's sandbox state under KeysDirName.
	SandboxDirName = "sandbox"
	// ServedReposDirName is the served root under the sandbox directory.
	ServedReposDirName = "repos"
)

// DefaultKeysDir anchors key material beside the config file: with the default
// ~/.captain.yaml this is ~/.captain/sandbox. Tests that redirect the config
// path get an isolated keys dir for free.
func DefaultKeysDir() (string, error) {
	path, err := captainconfig.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), KeysDirName, SandboxDirName), nil
}

// DefaultServedRoot is where a receiver keeps the repositories it serves.
func DefaultServedRoot() (string, error) {
	keysDir, err := DefaultKeysDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(keysDir, ServedReposDirName), nil
}

// ServedRootFor resolves a backend's served root, honouring an explicit
// mailboxRoot option and falling back to the default layout.
func ServedRootFor(options map[string]any) (string, error) {
	if root, _ := options["mailboxRoot"].(string); strings.TrimSpace(root) != "" {
		return strings.TrimSpace(root), nil
	}
	return DefaultServedRoot()
}

// ScanMailboxes lists the receiver repositories under a served root. Each is a
// bare mailbox serving one canonical repository.
func ScanMailboxes(servedRoot string) ([]Mailbox, error) {
	entries, err := os.ReadDir(filepath.Join(servedRoot, MailboxesDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mailboxes := make([]Mailbox, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		route := MailboxesDir + "/" + entry.Name()
		if err := ValidateMailboxRoute(route); err != nil {
			continue
		}
		mailbox := Mailbox{
			Path:  filepath.Join(servedRoot, MailboxesDir, entry.Name()),
			Route: route,
		}
		// The binding names the repository accepted work integrates into. A
		// mailbox that has not been bound yet is still worth reporting; its
		// tasks simply have no repository to display.
		if binding, err := LoadMailboxBinding(mailbox.Path); err == nil {
			mailbox.Repository = binding.Repository
		}
		mailboxes = append(mailboxes, mailbox)
	}
	sort.Slice(mailboxes, func(i, j int) bool { return mailboxes[i].Route < mailboxes[j].Route })
	return mailboxes, nil
}

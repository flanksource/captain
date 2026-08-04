// Package gitagent implements the git-agent protocol v1: dispatching a unit
// of work to a coding agent on another machine and vetting the result using
// nothing but git (issue #40, SPEC-git-agent-protocol).
//
// This file is the ref layer — pure data, no git invocation. The two hops are
// deliberately asymmetric: the agent pushes an ordinary branch
// (refs/heads/captain/<task>), while the sidecar↔supervisor hop is an
// append-only audit trail under refs/captain/tasks/.
package gitagent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// taskRefPrefix roots the machine-to-machine audit refs. These land in a
	// mailbox repo, never the user's working repository (R2.1).
	taskRefPrefix = "refs/captain/tasks/"

	// agentBranchPrefix roots the agent↔sidecar hop: a plain branch the agent
	// pushes with no protocol awareness (R3.1).
	agentBranchPrefix = "refs/heads/captain/"

	// MaxAttempt bounds attempt numbers well above any real retry budget so a
	// hostile ref name cannot drive integer growth.
	MaxAttempt = 999999
)

// RefKind names the four protocol ref roles under a task namespace.
type RefKind string

const (
	RefDispatch RefKind = "dispatch" // code: supervisor's snapshot, parent = base
	RefControl  RefKind = "control"  // control: task.json / hooks.json / policy.json
	RefResult   RefKind = "result"   // code: the agent's work, parent = dispatch
	RefVerdict  RefKind = "verdict"  // control: verdict.json + log
)

var refKinds = map[RefKind]bool{
	RefDispatch: true,
	RefControl:  true,
	RefResult:   true,
	RefVerdict:  true,
}

var (
	taskIDRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	// Attempts are positive decimals with no leading zeros (§3.2).
	attemptRe = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
)

// ValidateTaskID enforces the §3.2 task-id shape: ^[a-z0-9-]{1,64}$.
func ValidateTaskID(task string) error {
	if !taskIDRe.MatchString(task) {
		return fmt.Errorf("task id %q must match %s", task, taskIDRe)
	}
	return nil
}

// ParseAttempt parses an attempt number: a positive decimal integer with no
// leading zeros, bounded by MaxAttempt.
func ParseAttempt(s string) (int, error) {
	if !attemptRe.MatchString(s) {
		return 0, fmt.Errorf("attempt %q must be a positive integer with no leading zeros", s)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxAttempt {
		return 0, fmt.Errorf("attempt %q out of range [1,%d]", s, MaxAttempt)
	}
	return n, nil
}

// RefInfo is a parsed protocol ref.
type RefInfo struct {
	Task    string
	Kind    RefKind
	Attempt int
}

// TaskRef builds refs/captain/tasks/<task>/<kind>/<attempt>, validating each
// component.
func TaskRef(task string, kind RefKind, attempt int) (string, error) {
	if err := ValidateTaskID(task); err != nil {
		return "", err
	}
	if !refKinds[kind] {
		return "", fmt.Errorf("unknown protocol ref kind %q", kind)
	}
	if attempt < 1 || attempt > MaxAttempt {
		return "", fmt.Errorf("attempt %d out of range [1,%d]", attempt, MaxAttempt)
	}
	return taskRefPrefix + task + "/" + string(kind) + "/" + strconv.Itoa(attempt), nil
}

// DispatchRef, ControlRef, ResultRef and VerdictRef are the four TaskRef
// specializations.
func DispatchRef(task string, attempt int) (string, error) {
	return TaskRef(task, RefDispatch, attempt)
}

// ControlRef builds the control ref for an attempt.
func ControlRef(task string, attempt int) (string, error) { return TaskRef(task, RefControl, attempt) }

// ResultRef builds the result ref for an attempt.
func ResultRef(task string, attempt int) (string, error) { return TaskRef(task, RefResult, attempt) }

// VerdictRef builds the verdict ref for an attempt.
func VerdictRef(task string, attempt int) (string, error) { return TaskRef(task, RefVerdict, attempt) }

// AgentBranch builds refs/heads/captain/<task>, the ordinary branch the agent
// pushes (R3.1).
func AgentBranch(task string) (string, error) {
	if err := ValidateTaskID(task); err != nil {
		return "", err
	}
	return agentBranchPrefix + task, nil
}

// IsProtocolRef reports whether ref lies under refs/captain/tasks/.
func IsProtocolRef(ref string) bool {
	return strings.HasPrefix(ref, taskRefPrefix)
}

// ParseTaskRef parses a refs/captain/tasks/<task>/<kind>/<attempt> name,
// rejecting anything malformed.
func ParseTaskRef(ref string) (RefInfo, error) {
	rest, found := strings.CutPrefix(ref, taskRefPrefix)
	if !found {
		return RefInfo{}, fmt.Errorf("ref %q is not a protocol ref (missing %s)", ref, taskRefPrefix)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return RefInfo{}, fmt.Errorf("ref %q must be %s<task>/<kind>/<attempt>", ref, taskRefPrefix)
	}
	if err := ValidateTaskID(parts[0]); err != nil {
		return RefInfo{}, fmt.Errorf("ref %q: %w", ref, err)
	}
	kind := RefKind(parts[1])
	if !refKinds[kind] {
		return RefInfo{}, fmt.Errorf("ref %q: unknown protocol ref kind %q", ref, parts[1])
	}
	attempt, err := ParseAttempt(parts[2])
	if err != nil {
		return RefInfo{}, fmt.Errorf("ref %q: %w", ref, err)
	}
	return RefInfo{Task: parts[0], Kind: kind, Attempt: attempt}, nil
}

// TaskNamespace returns the ref namespace owned by a task:
// refs/captain/tasks/<task> (no trailing separator).
func TaskNamespace(task string) string {
	return taskRefPrefix + task
}

// NamespaceContains reports whether ref lies inside namespace. The comparison
// appends the separator before matching (R8.3): bare prefix matching would let
// agent "a" write agent "ab"'s namespace (H11).
func NamespaceContains(namespace, ref string) bool {
	namespace = strings.TrimSuffix(namespace, "/")
	if namespace == "" {
		return false
	}
	return strings.HasPrefix(ref, namespace+"/")
}

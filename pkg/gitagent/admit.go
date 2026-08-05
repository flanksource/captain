// Admission (§6.2 step 7): the sub-second, pure-data tier of pre-receive.
// Everything here is checkable from ref names, the envelope, task state and
// object metadata — no tree is materialized and no hook is run.
package gitagent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai/agent/commit"
)

// RefUpdate is one "old new ref" line of the pre-receive stdin.
type RefUpdate struct {
	Old, New, Ref string
}

// IsCreate reports whether the update creates the ref.
func (u RefUpdate) IsCreate() bool { return isZeroOID(u.Old) }

// IsDelete reports whether the update deletes the ref.
func (u RefUpdate) IsDelete() bool { return isZeroOID(u.New) }

// ParseRefUpdates reads pre-receive stdin lines.
func ParseRefUpdates(r io.Reader) ([]RefUpdate, error) {
	var updates []RefUpdate
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unparseable ref update line %q", line)
		}
		if err := ValidateOID(fields[0]); err != nil {
			return nil, fmt.Errorf("ref update %q: old: %w", line, err)
		}
		if err := ValidateOID(fields[1]); err != nil {
			return nil, fmt.Errorf("ref update %q: new: %w", line, err)
		}
		updates = append(updates, RefUpdate{Old: fields[0], New: fields[1], Ref: fields[2]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

// AdmitRequest carries everything the admission tier may consult.
type AdmitRequest struct {
	Repo     string
	Role     ReceiverRole
	Agent    string // authenticated agent identity; "" for an unauthenticated local push
	Updates  []RefUpdate
	Envelope *Envelope // nil when the push carried none (an agent's bare push)
	Env      []string  // hook environment — keeps quarantine object dirs readable
}

// Admit accepts or rejects a push. The error message is the rejection reason
// shown to the pusher.
func Admit(ctx context.Context, req AdmitRequest) error {
	if len(req.Updates) == 0 {
		return fmt.Errorf("push updates no refs")
	}
	protocol := map[string]RefInfo{}
	for _, u := range req.Updates {
		switch {
		case IsProtocolRef(u.Ref):
			info, err := admitProtocolRef(req, u)
			if err != nil {
				return err
			}
			protocol[u.Ref] = info
		case req.Role == RoleSidecar && strings.HasPrefix(u.Ref, agentBranchPrefix):
			if err := admitAgentBranch(ctx, req, u); err != nil {
				return err
			}
		default:
			return fmt.Errorf("ref %s is outside the protocol namespaces this receiver accepts", u.Ref)
		}
	}
	if err := requireAtomicPairs(ctx, req, protocol); err != nil {
		return err
	}
	for ref, info := range protocol {
		if err := admitCodeContent(ctx, req, refUpdateFor(req.Updates, ref), info); err != nil {
			return err
		}
	}
	return nil
}

// admitProtocolRef checks the pure-name properties of one protocol ref
// update: shape, create-only (R3.2), role allowlist, envelope agreement
// (R4.1), and namespace ownership (R8.3).
func admitProtocolRef(req AdmitRequest, u RefUpdate) (RefInfo, error) {
	info, err := ParseTaskRef(u.Ref)
	if err != nil {
		return RefInfo{}, err
	}
	if u.IsDelete() {
		return RefInfo{}, fmt.Errorf("ref %s: protocol refs cannot be deleted (R3.2)", u.Ref)
	}
	if !u.IsCreate() {
		return RefInfo{}, fmt.Errorf("ref %s already exists; every protocol ref push is a create (R3.2)", u.Ref)
	}
	allowed := map[ReceiverRole][]RefKind{
		RoleSidecar: {RefDispatch, RefControl},
		RoleMailbox: {RefResult, RefControl},
	}[req.Role]
	if !containsKind(allowed, info.Kind) {
		return RefInfo{}, fmt.Errorf("ref %s: a %s does not accept %s refs over the wire", u.Ref, req.Role, info.Kind)
	}
	if req.Envelope == nil {
		return RefInfo{}, fmt.Errorf("ref %s: protocol ref pushes require the control envelope in push options (R4.1)", u.Ref)
	}
	if err := req.Envelope.MatchesRef(info); err != nil {
		return RefInfo{}, err
	}
	if !NamespaceContains(TaskNamespace(info.Task), u.Ref) {
		return RefInfo{}, fmt.Errorf("ref %s escapes its task namespace", u.Ref)
	}
	// Task ownership binds on the mailbox, where results arrive from enrolled
	// agents; a sidecar's dispatch refs are what CREATE the task there.
	if req.Role == RoleMailbox && req.Agent != "" {
		st, ok, err := LoadTaskState(req.Repo, info.Task)
		if err != nil {
			return RefInfo{}, err
		}
		if !ok {
			return RefInfo{}, fmt.Errorf("task %s was never dispatched here", info.Task)
		}
		if st.Agent != req.Agent {
			return RefInfo{}, fmt.Errorf("agent %q cannot write task %s, which belongs to agent %q (R8.3)", req.Agent, info.Task, st.Agent)
		}
		if st.Policy.MaxAttempts > 0 && info.Attempt > st.Policy.MaxAttempts {
			return RefInfo{}, fmt.Errorf("attempt %d exceeds the task's maxAttempts %d", info.Attempt, st.Policy.MaxAttempts)
		}
	}
	return info, nil
}

// admitAgentBranch checks an agent's bare push to refs/heads/captain/<task>:
// the task must have been dispatched here, deletes are refused, and updates
// must be fast-forward.
func admitAgentBranch(ctx context.Context, req AdmitRequest, u RefUpdate) error {
	task := strings.TrimPrefix(u.Ref, agentBranchPrefix)
	if err := ValidateTaskID(task); err != nil {
		return fmt.Errorf("ref %s: %w", u.Ref, err)
	}
	if u.IsDelete() {
		return fmt.Errorf("ref %s: the task branch cannot be deleted", u.Ref)
	}
	st, ok, err := LoadTaskState(req.Repo, task)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task %s was never dispatched to this sidecar", task)
	}
	if !u.IsCreate() {
		code, _, err := gitExitCode(ctx, req.Repo, req.Env, "merge-base", "--is-ancestor", u.Old, u.New)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("ref %s: non-fast-forward push rejected; fetch and rebase onto your branch tip", u.Ref)
		}
	}
	return admitContent(ctx, req, st, st.DispatchCommit, u.New)
}

// requireAtomicPairs enforces R3.4: a code ref is unprocessable without its
// control ref. The control may travel in the same atomic push or already be
// present at the receiver — git filters an up-to-date control out of a push,
// so attempt 1's relay legitimately arrives result-only when the supervisor
// audit-wrote the identical control commit at dispatch.
func requireAtomicPairs(ctx context.Context, req AdmitRequest, protocol map[string]RefInfo) error {
	if len(protocol) == 0 {
		return nil
	}
	type key struct {
		task    string
		attempt int
	}
	kinds := map[key]map[RefKind]bool{}
	for _, info := range protocol {
		k := key{info.Task, info.Attempt}
		if kinds[k] == nil {
			kinds[k] = map[RefKind]bool{}
		}
		kinds[k][info.Kind] = true
	}
	for k, present := range kinds {
		code := present[RefDispatch] || present[RefResult]
		if code && !present[RefControl] && !controlRefExists(ctx, req, k.task, k.attempt) {
			return fmt.Errorf("task %s attempt %d: a code ref without its control ref is unprocessable (R3.4)", k.task, k.attempt)
		}
		if present[RefControl] && !code {
			return fmt.Errorf("task %s attempt %d: a control ref must travel with its code ref (R3.4)", k.task, k.attempt)
		}
	}
	return nil
}

func controlRefExists(ctx context.Context, req AdmitRequest, task string, attempt int) bool {
	ref, err := ControlRef(task, attempt)
	if err != nil {
		return false
	}
	code, _, err := gitExitCode(ctx, req.Repo, req.Env, "rev-parse", "--verify", "--quiet", ref)
	return err == nil && code == 0
}

// admitCodeContent runs the content checks that need object access: result
// parentage, blob caps and name gates. Control refs carry no worktree code.
func admitCodeContent(ctx context.Context, req AdmitRequest, u RefUpdate, info RefInfo) error {
	if info.Kind != RefResult {
		return nil
	}
	st, ok, err := LoadTaskState(req.Repo, info.Task)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task %s was never dispatched here", info.Task)
	}
	if req.Envelope == nil || req.Envelope.Base != st.Base {
		got := ""
		if req.Envelope != nil {
			got = req.Envelope.Base
		}
		return fmt.Errorf("task %s: envelope base %s does not match dispatched base %s", info.Task, got, st.Base)
	}
	parents, err := runGit(ctx, req.Repo, req.Env, "rev-list", "--parents", "-n", "1", u.New)
	if err != nil {
		return err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[1] != st.DispatchCommit {
		return fmt.Errorf("result %s must be parented on its dispatch %s", u.New, st.DispatchCommit)
	}
	return admitContent(ctx, req, st, st.DispatchCommit, u.New)
}

// admitContent applies the pure-data content gates over old..new: path
// policy, secret-shaped names, and blob size caps. It stats nothing on disk —
// the tree is not materialized at this tier.
func admitContent(ctx context.Context, req AdmitRequest, st *TaskState, from, to string) error {
	out, err := runGitRaw(ctx, req.Repo, req.Env, nil,
		"diff-tree", "-r", "-z", "--name-only", "--no-commit-id", "--no-renames", from, to)
	if err != nil {
		return err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	kept, err := filterPolicyPaths(paths, st.Policy.Paths)
	if err != nil {
		return err
	}
	if len(kept) != len(paths) {
		denied := diffPaths(paths, kept)
		return fmt.Errorf("gate:path-denied %s", strings.Join(denied, " "))
	}
	for _, p := range paths {
		if commit.LooksSecret(p) {
			return fmt.Errorf("gate:secret-name %s looks like a credential; the push is rejected (A5.4)", p)
		}
	}
	return admitBlobCaps(ctx, req, st, to)
}

func admitBlobCaps(ctx context.Context, req AdmitRequest, st *TaskState, tip string) error {
	maxBlob := st.Policy.MaxBlobSize
	if maxBlob == 0 {
		maxBlob = DefaultSnapshotMaxFileSize
	}
	objects, err := runGitRaw(ctx, req.Repo, req.Env, nil,
		"rev-list", "--objects", tip, "--not", "--all", "--alternate-refs")
	if err != nil {
		return err
	}
	var oids strings.Builder
	for _, line := range strings.Split(objects, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			oids.WriteString(fields[0])
			oids.WriteByte('\n')
		}
	}
	if oids.Len() == 0 {
		return nil
	}
	sizes, err := runGitIn(ctx, req.Repo, req.Env, strings.NewReader(oids.String()),
		"cat-file", "--batch-check=%(objecttype) %(objectsize) %(objectname)")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(sizes, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "blob" {
			size, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return fmt.Errorf("gate:blob-size cannot read the size of object %s: %w", fields[2], err)
			}
			if size > maxBlob {
				return fmt.Errorf("gate:blob-size object %s is %d bytes, over the %d-byte cap", fields[2], size, maxBlob)
			}
		}
	}
	return nil
}

func containsKind(kinds []RefKind, k RefKind) bool {
	for _, kind := range kinds {
		if kind == k {
			return true
		}
	}
	return false
}

func refUpdateFor(updates []RefUpdate, ref string) RefUpdate {
	for _, u := range updates {
		if u.Ref == ref {
			return u
		}
	}
	return RefUpdate{}
}

func diffPaths(all, kept []string) []string {
	keep := map[string]bool{}
	for _, p := range kept {
		keep[p] = true
	}
	var out []string
	for _, p := range all {
		if !keep[p] {
			out = append(out, p)
		}
	}
	return out
}

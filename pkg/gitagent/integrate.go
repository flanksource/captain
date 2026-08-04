// Integration (§10): an accepted result merges three-way against the base
// recorded in the envelope — never against current HEAD, which may have moved
// during the task (R10.1). Conflicts become structured feedback; nothing is
// auto-resolved (R10.2).
package gitagent

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// IntegrationResult reports where an accepted result landed.
type IntegrationResult struct {
	Branch   string // captain/<task> in the real repository
	Commit   string // the merge commit the branch points at
	Conflict string // non-empty when the merge conflicted; Branch is then unset
}

// Integrate merges result into the real repository's HEAD using base as the
// merge base and parks the outcome on refs/heads/captain/<task>, leaving the
// user's worktree and current branch untouched. mailbox is fetched first: the
// result's objects arrived there, and the real repository has no alternates
// pointing back (R2.1 keeps that arrow one-way).
func Integrate(ctx context.Context, realRepo, mailbox, task string, attempt int, base, result string) (*IntegrationResult, error) {
	branch, err := AgentBranch(task)
	if err != nil {
		return nil, err
	}
	env := ScrubGitEnv(os.Environ())
	resultRef, err := ResultRef(task, attempt)
	if err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, realRepo, env, "fetch", "--quiet", "--no-write-fetch-head", mailbox, resultRef); err != nil {
		return nil, err
	}
	head, err := runGit(ctx, realRepo, env, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	code, out, err := gitExitCode(ctx, realRepo, env,
		"merge-tree", "--write-tree", "--merge-base="+base, head, result)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	switch {
	case code == 1:
		// Content conflicts: the first line is the conflict-markered tree,
		// the rest describes the conflicted paths.
		detail := "merge conflict"
		if len(lines) > 1 {
			detail = "merge conflict in: " + strings.Join(lines[1:], ", ")
		}
		return &IntegrationResult{Conflict: detail}, nil
	case code != 0 || len(lines) == 0:
		return nil, fmt.Errorf("merge-tree failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	tree := strings.TrimSpace(lines[0])
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	merge, err := runGitIn(ctx, realRepo, cenv,
		strings.NewReader(fmt.Sprintf("captain: integrate task %s\n", task)),
		"commit-tree", tree, "-p", head, "-p", result)
	if err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, realRepo, env, "update-ref", branch, merge); err != nil {
		return nil, err
	}
	return &IntegrationResult{Branch: branch, Commit: merge}, nil
}

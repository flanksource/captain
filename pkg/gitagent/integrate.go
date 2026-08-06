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

type integrationPlan struct {
	head, tree, conflict string
}

// checkIntegration evaluates the merge while the result is still quarantined.
// The mailbox can see both the quarantine and the real repository's objects,
// so pre-receive can reject a conflict before git reports push success.
func checkIntegration(ctx context.Context, realRepo, mailbox, base, result string, env []string) (string, error) {
	head, err := runGit(ctx, realRepo, ScrubGitEnv(env), "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	plan, err := prepareIntegration(ctx, mailbox, env, head, base, result)
	if err != nil {
		return "", err
	}
	return plan.conflict, nil
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
	plan, err := prepareIntegration(ctx, realRepo, env, head, base, result)
	if err != nil {
		return nil, err
	}
	if plan.conflict != "" {
		return &IntegrationResult{Conflict: plan.conflict}, nil
	}
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	merge, err := runGitIn(ctx, realRepo, cenv,
		strings.NewReader(fmt.Sprintf("captain: integrate task %s\n", task)),
		"commit-tree", plan.tree, "-p", plan.head, "-p", result)
	if err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, realRepo, env, "update-ref", branch, merge); err != nil {
		return nil, err
	}
	return &IntegrationResult{Branch: branch, Commit: merge}, nil
}

// prepareIntegration computes a three-way merge tree without updating refs.
// objectRepo must be able to read head, base, and result; during pre-receive
// that is the mailbox with its quarantine environment intact.
func prepareIntegration(ctx context.Context, objectRepo string, env []string, head, base, result string) (*integrationPlan, error) {
	// Older supported Git releases do not have merge-tree's --merge-base
	// option. Give the current HEAD tree a synthetic parent at the envelope's
	// recorded base instead: merge-tree then computes that exact base from the
	// graph while leaving the user's real history and checkout untouched.
	headTree, err := runGit(ctx, objectRepo, env, "rev-parse", "--verify", head+"^{tree}")
	if err != nil {
		return nil, err
	}
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	mergeHead, err := runGitIn(ctx, objectRepo, cenv,
		strings.NewReader("captain: integration merge input\n"),
		"commit-tree", headTree, "-p", base)
	if err != nil {
		return nil, err
	}
	code, out, err := gitExitCode(ctx, objectRepo, env,
		"merge-tree", "--write-tree", mergeHead, result)
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
		return &integrationPlan{head: head, conflict: detail}, nil
	case code != 0 || len(lines) == 0:
		return nil, fmt.Errorf("merge-tree failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	return &integrationPlan{head: head, tree: strings.TrimSpace(lines[0])}, nil
}

package commit

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// record folds what a cut actually committed into the run's workspace and
// updates the chain state: the first commit of a fixup/amend run becomes the
// anchor everything after it folds into.
//
// The record is read from git (pre..HEAD) rather than built from sha and
// plan.Subject: a host pipeline may cut several commits under messages of its
// own and hand back only the last. sha still drives the anchor, so it has to be
// one of the commits read — anything else would anchor the chain on a commit
// the record does not hold.
func (h *Hook) record(hc *agent.HookContext, plan Plan, pre, sha string, err error) error {
	if err != nil {
		return err
	}
	if sha == "" {
		// The paths resolved but the pipeline staged nothing (an earlier phase
		// already took them). Said out loud because the "committing" line above
		// has already promised a commit. A HEAD that moved anyway means the cut
		// committed without reporting it — recording nothing would silently drop
		// those commits from the run.
		head, err := headSHA(plan.Dir)
		if err != nil {
			return err
		}
		if head != pre {
			return fmt.Errorf("commit: the %s cut reported no commit, but HEAD moved from %q to %s in %s",
				plan.action(), pre, head, plan.Dir)
		}
		hc.Notify("[post-%s] nothing left to stage", plan.Phase)
		return nil
	}
	commits, err := commitsSince(plan.Dir, pre)
	if err != nil {
		return err
	}
	if !containsSHA(commits, sha) {
		return fmt.Errorf("commit: the %s cut returned %s, which is not among the %d commit(s) git log %s found in %s",
			plan.action(), sha, len(commits), sinceRange(pre), plan.Dir)
	}
	if plan.Anchor != "" {
		h.fixups++
	} else if h.anchor == "" {
		h.anchor = sha
	} else if plan.Mode == api.CommitModeAmend {
		h.anchor = sha // amend rewrote the anchor
	}
	if err := h.track(hc.Workspace(), plan, pre, commits); err != nil {
		return err
	}
	for _, c := range commits {
		subject, _, _ := strings.Cut(c.Message, "\n")
		hc.Notify("[post-%s] committed %s: %s", plan.Phase, shortSHA(c.SHA), subject)
	}
	return nil
}

// track adds a cut's commits to the workspace record. An amend after the first
// commit rewrote one this hook already recorded, so the hook's whole record is
// re-read rather than appended to.
func (h *Hook) track(ws *api.Workspace, plan Plan, pre string, commits []api.CommitRecord) error {
	if len(h.recorded) == 0 {
		h.start = pre
	} else if plan.Mode == api.CommitModeAmend {
		return h.rewrite(ws, plan.Dir)
	}
	for _, c := range commits {
		ws.AddCommit(c.SHA, c.Message)
		h.recorded = append(h.recorded, c.SHA)
	}
	return nil
}

// rewrite replaces this hook's recorded commits with what start..HEAD holds
// now, after a history rewrite (autosquash, amend) took the recorded SHAs off
// the branch. Other hooks' entries keep their places.
func (h *Hook) rewrite(ws *api.Workspace, dir string) error {
	commits, err := commitsSince(dir, h.start)
	if err != nil {
		return err
	}
	ws.ReplaceCommits(h.recorded, commits)
	h.recorded = make([]string, 0, len(commits))
	for _, c := range commits {
		h.recorded = append(h.recorded, c.SHA)
	}
	return nil
}

// containsSHA matches by prefix: a host pipeline may hand back an abbreviated
// hash, while the record always holds git's full one.
func containsSHA(commits []api.CommitRecord, sha string) bool {
	for _, c := range commits {
		if strings.HasPrefix(c.SHA, sha) {
			return true
		}
	}
	return false
}

// shortSHA abbreviates to git's conventional display width. A host pipeline may
// hand back a short hash already, so this truncates rather than assuming 40.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// squash collapses the fixup chain back into its anchor, then re-reads this
// hook's record so it names the squashed commit instead of the chain that no
// longer exists. A chain of zero fixups is already one commit, so the rebase is
// skipped rather than run as a no-op that could still fail.
func (h *Hook) squash(hc *agent.HookContext) error {
	if h.fixups == 0 || h.anchor == "" || h.DryRun {
		return nil
	}
	fixups := h.fixups
	dir, err := workDir(hc)
	if err != nil {
		return err
	}
	base, root := h.Base, false
	if base == "" {
		if base, root, err = autosquashBase(dir, h.anchor); err != nil {
			return err
		}
	}
	if err := autosquash(dir, base, root); err != nil {
		return err
	}
	h.fixups = 0
	if err := h.rewrite(hc.Workspace(), dir); err != nil {
		return fmt.Errorf("commit: re-reading the record after squashing: %w", err)
	}
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	h.anchor = head
	hc.Notify("[post-run] squashed %d fixup(s) into %s", fixups, shortSHA(head))
	return nil
}

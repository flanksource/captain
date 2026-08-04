// Package commit turns an api.Commit policy into an agent Post hook, so a run's
// work becomes durable at the lifecycle boundary the policy names rather than
// only at the very end.
//
// The default per-turn policy cuts a real commit for the first turn and a
// `git commit --fixup` for every turn after it, then autosquashes the chain back
// into that one commit once the run is over. Nothing here asks a model for a
// message: a fixup borrows its anchor's subject, so committing a turn can never
// stall or fail on an LLM call, and a run that is interrupted or fails
// verification still leaves its work behind.
//
// Repo pre-commit hooks are bypassed (`--no-verify`): captain runs its own cheap
// gates per commit, and a full pre-commit pipeline belongs to the host, which
// supplies it via Hook.Do together with gates: full.
package commit

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

var fallbackLog = logger.GetLogger("commit")

// Plan is one resolved commit, handed to Hook.Do so a host can cut it with its
// own pipeline. It is the policy after every default and lookup is applied —
// the phase, the exact paths, the anchor to fix up — so a host never has to
// re-derive any of it.
type Plan struct {
	Dir     string          // working dir the commit is cut in
	Phase   agent.Phase     // boundary this commit is being cut at
	Mode    api.CommitMode  // commit | fixup | amend
	Subject string          // subject for a real commit; empty for a fixup
	Anchor  string          // resolved SHA to fix up onto; empty in commit/amend mode
	Stage   api.CommitStage // how Paths were selected
	Paths   []string        // repo-relative paths to stage (never empty)
	Gates   api.CommitGates // how much checking the host should run
	DryRun  bool
}

// action names what this plan does, distinguishing the run's first commit from
// the fixups that follow it — in fixup mode an empty Anchor means this commit is
// the anchor, and reporting it as a "fixup" would be a lie.
func (p Plan) action() string {
	if p.Mode == api.CommitModeFixup && p.Anchor == "" {
		return "anchor"
	}
	return string(p.Mode)
}

// Hook cuts commits for one api.Commit policy. The embedded policy is the whole
// configuration; the three callbacks are escape hatches that let an API caller
// override a single decision — the subject, the file selection, or the commit
// itself — without reimplementing phase dispatch.
type Hook struct {
	api.Commit

	// MaxFileSize overrides the cheap gate's per-file ceiling (DefaultMaxFileSize).
	MaxFileSize int64

	// Subject overrides the commit subject. Called once per run; the result is
	// reused for every commit so a chain shares one anchor subject.
	Subject func(*agent.HookContext) (string, error)
	// StagePaths overrides which repo-relative paths are committed, replacing
	// the Stage policy entirely.
	StagePaths func(*agent.HookContext) ([]string, error)
	// Do replaces the built-in git implementation with the host's commit
	// pipeline, returning the new SHA (or "" when it committed nothing). This is
	// what gates: full requires, and how gavel reuses its own fixup machinery.
	Do func(*agent.HookContext, Plan) (string, error)

	anchor  string // SHA the chain fixes up onto, set by the run's first commit
	subject string // resolved once, shared by every commit in the run
	fixups  int    // fixup commits cut so far; a chain of 0 needs no squash
}

// New builds a Hook for a policy.
func New(c api.Commit) *Hook { return &Hook{Commit: c} }

// Name identifies the hook in runner errors; the phase disambiguates a workflow
// that declares several policies.
func (h *Hook) Name() string { return "commit:" + string(h.Phase()) }

// Phases declares the boundaries this policy needs. A per-turn policy also takes
// the agent phase, to sweep up work from a turn that errored before the runner
// could dispatch its turn phase; a squashing policy also takes the run phase,
// where the chain collapses.
func (h *Hook) Phases() []agent.Phase {
	phases := []agent.Phase{commitPhase(h.Phase())}
	if h.Phase() == api.CommitOnTurn {
		phases = append(phases, agent.PhaseAgent)
	}
	if h.ShouldSquash() && phases[len(phases)-1] != agent.PhaseRun {
		phases = append(phases, agent.PhaseRun)
	}
	return phases
}

// commitPhase maps the serializable phase onto the runner's.
func commitPhase(p api.CommitPhase) agent.Phase {
	switch p {
	case api.CommitOnTurn:
		return agent.PhaseTurn
	case api.CommitOnAgent:
		return agent.PhaseAgent
	default:
		return agent.PhaseRun
	}
}

// Post cuts this policy's commit when the phase matches, then collapses the
// chain at the end of the run.
func (h *Hook) Post(hc *agent.HookContext, phase agent.Phase) error {
	if h.commitsAt(phase) {
		if err := h.commit(hc, phase); err != nil {
			return err
		}
	}
	if phase == agent.PhaseRun && h.ShouldSquash() {
		return h.squash(hc)
	}
	return nil
}

// commitsAt reports whether a commit is cut at this phase: the declared one, or
// the agent-phase sweep that closes out a per-turn policy.
func (h *Hook) commitsAt(phase agent.Phase) bool {
	if phase == commitPhase(h.Phase()) {
		return true
	}
	return h.Phase() == api.CommitOnTurn && phase == agent.PhaseAgent
}

// commit resolves and cuts one commit. Finding nothing to commit is a normal
// outcome — a turn that only read files, or a sweep after everything was already
// committed — and is not an error.
func (h *Hook) commit(hc *agent.HookContext, phase agent.Phase) error {
	if !h.shouldCommit(hc) {
		return nil
	}
	dir, err := workDir(hc)
	if err != nil {
		return err
	}
	paths, stageMode, err := h.resolvePaths(hc, dir)
	if err != nil || len(paths) == 0 {
		return err
	}
	if err := CheckGates(dir, h.EffectiveGates(), h.MaxFileSize, paths); err != nil {
		return err
	}

	mode := h.EffectiveMode()
	anchor, err := h.resolveAnchor(dir, mode)
	if err != nil {
		return err
	}
	subject, err := h.commitSubject(hc)
	if err != nil {
		return err
	}
	plan := Plan{
		Dir:     dir,
		Phase:   phase,
		Mode:    mode,
		Subject: subject,
		Anchor:  anchor,
		Stage:   stageMode,
		Paths:   paths,
		Gates:   h.EffectiveGates(),
		DryRun:  h.DryRun,
	}
	if h.DryRun {
		ai.LoggerFromContext(hc, fallbackLog).Infof("commit: would cut a %s commit at %s over %d file(s): %s",
			plan.action(), phase, len(paths), strings.Join(paths, ", "))
		return nil
	}
	if h.Do == nil && h.EffectiveGates() == api.CommitGatesFull {
		return fmt.Errorf("commit: gates: full runs the host's pre-commit pipeline, which captain does not have — supply Hook.Do or lower gates to cheap")
	}
	sha, err := h.cut(hc, plan)
	return h.record(hc, plan, sha, err)
}

// cut delegates to the host's commit pipeline when one is supplied, else to the
// built-in git implementation.
func (h *Hook) cut(hc *agent.HookContext, plan Plan) (string, error) {
	if h.Do != nil {
		return h.Do(hc, plan)
	}
	return h.run(plan)
}

// run is the built-in git implementation of a Plan.
func (h *Hook) run(plan Plan) (string, error) {
	if err := stage(plan.Dir, plan.Paths); err != nil {
		return "", err
	}
	staged, err := hasStaged(plan.Dir)
	if err != nil {
		return "", err
	}
	if !staged {
		return "", nil // the paths were already committed by an earlier phase
	}
	switch plan.Mode {
	case api.CommitModeFixup:
		if plan.Anchor != "" {
			return commitFixup(plan.Dir, plan.Anchor)
		}
		return commitStaged(plan.Dir, plan.Subject) // the anchor itself
	case api.CommitModeAmend:
		if h.anchor != "" {
			return commitAmend(plan.Dir)
		}
		return commitStaged(plan.Dir, plan.Subject)
	default:
		return commitStaged(plan.Dir, plan.Subject)
	}
}

// record folds a cut commit into the run's workspace and updates the chain
// state: the first commit of a fixup/amend run becomes the anchor everything
// after it folds into.
func (h *Hook) record(hc *agent.HookContext, plan Plan, sha string, err error) error {
	if err != nil {
		return err
	}
	if sha == "" {
		return nil
	}
	message := plan.Subject
	if plan.Anchor != "" {
		message = "fixup! " + plan.Subject
		h.fixups++
	} else if h.anchor == "" {
		h.anchor = sha
	} else if plan.Mode == api.CommitModeAmend {
		h.anchor = sha // amend rewrote the anchor
	}
	hc.Workspace().AddCommit(sha, message)
	return nil
}

// squash collapses the fixup chain back into its anchor. A chain of zero fixups
// is already one commit, so the rebase is skipped rather than run as a no-op
// that could still fail.
func (h *Hook) squash(hc *agent.HookContext) error {
	if h.fixups == 0 || h.anchor == "" || h.DryRun {
		return nil
	}
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
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	h.anchor = head
	return nil
}

// shouldCommit applies the outcome gate. A per-turn policy never reaches the
// non-always cases — api.Commit.Validate rejects them, because a turn commits
// before its verifiers vote.
func (h *Hook) shouldCommit(hc *agent.HookContext) bool {
	switch h.When {
	case api.CommitWhenSuccess:
		return !hc.Failed
	case api.CommitWhenVerify:
		return !hc.Failed && hc.Verified
	default:
		return true
	}
}

// resolvePaths selects the files this commit picks up, and reports which staging
// mode chose them.
func (h *Hook) resolvePaths(hc *agent.HookContext, dir string) ([]string, api.CommitStage, error) {
	mode := h.stageMode(hc)
	if h.StagePaths != nil {
		paths, err := h.StagePaths(hc)
		return paths, mode, err
	}
	dirty, err := dirtyPaths(dir)
	if err != nil {
		return nil, mode, err
	}
	if len(dirty) == 0 {
		return nil, mode, nil // clean tree: nothing to commit, not a failure
	}
	if mode == api.CommitStageWorktree {
		return dirty, mode, nil
	}
	changed := attributable(dir, dirty, hc.Workspace().Changed)
	if len(changed) == 0 {
		// Staging the tree anyway would sweep the caller's own uncommitted work
		// into an agent commit. Refusing is the whole point of the changed mode.
		return nil, mode, fmt.Errorf("commit: %s has uncommitted changes but none are attributable to this run (%d dirty path(s), 0 recorded as agent-modified); refusing to stage a tree that may hold your own work — run with an isolated worktree, or set stage: worktree to commit everything", dir, len(dirty))
	}
	return changed, mode, nil
}

// stageMode resolves the staging policy: an isolated run holds nothing but the
// agent's work, so it commits the whole tree; a run sharing the caller's tree is
// restricted to the files the agent is recorded as having touched.
func (h *Hook) stageMode(hc *agent.HookContext) api.CommitStage {
	if h.Stage != "" {
		return h.Stage
	}
	if hc.Workspace().Branch != "" {
		return api.CommitStageWorktree
	}
	return api.CommitStageChanged
}

// attributable intersects git's dirty set with the paths the agent is recorded
// as having modified, so a commit can never contain a file the run did not
// touch. Recorded paths may be absolute (the agent reports what it wrote), so
// each is normalized against the working dir first.
func attributable(dir string, dirty, changed []string) []string {
	recorded := make(map[string]bool, len(changed))
	for _, c := range changed {
		recorded[normalizePath(dir, c)] = true
	}
	var out []string
	for _, p := range dirty {
		if recorded[filepath.ToSlash(p)] {
			out = append(out, p)
		}
	}
	return out
}

// normalizePath renders path relative to dir, matching git's repo-relative,
// forward-slashed form.
func normalizePath(dir, path string) string {
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	return filepath.ToSlash(path)
}

// resolveAnchor returns the SHA this commit fixes up onto, or "" when it is
// itself a real commit.
func (h *Hook) resolveAnchor(dir string, mode api.CommitMode) (string, error) {
	if mode != api.CommitModeFixup {
		return "", nil
	}
	if h.anchor != "" {
		return h.anchor, nil
	}
	switch h.Anchor {
	case "":
		return "", nil // this commit becomes the anchor
	case "auto":
		return "", fmt.Errorf("commit: anchor: auto routes each file to the commit that last touched it, which captain's built-in committer does not implement — supply Hook.Do (gavel does), or name a ref")
	default:
		sha, err := resolveRef(dir, h.Anchor)
		if err != nil {
			return "", err
		}
		h.anchor = sha
		return sha, nil
	}
}

// commitSubject resolves the run's commit subject once and reuses it, so every
// commit in a chain shares the anchor's subject.
func (h *Hook) commitSubject(hc *agent.HookContext) (string, error) {
	if h.subject != "" {
		return h.subject, nil
	}
	switch {
	case h.Subject != nil:
		s, err := h.Subject(hc)
		if err != nil {
			return "", fmt.Errorf("commit: subject callback: %w", err)
		}
		h.subject = strings.TrimSpace(s)
	case h.Message != "":
		h.subject = h.Message
	default:
		h.subject = deriveSubject(hc.Request)
	}
	if h.subject == "" {
		return "", fmt.Errorf("commit: resolved an empty commit subject")
	}
	return h.subject, nil
}

// maxSubject keeps the summary line inside the width git and review tools assume.
const maxSubject = 72

// deriveSubject builds a conventional-commit subject from the run's prompt. It
// never asks a model: the subject has to exist before the first turn's commit,
// and a commit that can fail on a network call is not a durability mechanism.
func deriveSubject(req *ai.Request) string {
	summary := ""
	if req != nil {
		summary = firstLine(req.Prompt.User)
		if summary == "" {
			summary = strings.TrimSpace(req.Prompt.Source)
		}
	}
	if summary == "" {
		summary = "agent run"
	}
	subject := "chore(agent): " + summary
	if len(subject) > maxSubject {
		// Cut on a rune boundary, and budget for the ellipsis in bytes — the
		// limit git and review tools apply is a column count, not a rune count.
		const ellipsis = "…"
		cut := maxSubject - len(ellipsis)
		for cut > 0 && !utf8.RuneStart(subject[cut]) {
			cut--
		}
		subject = strings.TrimRight(subject[:cut], " ") + ellipsis
	}
	return subject
}

// firstLine is the first non-empty, non-heading line of a prompt body.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#> "))
		if line != "" {
			return line
		}
	}
	return ""
}

// workDir is the directory commits are cut in: the run's cwd (an isolated
// worktree when there is one), else the repo root.
func workDir(hc *agent.HookContext) (string, error) {
	ws := hc.Workspace()
	if ws.Cwd != "" {
		return ws.Cwd, nil
	}
	if ws.Repo != "" {
		return ws.Repo, nil
	}
	return "", fmt.Errorf("commit: no working directory on the run's workspace (set Runner.Cwd or Runner.Repo)")
}

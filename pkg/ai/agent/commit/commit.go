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
	failed  error  // first commit failure, so the agent-phase sweep does not repeat it
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
			h.failed = err
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
//
// The sweep is skipped once a commit has already failed. It exists to catch a
// turn that errored before the runner could dispatch its turn phase, so it has
// nothing to add after a turn-phase attempt that resolved the same paths and
// failed on them — it would only re-derive the identical error, which the runner
// then joins onto the one already propagating, printing the same failure twice
// under two different phase names.
func (h *Hook) commitsAt(phase agent.Phase) bool {
	if phase == commitPhase(h.Phase()) {
		return true
	}
	return h.Phase() == api.CommitOnTurn && phase == agent.PhaseAgent && h.failed == nil
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
	// Announced before the cut, not only after it: a host pipeline on gates: full
	// runs lint and pre-commit hooks, which is long enough that silence reads as a
	// hang. A turn that resolved no paths says nothing at all — silence there
	// already means "nothing happened", and a line per read-only turn is noise.
	hc.Notify("[post-%s] committing %d file(s)", phase, len(paths))
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
		// The paths resolved but the pipeline staged nothing (an earlier phase
		// already took them). Said out loud because the "committing" line above
		// has already promised a commit.
		hc.Notify("[post-%s] nothing left to stage", plan.Phase)
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
	hc.Notify("[post-%s] committed %s: %s", plan.Phase, shortSHA(sha), message)
	return nil
}

// shortSHA abbreviates to git's conventional display width. A host pipeline may
// hand back a short hash already, so this truncates rather than assuming 40.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// squash collapses the fixup chain back into its anchor. A chain of zero fixups
// is already one commit, so the rebase is skipped rather than run as a no-op
// that could still fail.
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
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	h.anchor = head
	hc.Notify("[post-run] squashed %d fixup(s) into %s", fixups, shortSHA(head))
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
	recorded := hc.Workspace().Changed
	changed := attributable(dir, recordBase(hc), dirty, recorded)
	if len(changed) == 0 {
		// Staging the tree anyway would sweep the caller's own uncommitted work
		// into an agent commit. Refusing is the whole point of the changed mode.
		return nil, mode, unattributableErr(dir, dirty, recorded)
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

// unattributableErr explains a refusal. The two ways of arriving here are
// indistinguishable in the tree but call for different investigations, so they
// get different messages: an agent that recorded nothing may have written
// through tools the runner does not track (a shell redirect, an MCP server),
// while an agent that recorded files none of which are dirty here needs those
// paths named — reporting them as "none recorded" sends the reader looking for
// a bug in the agent instead of at where its edits actually landed.
func unattributableErr(dir string, dirty, recorded []string) error {
	const advice = "refusing to stage a tree that may hold your own work — run with an isolated worktree, or set stage: worktree to commit everything"
	if len(recorded) == 0 {
		return fmt.Errorf("commit: %s has %d uncommitted path(s) but the run recorded no file edits (an agent that writes through the shell or an MCP tool is not tracked); %s", dir, len(dirty), advice)
	}
	return fmt.Errorf("commit: %s has %d uncommitted path(s), none of them among the %d file(s) the run recorded editing (%s) — those edits are either in another tree or already committed; %s",
		dir, len(dirty), len(recorded), strings.Join(elide(recorded, 3), ", "), advice)
}

// elide renders at most limit entries, summarising the rest by count so a run
// that touched a hundred files still produces a readable one-line error. The
// capped slice keeps the append off the caller's backing array.
func elide(paths []string, limit int) []string {
	if len(paths) <= limit {
		return paths
	}
	return append(paths[:limit:limit], fmt.Sprintf("and %d more", len(paths)-limit))
}

// attributable intersects git's dirty set with the paths the agent is recorded
// as having modified, so a commit can never contain a file the run did not
// touch.
//
// The two sides arrive in different namespaces and neither can be converted to
// the other's by string surgery: dirty paths are relative to root, while
// recorded paths are relative to recordBase — which is the same directory only
// when the run was launched from the top of its working tree. Resolving both to
// absolute paths is what lets a run started in a subdirectory attribute its own
// edits.
func attributable(root, base string, dirty, changed []string) []string {
	recorded := make(map[string]bool, len(changed))
	for _, c := range changed {
		recorded[resolveAgainst(base, c)] = true
	}
	var out []string
	for _, p := range dirty {
		if recorded[resolveAgainst(root, p)] {
			out = append(out, p)
		}
	}
	return out
}

// resolveAgainst renders path as an absolute, forward-slashed path anchored on
// base. A path that is already absolute stands on its own; base may be empty,
// in which case only absolute paths can ever match.
func resolveAgainst(base, path string) string {
	if !filepath.IsAbs(path) {
		if base == "" {
			return filepath.ToSlash(filepath.Clean(path))
		}
		path = filepath.Join(base, path)
	}
	return filepath.ToSlash(filepath.Clean(path))
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

// workDir is the directory commits are cut in: the root of the working tree the
// run's cwd sits in (an isolated worktree when there is one). It is the root
// rather than the cwd itself because every path that reaches staging comes from
// `git status`, which reports repo-relative paths however deep it is invoked —
// a run started in a monorepo subdirectory would otherwise stage pathspecs
// against the wrong base.
func workDir(hc *agent.HookContext) (string, error) {
	ws := hc.Workspace()
	// Cwd leads: an isolated run keeps Repo pointing at the checkout it branched
	// from, and committing there rather than in the worktree would defeat the
	// isolation entirely.
	dir := ws.Cwd
	if dir == "" {
		dir = ws.Repo
	}
	if dir == "" {
		return "", fmt.Errorf("commit: no working directory on the run's workspace (set Runner.Cwd or Runner.Repo)")
	}
	return gitRoot(dir)
}

// recordBase is the directory the workspace's recorded paths are relative to.
// Runner.recordEvent relativizes each edit against ws.Repo and leaves it
// absolute when there is none, so this is ws.Repo and nothing else — resolving
// against any other directory would silently mis-attribute every path.
func recordBase(hc *agent.HookContext) string {
	if repo := hc.Workspace().Repo; repo != "" {
		return canonicalDir(repo)
	}
	return ""
}

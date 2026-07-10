package cmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

const defaultExecutorTimeout = 30 * time.Minute
const defaultSendAttempts = 3
const defaultSendRetryDelay = 2 * time.Second
const defaultScreenPollInterval = time.Second
const defaultScreenMaxPollInterval = 5 * time.Second
const defaultScreenStableDuration = 2 * time.Second
const defaultScreenLines = 120

// defaultSessionStartRetryDelays is the back-off between re-pressing Enter when
// a submit keystroke did not start the turn. cmux occasionally drops the Enter
// (the REPL was still initializing, or it landed in paste mode), leaving the
// typed text unsent until a downstream timeout fails the run. Re-pressing Enter
// resubmits the already-typed text. The escalation starts fast (2s) and grows so
// a dropped Enter is recovered in seconds rather than tens of seconds.
var defaultSessionStartRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// defaultSendSettleDelay is the settle/quiesce window a send waits for the paste
// to hold stable on the surface before pressing Enter (SendKeySurface). cmux can
// still be applying the paste when the Enter arrives, submitting a half-applied
// buffer or swallowing the Enter in paste mode; requiring the surface to stay
// unchanged for this window gives the paste time to fully land. It is also the
// fixed fallback delay used when the paste is never observed landing (see
// waitForPasteLanded).
const defaultSendSettleDelay = 2 * time.Second

// defaultPasteLandTimeout bounds how long a send polls for the pasted text to
// render on the surface before giving up and falling back to the fixed settle
// delay + Enter. Generous so a slow claude/codex input event queue still lands the
// paste before the fallback fires.
const defaultPasteLandTimeout = 60 * time.Second

// defaultPasteLandPollInterval is how often a send re-reads the surface while
// waiting for the paste to land.
const defaultPasteLandPollInterval = 500 * time.Millisecond

// defaultREPLReadyTimeout bounds how long the claude launch waits for the REPL's
// input prompt to appear before falling back to plain screen-idle detection.
const defaultREPLReadyTimeout = 30 * time.Second

// runConfig carries the timeouts and poll intervals the cmux driver reads. Every
// field defaults via the accessor methods on *run, so a zero value drives a real
// run with the production defaults; tests override individual fields.
type runConfig struct {
	Timeout               time.Duration
	SendAttempts          int
	SendRetryDelay        time.Duration
	ScreenPollInterval    time.Duration
	ScreenMaxPollInterval time.Duration
	ScreenStableDuration  time.Duration
	ScreenLines           int

	SessionLogPollInterval  time.Duration
	SessionLogAppearTimeout time.Duration
	SessionLogQuiescePeriod time.Duration

	// SessionStartRetryDelays is the back-off used to re-press Enter when a submit
	// keystroke did not start the turn. Defaults to defaultSessionStartRetryDelays.
	SessionStartRetryDelays []time.Duration

	// SendSettleDelay is the settle/quiesce window between the paste landing and
	// pressing Enter (also the fixed fallback when the paste is never observed).
	SendSettleDelay time.Duration
	// PasteLandTimeout bounds how long a send waits for the pasted text to render on
	// the surface before falling back to SendSettleDelay and pressing Enter.
	PasteLandTimeout time.Duration
	// PasteLandPollInterval is how often the send re-reads the surface while waiting
	// for the paste to land.
	PasteLandPollInterval time.Duration
	// REPLReadyTimeout bounds the wait for the claude REPL input prompt before
	// falling back to screen-idle.
	REPLReadyTimeout time.Duration

	// StallTimeout is how long the run may make no progress (neither the session
	// log nor the terminal surface advances) before the stall watchdog nudges,
	// then fails.
	StallTimeout time.Duration
	// StallNudges is how many times the watchdog re-presses Enter to revive a
	// stalled turn before failing loudly.
	StallNudges int
	// StallPollInterval is how often the watchdog samples progress and the surface.
	StallPollInterval time.Duration
}

// run drives one cmux session: it owns the cmux client, the run's tuning config,
// the event sink (emit), and the optional tool-permission broker (canUseTool).
// The last* fields capture the live surface/session from the most recent run so a
// follow-up (resume) can reuse the same agent REPL.
type run struct {
	client     *Client
	cfg        runConfig
	emit       func(ai.Event)
	canUseTool ai.PermissionFunc

	// approvals tracks the in-flight approval handlers spawned by the stall
	// watchdog so the driver can wait for them (and the EventPermission they emit)
	// before the event channel is closed.
	approvals sync.WaitGroup

	lastSurface   WorkspaceRef
	lastSessionID string
	lastWorkDir   string
}

// readScreen returns the normalized surface contents, or "" if the read failed.
func (r *run) readScreen(ctx context.Context, ref WorkspaceRef) string {
	screen, err := r.client.ReadScreen(ctx, ReadScreenOpts{
		WorkspaceRef: ref.String(),
		SurfaceRef:   ref.SurfaceID,
		Lines:        r.screenLines(),
	})
	if err != nil {
		log.Debugf("cmux: read-screen during session-start check failed: %v", err)
		return ""
	}
	return normalizeScreen(screen)
}

func (r *run) waitForScreenIdle(ctx context.Context, ref WorkspaceRef, phase string, timeout time.Duration, baseline string, requireChange bool) (string, error) {
	workspaceRef := strings.TrimSpace(ref.String())
	if workspaceRef == "" {
		return "", fmt.Errorf("cmux workspace reference is required for read-screen")
	}
	surfaceRef := strings.TrimSpace(ref.SurfaceID)
	if surfaceRef == "" {
		return "", fmt.Errorf("cmux surface reference is required for read-screen")
	}
	if timeout <= 0 {
		timeout = defaultExecutorTimeout
	}
	poll := r.screenPollInterval()
	maxPoll := r.screenMaxPollInterval()
	stableFor := r.screenStableDuration()
	lines := r.screenLines()
	log.Debugf("cmux wait: read-screen workspace=%q surface=%q phase=%q lines=%d poll=%s max-poll=%s stable=%s timeout=%s", workspaceRef, surfaceRef, phase, lines, poll, maxPoll, stableFor, timeout)

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waitStart := time.Now()

	var (
		lastScreen    string
		lastChange    time.Time
		lastErr       error
		sawScreen     bool
		changedEnough = !requireChange
	)

	for {
		now := time.Now()
		screen, err := r.client.ReadScreen(waitCtx, ReadScreenOpts{
			WorkspaceRef: workspaceRef,
			SurfaceRef:   surfaceRef,
			Lines:        lines,
		})
		if err != nil {
			lastErr = err
			log.Debugf("cmux read-screen failed while waiting for %s: %v", phase, err)
		} else {
			normalized := normalizeScreen(screen)
			if normalized != "" {
				sawScreen = true
				if lastScreen == "" || normalized != lastScreen {
					lastScreen = normalized
					lastChange = now
					if !changedEnough && normalizeScreen(baseline) != normalized {
						changedEnough = true
						log.Debugf("cmux screen changed for %s", phase)
					}
					log.Debugf("cmux read-screen %s changed (%d bytes):\n%s", phase, len(normalized), screenSnippet(normalized))
				}
				if changedEnough && !lastChange.IsZero() && now.Sub(lastChange) >= stableFor {
					log.Debugf("cmux screen stable for %s during %s", now.Sub(lastChange).Round(time.Millisecond), phase)
					return normalized, nil
				}
			}
		}

		select {
		case <-waitCtx.Done():
			if lastErr != nil && !sawScreen {
				return "", fmt.Errorf("timed out waiting for cmux screen during %s: %w", phase, lastErr)
			}
			if requireChange && !changedEnough {
				return "", fmt.Errorf("timed out waiting for cmux screen to change during %s", phase)
			}
			return "", fmt.Errorf("timed out waiting for cmux screen to stabilize during %s", phase)
		default:
		}

		delay := screenPollDelay(waitStart, poll, maxPoll)
		log.Debugf("cmux read-screen next poll for %s in %s", phase, delay)
		if err := sleepContext(waitCtx, delay); err != nil {
			if lastErr != nil && !sawScreen {
				return "", fmt.Errorf("timed out waiting for cmux screen during %s: %w", phase, lastErr)
			}
			if requireChange && !changedEnough {
				return "", fmt.Errorf("timed out waiting for cmux screen to change during %s", phase)
			}
			return "", fmt.Errorf("timed out waiting for cmux screen to stabilize during %s", phase)
		}
	}
}

type AgentCommandOpts struct {
	Agent     string
	Model     string
	SessionID string
	// Resume reuses SessionID as an existing conversation (claude --resume)
	// rather than creating a new one (claude --session-id). Ignored when
	// SessionID is empty or for codex.
	Resume bool
	// Plan starts claude in plan-only mode (--permission-mode plan). codex has
	// no equivalent flag, so plan there is enforced by the prompt instruction.
	Plan bool
	// PermissionMode is the base permission posture for claude (--permission-mode).
	// Plan forces it to plan. codex ignores it.
	PermissionMode api.PermissionMode
	// AllowedTools / DisallowedTools are passed to claude as --allowedTools /
	// --disallowedTools. codex ignores them.
	AllowedTools    []string
	DisallowedTools []string
	// Effort maps onto claude --effort or Codex's model_reasoning_effort config.
	Effort api.Effort
	// Memory drives claude --bare / --disable-slash-commands / --setting-sources
	// (from Spec.Memory). codex ignores it.
	Memory api.Memory
	// Extra carries the backend's "extra cmux args": *api.ClaudeCmuxOptions for
	// claude, *api.CodexCmuxOptions for codex. A nil/mismatched value emits none.
	Extra any
}

// cliPermissionMode maps an api.PermissionMode onto the claude --permission-mode
// flag value. Unset/"" collapses to "default"; every recognised mode passes
// through to its CLI-native value.
func cliPermissionMode(m api.PermissionMode) string {
	switch m {
	case api.PermissionPlan:
		return "plan"
	case api.PermissionAcceptEdits:
		return "acceptEdits"
	case api.PermissionBypass:
		return "bypassPermissions"
	case api.PermissionAuto:
		return "auto"
	case api.PermissionDontAsk:
		return "dontAsk"
	default:
		return "default"
	}
}

func AgentCommand(opts AgentCommandOpts) string {
	switch opts.Agent {
	case "codex":
		tokens := []string{"codex"}
		if opts.Model != "" {
			tokens = append(tokens, "-m", opts.Model)
		}
		if opts.Effort != api.EffortNone {
			tokens = append(tokens, "-c", fmt.Sprintf("model_reasoning_effort=%q", opts.Effort))
		}
		if extra, ok := opts.Extra.(*api.CodexCmuxOptions); ok && extra != nil {
			tokens = append(tokens, flagArgs(*extra)...)
		}
		return joinCommand(tokens)
	default:
		tokens := []string{"claude"}
		if opts.SessionID != "" {
			if opts.Resume {
				tokens = append(tokens, "--resume", opts.SessionID)
			} else {
				tokens = append(tokens, "--session-id", opts.SessionID)
			}
		}
		mode := opts.PermissionMode
		if opts.Plan {
			mode = api.PermissionPlan
		}
		if opts.Plan || opts.PermissionMode != "" {
			tokens = append(tokens, "--permission-mode", cliPermissionMode(mode))
		}
		if len(opts.AllowedTools) > 0 {
			tokens = append(tokens, "--allowedTools", strings.Join(opts.AllowedTools, ","))
		}
		if len(opts.DisallowedTools) > 0 {
			tokens = append(tokens, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
		}
		if opts.Model != "" {
			tokens = append(tokens, "--model", opts.Model)
		}
		if opts.Effort != "" {
			tokens = append(tokens, "--effort", string(opts.Effort))
		}
		tokens = append(tokens, claudeMemoryArgs(opts.Memory)...)
		if extra, ok := opts.Extra.(*api.ClaudeCmuxOptions); ok && extra != nil {
			tokens = append(tokens, flagArgs(*extra)...)
		}
		return joinCommand(tokens)
	}
}

// claudeMemoryArgs maps Spec.Memory toggles onto claude flags: Bare -> --bare,
// SkipSkills -> --disable-slash-commands, and SkipProject/SkipUser -> a narrowed
// --setting-sources list. SkipHooks/SkipMemory have no granular claude flag (only
// --bare covers them), so they are intentionally not emitted here.
func claudeMemoryArgs(m api.Memory) []string {
	var args []string
	if m.Bare {
		args = append(args, "--bare")
	}
	if m.SkipSkills {
		args = append(args, "--disable-slash-commands")
	}
	if m.SkipProject || m.SkipUser {
		var sources []string
		if !m.SkipUser {
			sources = append(sources, "user")
		}
		if !m.SkipProject {
			sources = append(sources, "project", "local")
		}
		if len(sources) > 0 {
			args = append(args, "--setting-sources", strings.Join(sources, ","))
		}
	}
	return args
}

// flagArgs renders the "extra cmux args" from a struct's `flag:` tags and current
// field values: bools emit "--flag" when true, strings emit "--flag value" when
// non-empty, and []string emit "--flag v1 v2 ..." (the repeatable/space form both
// CLIs accept). Fields emit in declaration order (matching the clicky form order).
func flagArgs(v any) []string {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	var args []string
	for i := 0; i < rt.NumField(); i++ {
		flag := rt.Field(i).Tag.Get("flag")
		if flag == "" {
			continue
		}
		field := rv.Field(i)
		switch field.Kind() {
		case reflect.Bool:
			if field.Bool() {
				args = append(args, "--"+flag)
			}
		case reflect.String:
			if s := field.String(); s != "" {
				args = append(args, "--"+flag, s)
			}
		case reflect.Slice:
			if field.Len() == 0 {
				continue
			}
			args = append(args, "--"+flag)
			for j := 0; j < field.Len(); j++ {
				args = append(args, field.Index(j).String())
			}
		}
	}
	return args
}

// joinCommand renders command tokens into the single shell string pasted onto the
// cmux surface, single-quoting any token that isn't a bare shell-safe word so
// values with spaces or metacharacters (system prompts, JSON settings) survive.
func joinCommand(tokens []string) string {
	quoted := make([]string, len(tokens))
	for i, t := range tokens {
		quoted[i] = shellQuoteIfNeeded(t)
	}
	return strings.Join(quoted, " ")
}

var shellSafeToken = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuoteIfNeeded(s string) string {
	if s != "" && shellSafeToken.MatchString(s) {
		return s
	}
	return shellSingleQuote(s)
}

// withEnv prepends KEY='value' assignments (sorted by key for deterministic
// output) to the agent launch command so the terminal shell exports them to the
// agent process, whose tool children inherit them. The host supplies the env via
// the prepared setup on api.Spec.
func withEnv(command string, env map[string]string) string {
	if len(env) == 0 {
		return command
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assigns := make([]string, 0, len(keys))
	for _, k := range keys {
		assigns = append(assigns, k+"="+shellSingleQuote(env[k]))
	}
	return strings.Join(assigns, " ") + " " + command
}

// shellSingleQuote wraps v in single quotes so the terminal shell treats it as a
// literal, escaping any embedded single quotes.
func shellSingleQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// modelFlag normalizes the configured model into the exact value claude/codex
// --model expects. The bare agent names ("claude"/"codex") and an empty string
// carry no concrete model, so they yield "" (no flag).
func modelFlag(agent, model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" || lower == agent {
		return ""
	}
	if agent == "claude" {
		return ai.NormalizeModelForBackend(ai.BackendClaudeCmux, model)
	}
	if agent == "codex" {
		return ai.NormalizeModelForBackend(ai.BackendCodexCmux, model)
	}
	return model
}

// maxTitleBytes caps the prompt title inlined onto the surface. The title is only
// a pointer into the input file, so a prompt whose first line is itself huge can't
// reintroduce the large-paste failures this design avoids.
const maxTitleBytes = 120

// buildInstruction writes the full host-assembled prompt to an input file under
// <workDir>/.gavel/cmux/ and returns the one-line message dispatched to the agent
// surface: the prompt's title plus a pointer to that file. Always routing the
// inputs through a file keeps the surface paste short and reliable, sidestepping
// the terminal paste truncation and dropped-Enter failures that large inline
// prompts trigger.
func (r *run) buildInstruction(workDir, sessionID, prompt string) (string, error) {
	path, err := writePromptFile(workDir, sessionID, prompt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s - See %s for full details", promptTitle(prompt), path), nil
}

// promptTitle derives a one-line title from the prompt: its first non-empty line
// with leading markdown heading markers stripped, capped to maxTitleBytes. It
// falls back to "Task" when the prompt has no usable text.
func promptTitle(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		if len(line) > maxTitleBytes {
			line = strings.TrimSpace(line[:maxTitleBytes])
		}
		return line
	}
	return "Task"
}

// writePromptFile persists the full prompt body so the surface paste can point the
// agent at it. The filename is keyed on the session id (or "group" when there is
// none, e.g. codex).
func writePromptFile(workDir, sessionID, prompt string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("workDir is required")
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(absWorkDir, ".gavel", "cmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitizeName(sessionID)
	if name == "" {
		name = "group"
	}
	path := filepath.Join(dir, "prompt-"+name+".md")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(prompt)+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (r *run) timeout() time.Duration {
	if r.cfg.Timeout > 0 {
		return r.cfg.Timeout
	}
	return defaultExecutorTimeout
}

func (r *run) sendAttempts() int {
	if r.cfg.SendAttempts > 0 {
		return r.cfg.SendAttempts
	}
	return defaultSendAttempts
}

func (r *run) sendRetryDelay() time.Duration {
	if r.cfg.SendRetryDelay > 0 {
		return r.cfg.SendRetryDelay
	}
	return defaultSendRetryDelay
}

func (r *run) sessionStartRetryDelays() []time.Duration {
	if len(r.cfg.SessionStartRetryDelays) > 0 {
		return r.cfg.SessionStartRetryDelays
	}
	return defaultSessionStartRetryDelays
}

func (r *run) screenPollInterval() time.Duration {
	if r.cfg.ScreenPollInterval > 0 {
		return r.cfg.ScreenPollInterval
	}
	return defaultScreenPollInterval
}

func (r *run) screenMaxPollInterval() time.Duration {
	if r.cfg.ScreenMaxPollInterval > 0 {
		return r.cfg.ScreenMaxPollInterval
	}
	return defaultScreenMaxPollInterval
}

func (r *run) screenStableDuration() time.Duration {
	if r.cfg.ScreenStableDuration > 0 {
		return r.cfg.ScreenStableDuration
	}
	return defaultScreenStableDuration
}

func (r *run) screenLines() int {
	if r.cfg.ScreenLines > 0 {
		return r.cfg.ScreenLines
	}
	return defaultScreenLines
}

func (r *run) pasteLandTimeout() time.Duration {
	if r.cfg.PasteLandTimeout > 0 {
		return r.cfg.PasteLandTimeout
	}
	return defaultPasteLandTimeout
}

func (r *run) pasteLandPollInterval() time.Duration {
	if r.cfg.PasteLandPollInterval > 0 {
		return r.cfg.PasteLandPollInterval
	}
	return defaultPasteLandPollInterval
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func screenPollDelay(start time.Time, base, max time.Duration) time.Duration {
	if base <= 0 {
		base = defaultScreenPollInterval
	}
	if max <= 0 {
		max = defaultScreenMaxPollInterval
	}
	if max < base {
		max = base
	}
	elapsed := time.Since(start)
	steps := int(elapsed / (10 * time.Second))
	delay := base
	for i := 0; i < steps && delay < max; i++ {
		delay *= 2
		if delay > max {
			return max
		}
	}
	return delay
}

func normalizeScreen(screen string) string {
	return strings.TrimSpace(strings.ReplaceAll(screen, "\r\n", "\n"))
}

func screenSnippet(screen string) string {
	const max = 2000
	screen = normalizeScreen(screen)
	if len(screen) <= max {
		return screen
	}
	return screen[:max] + "\n... (truncated)"
}

// groupWorkDir cleans the host-supplied working directory, defaulting to "." when
// it is empty.
func groupWorkDir(dir string) string {
	if strings.TrimSpace(dir) != "" {
		return filepath.Clean(dir)
	}
	return "."
}

func workspaceName(workDir string) string {
	name := filepath.Base(filepath.Clean(workDir))
	if name == "." || name == string(filepath.Separator) {
		return "gavel-todos"
	}
	return name
}

func AgentWorkspaceName(workDir, agent string) string {
	name := workspaceName(workDir)
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return name
	}
	name = sanitizeName(name + "-" + agent)
	if name == "" {
		return "gavel-todos-" + sanitizeName(agent)
	}
	return name
}

var unsafePromptName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(name string) string {
	name = unsafePromptName.ReplaceAllString(strings.TrimSpace(name), "-")
	name = strings.Trim(name, "-._")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

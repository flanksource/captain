package cmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// defaultSendSettleDelay is the pause between pasting text onto the surface
// (SendSurface) and pressing Enter (SendKeySurface). cmux can still be applying
// the paste when the Enter arrives, submitting a half-applied buffer or
// swallowing the Enter in paste mode; the settle gives the paste time to land.
const defaultSendSettleDelay = 150 * time.Millisecond

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

	// SendSettleDelay is the pause between pasting text and pressing Enter.
	SendSettleDelay time.Duration
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
}

// cliPermissionMode maps an api.PermissionMode onto the claude --permission-mode
// flag value. default/auto/"" all collapse to "default".
func cliPermissionMode(m api.PermissionMode) string {
	switch m {
	case api.PermissionPlan:
		return "plan"
	case api.PermissionAcceptEdits:
		return "acceptEdits"
	case api.PermissionBypass:
		return "bypassPermissions"
	default:
		return "default"
	}
}

func AgentCommand(opts AgentCommandOpts) string {
	switch opts.Agent {
	case "codex":
		if opts.Model != "" {
			return "codex -m " + opts.Model
		}
		return "codex"
	default:
		cmd := "claude"
		if opts.SessionID != "" {
			if opts.Resume {
				cmd += " --resume " + opts.SessionID
			} else {
				cmd += " --session-id " + opts.SessionID
			}
		}
		mode := opts.PermissionMode
		if opts.Plan {
			mode = api.PermissionPlan
		}
		if opts.Plan || opts.PermissionMode != "" {
			cmd += " --permission-mode " + cliPermissionMode(mode)
		}
		if len(opts.AllowedTools) > 0 {
			cmd += " --allowedTools " + strings.Join(opts.AllowedTools, ",")
		}
		if len(opts.DisallowedTools) > 0 {
			cmd += " --disallowedTools " + strings.Join(opts.DisallowedTools, ",")
		}
		if opts.Model != "" {
			cmd += " --model " + opts.Model
		}
		return cmd
	}
}

// withEnv prepends KEY='value' assignments (sorted by key for deterministic
// output) to the agent launch command so the terminal shell exports them to the
// agent process, whose tool children inherit them. The host supplies the env via
// req.Context.Env.
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

// modelFlag normalizes the configured model into the value claude/codex --model
// expects: the bare agent names ("claude"/"codex") and an empty string carry no
// concrete model, so they yield "" (no flag).
func modelFlag(agent, model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" || lower == agent {
		return ""
	}
	return model
}

// maxInlinePromptBytes bounds how much of the prompt is dispatched directly to
// the agent surface. Prompts at or below this are inlined in full; larger ones
// are truncated to this size with a pointer to the prompt file for the rest.
const maxInlinePromptBytes = 10 * 1024

// buildInstruction renders the message dispatched to the agent surface from the
// host-assembled prompt body. Small prompts are pasted verbatim; large ones are
// truncated and the full text is written to <workDir>/.gavel/cmux/prompt-*.md
// with a pointer appended.
func (r *run) buildInstruction(workDir, sessionID, prompt string) (string, error) {
	body, truncated := truncatePrompt(prompt, maxInlinePromptBytes)
	if !truncated {
		return body, nil
	}
	path, err := writePromptFile(workDir, sessionID, prompt)
	if err != nil {
		return "", err
	}
	return body + fmt.Sprintf("\n\n... (prompt truncated — read %s for the full prompt)", path), nil
}

// writePromptFile persists the full prompt body so the truncated surface paste
// can point the agent at it. The filename is keyed on the session id (or "group"
// when there is none, e.g. codex).
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

// truncatePrompt clamps prompt to max bytes, cutting on the last line boundary so
// the inlined body never ends mid-line. The bool reports whether truncation happened.
func truncatePrompt(prompt string, max int) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	if len(prompt) <= max {
		return prompt, false
	}
	clipped := prompt[:max]
	if idx := strings.LastIndexByte(clipped, '\n'); idx > 0 {
		clipped = clipped[:idx]
	}
	return strings.TrimRight(clipped, "\n"), true
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

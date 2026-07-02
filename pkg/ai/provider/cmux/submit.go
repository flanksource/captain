package cmux

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// replReadyRe matches the claude REPL's input prompt — the box-drawn "> " / "❯ "
// line claude renders once it is ready to accept a message. It is a positive
// readiness signal, stronger than screen-idle, which can settle on a half-drawn
// startup banner before the prompt is actually accepting input.
var replReadyRe = regexp.MustCompile(`(?m)^\s*(?:[│|]\s*)?[>❯](?:\s|$)`)

// submitConfirm describes how a submit confirms it actually took (the agent began
// the turn). The initial prompt waits for the session log to appear; feedback
// waits for it to grow past the byte offset captured before the send. Both also
// accept the terminal surface advancing past the just-submitted screen.
type submitConfirm struct {
	logPath string
	// baseOffset is the session-log size captured before the send. When growth is
	// set, confirmation requires the log to grow beyond it (feedback resumes an
	// existing log); otherwise the log merely appearing confirms (initial start).
	baseOffset int64
	growth     bool
}

// submitAndConfirm pastes text onto the surface, presses Enter, and confirms the
// submission actually started the turn, re-pressing Enter on the configured
// back-off when neither the session log nor the surface shows progress. cmux
// occasionally drops the Enter (REPL initializing, paste mode), leaving the text
// typed but unsent; this is the shared resilience the initial prompt and feedback
// both rely on. If the turn still hasn't started after the last re-press it
// returns nil and lets the downstream wait surface the loud failure rather than
// masking it here.
func (r *run) submitAndConfirm(ctx context.Context, ref WorkspaceRef, label, text string, sc submitConfirm) error {
	if err := r.sendSurfaceText(ctx, ref, label, text); err != nil {
		return err
	}
	return r.confirmSubmitted(ctx, ref, label, sc)
}

// confirmSubmitted re-presses Enter until the submit is confirmed (or the
// back-off is exhausted). The post-send screen is the baseline the surface signal
// is compared against: if the Enter was dropped the typed text sits here
// unchanged; if it took, the screen advances past it.
func (r *run) confirmSubmitted(ctx context.Context, ref WorkspaceRef, label string, sc submitConfirm) error {
	postSend := r.readScreen(ctx, ref)
	if started, why := r.confirmStarted(ctx, ref, sc, postSend); started {
		log.Debugf("cmux: %s confirmed (%s)", label, why)
		return nil
	}

	// Wait, re-check, then re-press only if still not started, so a submit whose
	// Enter merely took a moment to register isn't sent a spurious extra Enter.
	delays := r.sessionStartRetryDelays()
	for i, delay := range delays {
		log.Infof("cmux: %s not confirmed yet; waiting %s before re-pressing Enter (attempt %d/%d)", label, delay, i+1, len(delays))
		if err := sleepContext(ctx, delay); err != nil {
			return err
		}
		if started, why := r.confirmStarted(ctx, ref, sc, postSend); started {
			log.Infof("cmux: %s confirmed while waiting to re-press Enter (%s)", label, why)
			return nil
		}
		log.Debugf("cmux command: cmux send-key --workspace %q --surface %q Enter", ref.String(), ref.SurfaceID)
		if err := r.client.SendKeySurface(ctx, ref.String(), ref.SurfaceID, "Enter"); err != nil {
			if ctx.Err() != nil {
				return err
			}
			log.Warnf("cmux: %s Enter re-press failed: %v", label, err)
		}
	}

	if started, why := r.confirmStarted(ctx, ref, sc, postSend); started {
		log.Infof("cmux: %s confirmed after re-pressing Enter (%s)", label, why)
		return nil
	}
	log.Warnf("cmux: %s not confirmed after re-pressing Enter %d time(s); relying on downstream timeout", label, len(delays))
	return nil
}

// confirmStarted reports whether the submit started the turn, from the session
// jsonl (authoritative — claude writes it as the turn progresses) and, as a
// fallback, the surface having advanced past the post-send baseline.
func (r *run) confirmStarted(ctx context.Context, ref WorkspaceRef, sc submitConfirm, postSend string) (bool, string) {
	if sc.growth {
		if fileSize(sc.logPath) > sc.baseOffset {
			return true, "session log grew past pre-send offset"
		}
	} else if _, err := os.Stat(sc.logPath); err == nil {
		return true, "session log appeared"
	}
	screen := r.readScreen(ctx, ref)
	if screen != "" && postSend != "" && screen != postSend {
		return true, "surface advanced past submission"
	}
	return false, ""
}

// sendSurfaceText pastes text onto the surface and presses Enter, retrying the
// whole paste+Enter on transient cmux errors. Between the paste and the Enter it
// waits for the paste to actually land on the surface (waitForPasteLanded) rather
// than firing Enter blindly, so a slow input event queue doesn't get Enter pressed
// into a half-applied buffer or swallowed while the surface is still in paste mode.
func (r *run) sendSurfaceText(ctx context.Context, ref WorkspaceRef, label, text string) error {
	attempts := r.sendAttempts()
	delay := r.sendRetryDelay()
	settle := r.sendSettleDelay()
	workspaceRef := ref.String()
	surfaceRef := ref.SurfaceID
	text = strings.TrimRight(text, "\r\n")
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			log.Debugf("cmux: waiting %s before retrying %s send", delay, label)
			if err := sleepContext(ctx, delay); err != nil {
				return err
			}
		}
		ai.LoggerFromContext(ctx, log).Tracef("cmux: sending %s to workspace %s surface %s (attempt %d/%d)", label, workspaceRef, surfaceRef, attempt, attempts)
		log.Debugf("cmux command: cmux send --workspace %q --surface %q -- <%s>", workspaceRef, surfaceRef, label)
		log.Debugf("cmux command: cmux send-key --workspace %q --surface %q Enter", workspaceRef, surfaceRef)
		log.Debugf("cmux send payload:\n%s", screenSnippet(text))
		before := r.readScreen(ctx, ref)
		if err := r.client.SendSurface(ctx, workspaceRef, surfaceRef, text); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return err
			}
			if attempt < attempts {
				log.Warnf("cmux: %s send attempt %d/%d failed: %v; retrying in %s", label, attempt, attempts, err, delay)
			} else {
				log.Warnf("cmux: %s send attempt %d/%d failed: %v", label, attempt, attempts, err)
			}
			continue
		}
		landed, err := r.waitForPasteLanded(ctx, ref, before)
		if err != nil {
			return err
		}
		if !landed {
			log.Warnf("cmux: %s paste not observed on surface within %s; pressing Enter after %s settle fallback", label, r.pasteLandTimeout(), settle)
			if err := sleepContext(ctx, settle); err != nil {
				return err
			}
		}
		if err := r.client.SendKeySurface(ctx, workspaceRef, surfaceRef, "Enter"); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return err
			}
			if attempt < attempts {
				log.Warnf("cmux: %s enter attempt %d/%d failed: %v; retrying in %s", label, attempt, attempts, err, delay)
			} else {
				log.Warnf("cmux: %s enter attempt %d/%d failed: %v", label, attempt, attempts, err)
			}
			continue
		}
		log.Infof("cmux: sent %s to workspace %s surface %s", label, workspaceRef, surfaceRef)
		return nil
	}
	return fmt.Errorf("send cmux %s after %d attempts: %w", label, attempts, lastErr)
}

// waitForPasteLanded polls the surface until the paste has rendered — the screen
// changes from the pre-paste baseline and then holds stable for the settle window,
// confirming cmux finished applying the paste rather than pressing Enter into a
// half-filled buffer. Sends are always issued from a quiesced surface, so any
// change is attributable to the paste. Returns true once landed; false on timeout,
// where the caller falls back to the fixed settle so a send never regresses below
// the previous fixed-delay behaviour.
func (r *run) waitForPasteLanded(ctx context.Context, ref WorkspaceRef, before string) (bool, error) {
	timeout := r.pasteLandTimeout()
	poll := r.pasteLandPollInterval()
	quiesce := r.sendSettleDelay()
	base := normalizeScreen(before)
	deadline := time.Now().Add(timeout)
	var stable string
	var stableSince time.Time
	for {
		screen := normalizeScreen(r.readScreen(ctx, ref))
		if screen != "" && screen != base {
			if screen == stable {
				if time.Since(stableSince) >= quiesce {
					log.Debugf("cmux: paste landed (surface changed and held stable for %s)", quiesce)
					return true, nil
				}
			} else {
				stable = screen
				stableSince = time.Now()
			}
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := sleepContext(ctx, poll); err != nil {
			return false, err
		}
	}
}

// waitForREPLReady polls the surface until the claude input prompt appears,
// confirming the REPL will accept the initial prompt. On timeout it falls back to
// the screen-idle wait so a REPL whose prompt we failed to recognize (a theme
// change, a wrapped banner) still proceeds rather than failing the run.
func (r *run) waitForREPLReady(ctx context.Context, ref WorkspaceRef, timeout time.Duration, baseline string) (string, error) {
	readyTimeout := r.replReadyTimeout(timeout)
	poll := r.screenPollInterval()
	base := normalizeScreen(baseline)
	deadline := time.Now().Add(readyTimeout)
	for {
		screen := r.readScreen(ctx, ref)
		if screen != "" && screen != base && replReadyRe.MatchString(screen) {
			log.Debugf("cmux: claude REPL ready (input prompt detected)")
			return screen, nil
		}
		if time.Now().After(deadline) {
			log.Debugf("cmux: claude REPL prompt not detected within %s; falling back to screen-idle", readyTimeout)
			return r.waitForScreenIdle(ctx, ref, "after agent launch", timeout, baseline, true)
		}
		if err := sleepContext(ctx, poll); err != nil {
			return "", err
		}
	}
}

func (r *run) sendSettleDelay() time.Duration {
	if r.cfg.SendSettleDelay > 0 {
		return r.cfg.SendSettleDelay
	}
	return defaultSendSettleDelay
}

func (r *run) replReadyTimeout(timeout time.Duration) time.Duration {
	rt := r.cfg.REPLReadyTimeout
	if rt <= 0 {
		rt = defaultREPLReadyTimeout
	}
	if timeout > 0 && rt > timeout {
		rt = timeout
	}
	return rt
}

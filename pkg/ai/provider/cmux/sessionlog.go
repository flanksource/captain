package cmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
)

const (
	defaultSessionLogPollInterval  = 500 * time.Millisecond
	defaultSessionLogAppearTimeout = 30 * time.Second
)

// errSessionLogNotFound is returned by the tailer when Claude never created the
// pre-generated session log within the appear timeout. The driver treats it as
// a hard run failure rather than falling back to screen-idle detection.
var errSessionLogNotFound = errors.New("claude session log did not appear")

// SessionLogPath resolves the on-disk Claude session log for a session id,
// mirroring Claude's `~/.claude/projects/<normalized-cwd>/<id>.jsonl` layout.
// It is exported so the dashboard can locate the log to follow it live.
func SessionLogPath(workDir, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir %q: %w", workDir, err)
	}
	projects := history.GetProjectsDir()
	if projects == "" {
		return "", fmt.Errorf("could not resolve claude projects directory")
	}
	return filepath.Join(projects, history.NormalizePath(abs), sessionID+".jsonl"), nil
}

// fileSize returns the size of path in bytes, or 0 when it cannot be stat-ed (a
// missing log reads as size 0, the natural "not started" baseline).
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// sessionTailer follows a Claude session log, streaming each parsed event to a
// callback until the assistant turn ends, the log goes quiet, or the context is
// cancelled.
type sessionTailer struct {
	path          string
	pollInterval  time.Duration
	appearTimeout time.Duration
	quiescePeriod time.Duration
	// seekToEnd skips the lines already in the log when tailing begins. Resume
	// runs reuse an existing log that ends in the prior turn's end_turn; without
	// this the tailer would see that stale terminal event and report completion
	// before the resumed turn produces anything.
	seekToEnd bool
	// onLine, when set, receives each complete raw log line before it is parsed
	// into events. It feeds the session-stats accumulator so token/cost totals
	// stay live during a run. The slice is only valid for the call's duration.
	onLine func([]byte)
}

func (st sessionTailer) poll() time.Duration {
	if st.pollInterval > 0 {
		return st.pollInterval
	}
	return defaultSessionLogPollInterval
}

// tail blocks until the session reaches a terminal turn (returns true), the log
// never appeared (errSessionLogNotFound), or the context is cancelled. Each
// parsed event is delivered to onEvent in order.
func (st sessionTailer) tail(ctx context.Context, onEvent func(history.SessionEvent)) (bool, error) {
	f, err := st.waitForFile(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	if st.seekToEnd {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return false, fmt.Errorf("seek session log %q to end: %w", st.path, err)
		}
	}

	var pending []byte
	buf := make([]byte, 32*1024)
	sawActivity := false
	lastActivity := time.Now()

	for {
		progressed, done, err := st.drain(f, &pending, buf, onEvent)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		if progressed {
			lastActivity = time.Now()
		} else if sawActivity && st.quiescePeriod > 0 && time.Since(lastActivity) >= st.quiescePeriod {
			return true, nil
		}
		sawActivity = sawActivity || progressed

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(st.poll()):
		}
	}
}

// drain reads all currently-available bytes, dispatches complete lines, and
// reports whether any events were seen (progressed) or a turn ended (done).
func (st sessionTailer) drain(f *os.File, pending *[]byte, buf []byte, onEvent func(history.SessionEvent)) (progressed, done bool, err error) {
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			*pending = append(*pending, buf[:n]...)
			for {
				i := bytes.IndexByte(*pending, '\n')
				if i < 0 {
					break
				}
				line := (*pending)[:i]
				*pending = (*pending)[i+1:]
				if st.onLine != nil {
					st.onLine(line)
				}
				events, perr := history.ParseSessionEvents(line)
				if perr != nil {
					continue
				}
				for _, ev := range events {
					progressed = true
					onEvent(ev)
					// EventTurnEnd is a normal completion; EventError is a terminal
					// API/network failure. Both end the tail — the caller distinguishes
					// success from failure from the events it saw.
					if ev.Kind == history.EventTurnEnd || ev.Kind == history.EventError {
						return progressed, true, nil
					}
				}
			}
		}
		if rerr == io.EOF {
			return progressed, false, nil
		}
		if rerr != nil {
			return progressed, false, rerr
		}
	}
}

// waitForFile polls until the session log exists or the appear timeout elapses.
func (st sessionTailer) waitForFile(ctx context.Context) (*os.File, error) {
	appear := st.appearTimeout
	if appear <= 0 {
		appear = defaultSessionLogAppearTimeout
	}
	deadline := time.Now().Add(appear)
	for {
		f, err := os.Open(st.path)
		if err == nil {
			return f, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("open session log %q: %w", st.path, err)
		}
		if time.Now().After(deadline) {
			return nil, errSessionLogNotFound
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(st.poll()):
		}
	}
}

// awaitSessionCompletion tails the Claude session log for the given pre-generated
// session id, emitting progress as ai.Events and reporting whether the assistant
// turn completed. It returns the resolved log path for diagnostics even on error.
func (r *run) awaitSessionCompletion(ctx context.Context, sessionID, workDir string, timeout time.Duration, resume bool, acc *SessionAccumulator) (string, bool, error) {
	path, err := SessionLogPath(workDir, sessionID)
	if err != nil {
		return "", false, err
	}
	log.Infof("cmux: tailing claude session log %s", path)

	tailer := sessionTailer{
		path:          path,
		pollInterval:  r.sessionLogPollInterval(),
		appearTimeout: r.sessionLogAppearTimeout(timeout),
		quiescePeriod: r.sessionLogQuiescePeriod(),
		seekToEnd:     resume,
	}
	if acc != nil {
		tailer.onLine = acc.AddLine
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A synthetic API/network error ends the turn in failure. Capture it so the run
	// fails loudly with the reason rather than being mis-reported as completed (the
	// error's stop_reason is "stop_sequence", indistinguishable from success).
	var apiErr error
	completed, err := tailer.tail(tctx, func(ev history.SessionEvent) {
		if ev.Kind == history.EventError {
			apiErr = fmt.Errorf("claude session %s ended on API error: %s", sessionID, sessionErrorText(ev))
		}
		r.emitSessionEvent(ev)
	})
	if err != nil {
		return path, completed, err
	}
	if apiErr != nil {
		return path, false, apiErr
	}
	return path, completed, nil
}

// emitSessionEvent maps a parsed session event onto the streamed ai.Events. The
// terminal result (EventResult) is emitted once by the driver from the run's
// outcome (with usage/cost from the accumulator), so this maps only the
// streaming assistant/thinking/tool content; an API error is also surfaced as an
// EventError here so the host sees the reason inline.
func (r *run) emitSessionEvent(ev history.SessionEvent) {
	switch ev.Kind {
	case history.EventAssistantText:
		r.emit(ai.Event{Kind: ai.EventText, Text: ev.Text})
	case history.EventThinking:
		r.emit(ai.Event{Kind: ai.EventThinking, Text: ev.Text})
	case history.EventToolUse:
		r.emit(ai.Event{
			Kind:       ai.EventToolUse,
			Tool:       ev.ToolUse.Tool,
			Input:      ev.ToolUse.Input,
			ToolCallID: ev.ToolUse.ToolUseID,
		})
	case history.EventError:
		r.emit(ai.Event{Kind: ai.EventError, Error: sessionErrorText(ev)})
	}
}

func (r *run) sessionLogPollInterval() time.Duration {
	if r.cfg.SessionLogPollInterval > 0 {
		return r.cfg.SessionLogPollInterval
	}
	return defaultSessionLogPollInterval
}

func (r *run) sessionLogAppearTimeout(timeout time.Duration) time.Duration {
	appear := r.cfg.SessionLogAppearTimeout
	if appear <= 0 {
		appear = defaultSessionLogAppearTimeout
	}
	if timeout > 0 && appear > timeout {
		appear = timeout
	}
	return appear
}

// sessionLogQuiescePeriod is opt-in (0 = disabled). The reliable completion
// signal is an end_turn stop_reason; log silence is unreliable because a single
// long-running tool call (build, test suite) leaves the log quiet for minutes
// mid-turn, so quiescence must not be the default.
func (r *run) sessionLogQuiescePeriod() time.Duration {
	return r.cfg.SessionLogQuiescePeriod
}

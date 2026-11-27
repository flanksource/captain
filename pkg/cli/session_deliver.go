package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/session/deliver"
	sessionprocess "github.com/flanksource/captain/pkg/session/process"
	clickyprocess "github.com/flanksource/clicky/process"
)

type SessionDeliverOptions struct {
	ID   string `args:"true" help:"Captain session ID or provider session ID of a live session"`
	Text string `flag:"text" help:"Message to deliver into the session"`
}

// SessionDeliverRequest is the body of POST /api/captain/sessions/{id}/deliver.
type SessionDeliverRequest struct {
	Text string `json:"text"`
}

var (
	readProcessEnvironment = clickyprocess.Environment
	discoverProcesses      = clickyprocess.Discover
	sessionDeliverer       = deliver.Deliverer{}
	errSessionNotLive      = errors.New("no live process")
)

// claudeMessagingEnvironment reads the messaging environment claude pid
// exported to its children (see deliver.ClaudeMessagingEnvironment). Children
// with no environment left to read (exited, zombie or exiting) are skipped by
// the snapshot. A readable child carrying the socket takes precedence over
// environment errors from siblings.
func claudeMessagingEnvironment(ctx context.Context, pid int) (map[string]string, error) {
	snapshot, err := discoverProcesses(ctx, clickyprocess.SnapshotOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing children of claude process %d: %w", pid, err)
	}
	if !stillRunning(snapshot, pid, nil) {
		return nil, fmt.Errorf("claude process %d is no longer running", pid)
	}
	children := snapshot.Children(pid)
	pids := make([]int, len(children))
	for i, child := range children {
		pids[i] = child.PID
	}
	snapshot.PopulateEnvironments(ctx, pids, clickyprocess.EnvironmentOptions{Keys: deliver.MessagingKeys})
	var environments []map[string]string
	var readErr error
	for _, childPID := range pids {
		child, _ := snapshot.Get(childPID)
		if child.EnvironmentError != "" {
			readErr = errors.Join(readErr, fmt.Errorf("reading environment of claude %d's child %d: %s", pid, childPID, child.EnvironmentError))
		}
		if child.Environment != nil {
			environments = append(environments, child.Environment)
		}
	}
	if env := deliver.ClaudeMessagingEnvironment(pid, environments); env != nil {
		return env, nil
	}
	return nil, readErr
}

// RunSessionDeliver sends text into a live claude or codex session that captain
// did not launch, through its messaging socket, cmux agent inbox or codex queue.
// A session that is not live is a 409.
func RunSessionDeliver(ctx context.Context, opts SessionDeliverOptions) (deliver.Result, error) {
	if strings.TrimSpace(opts.Text) == "" {
		return deliver.Result{}, newChatError(http.StatusBadRequest, "message text is required")
	}
	item, err := resolveSessionItem(ctx, opts.ID)
	if err != nil {
		return deliver.Result{}, err
	}
	result, err := deliverLiveSession(ctx, item, opts.Text)
	if errors.Is(err, errSessionNotLive) {
		err = newChatError(http.StatusConflict, err.Error())
	}
	if errors.Is(err, deliver.ErrNoChannel) {
		err = newChatError(http.StatusUnprocessableEntity, err.Error())
	}
	return result, err
}

func deliverLiveSession(ctx context.Context, item SessionGetItem, text string) (deliver.Result, error) {
	target, err := liveDeliveryTarget(ctx, item)
	if err != nil {
		return deliver.Result{}, err
	}
	result, err := sessionDeliverer.Deliver(ctx, target, text)
	if err != nil && !errors.Is(err, deliver.ErrNoChannel) {
		return deliver.Result{}, newChatError(http.StatusBadGateway, err.Error())
	}
	return result, err
}

// liveDeliveryTarget finds the session's live process and re-reads its
// environment now (for claude, that of its children), so the target reflects
// the process as it is rather than the last sample, and a PID since reused by
// another session is refused.
func liveDeliveryTarget(ctx context.Context, item SessionGetItem) (deliver.Target, error) {
	source := resumeTargetOf(item).Source
	if (source == "claude" || source == "codex") && item.ProviderSessionID != "" {
		processes, err := discoverSessionProcesses()
		if err != nil {
			return deliver.Target{}, err
		}
		if process := originalSessionProcess(processes, source, item.ProviderSessionID); process != nil {
			enrichSurfacesFromCmux([]agentProcess{*process})
			return processDeliveryTarget(ctx, item, *process)
		}
	}
	live, err := RunSessionLive(ctx, SessionLiveOptions{Source: "all", All: true, Full: true})
	if err != nil {
		return deliver.Target{}, err
	}
	var record *SessionRecord
	for i := range live.Sessions {
		candidate := &live.Sessions[i]
		if candidate.Live != nil && candidate.Live.PID > 0 &&
			(candidate.Key == item.CaptainID || (item.ProviderSessionID != "" && candidate.Live.SessionID == item.ProviderSessionID)) {
			record = candidate
			break
		}
	}
	if record == nil {
		return deliver.Target{}, fmt.Errorf("session %s has %w", item.CaptainID, errSessionNotLive)
	}
	process := agentProcess{PID: record.Live.PID, Source: record.Source, Surface: record.Live.Surface}
	return processDeliveryTarget(ctx, item, process)
}

// The process that originally opened the session takes precedence over a later
// resume of the same transcript and over sampled or cwd-inferred identity.
func originalSessionProcess(processes []agentProcess, source, sessionID string) *agentProcess {
	var original *agentProcess
	for i := range processes {
		process := &processes[i]
		if process.Source != source || process.PID <= 0 || process.Status == "zombie" || process.Status == "exited" || declaredSessionID(process) != sessionID {
			continue
		}
		if original == nil || (process.StartedAt != nil && (original.StartedAt == nil || process.StartedAt.Before(*original.StartedAt))) {
			original = process
		}
	}
	return original
}

func processDeliveryTarget(ctx context.Context, item SessionGetItem, process agentProcess) (deliver.Target, error) {
	var env map[string]string
	var err error
	if process.Source == "claude" {
		env, err = claudeMessagingEnvironment(ctx, process.PID)
	} else {
		env, err = readProcessEnvironment(ctx, process.PID, clickyprocess.EnvironmentOptions{Keys: deliver.MessagingKeys})
	}
	if err != nil {
		// An unreadable environment falls back to the pane recorded at discovery
		// only while that same process still runs: once it exits, the pane may
		// host another session.
		verifiedPane := process.Surface != nil && process.Surface.SurfaceID != "" &&
			item.ProviderSessionID != "" && declaredSessionID(&process) == item.ProviderSessionID
		if verifiedPane && ctx.Err() == nil {
			snapshot, discoverErr := discoverProcesses(ctx, clickyprocess.SnapshotOptions{})
			if discoverErr != nil {
				return deliver.Target{}, fmt.Errorf("checking session %s's process %d is still running: %w", item.CaptainID, process.PID, discoverErr)
			}
			verifiedPane = stillRunning(snapshot, process.PID, process.StartedAt)
		}
		if !verifiedPane || ctx.Err() != nil {
			return deliver.Target{}, newChatError(http.StatusConflict,
				fmt.Sprintf("session %s's process %d is no longer readable: %v", item.CaptainID, process.PID, err))
		}
	}
	_, declared := sessionprocess.IdentityFromEnvironment(env)
	if process.Source == "codex" {
		declared = env["CODEX_THREAD_ID"]
	}
	if declared != "" && declared != item.ProviderSessionID {
		return deliver.Target{}, newChatError(http.StatusConflict,
			fmt.Sprintf("process %d now runs session %s, not %s", process.PID, declared, item.ProviderSessionID))
	}
	return deliver.Target{
		Source: process.Source, SessionID: item.ProviderSessionID,
		Environment: env, Surface: process.Surface,
	}, nil
}

// stillRunning reports whether snapshot still holds the process discovered as
// pid: present, not exited, and not a later process that reused its PID.
func stillRunning(snapshot *clickyprocess.Snapshot, pid int, startedAt *time.Time) bool {
	current, found := snapshot.Get(pid)
	if !found || current.Status == "zombie" || current.Status == "exiting" || current.Status == "exited" {
		return false
	}
	return startedAt == nil || current.StartedAt == nil || current.StartedAt.Equal(*startedAt)
}

func handleSessionDeliver(w http.ResponseWriter, r *http.Request) {
	var request SessionDeliverRequest
	if !decodeChatRequest(w, r, &request) {
		return
	}
	result, err := RunSessionDeliver(r.Context(), SessionDeliverOptions{ID: r.PathValue("id"), Text: request.Text})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeChatJSON(w, http.StatusAccepted, result)
}

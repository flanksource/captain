package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
)

// ParseClaudeHookPayload maps a Claude Code hook stdin JSON onto a HookEvent.
func ParseClaudeHookPayload(data []byte) (HookEvent, error) {
	var input claude.HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return HookEvent{}, fmt.Errorf("parse claude hook payload: %w", err)
	}
	if input.HookEventName == "" {
		return HookEvent{}, fmt.Errorf("claude hook payload has no hook_event_name")
	}
	detail := input.Source
	if detail == "" {
		detail = input.Reason
	}
	return HookEvent{
		Provider:       "claude",
		Event:          input.HookEventName,
		SessionID:      input.SessionID,
		TranscriptPath: input.TranscriptPath,
		CWD:            input.CWD,
		Detail:         detail,
	}, nil
}

type claudeStatusLinePayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Cost           struct {
		TotalCostUSD *float64 `json:"total_cost_usd"`
	} `json:"cost"`
}

// ParseClaudeStatusLinePayload maps Claude Code's status-line stdin contract
// onto a session-level CLI estimate. The status-line payload is the only normal
// interactive artifact that carries Claude Code's cumulative USD estimate;
// transcript and lifecycle-hook records do not carry it.
func ParseClaudeStatusLinePayload(data []byte) (HookEvent, error) {
	var payload claudeStatusLinePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return HookEvent{}, fmt.Errorf("parse claude status-line payload: %w", err)
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		return HookEvent{}, fmt.Errorf("claude status-line payload has no session_id")
	}
	if strings.TrimSpace(payload.TranscriptPath) == "" {
		return HookEvent{}, fmt.Errorf("claude status-line payload has no transcript_path")
	}
	if payload.Cost.TotalCostUSD == nil || *payload.Cost.TotalCostUSD < 0 {
		return HookEvent{}, fmt.Errorf("claude status-line payload has no nonnegative cost.total_cost_usd")
	}
	return HookEvent{
		Provider: "claude", Event: ClaudeEventStatusLine, SessionID: strings.TrimSpace(payload.SessionID),
		TranscriptPath: payload.TranscriptPath, CWD: payload.CWD,
		ClaudeCLICostUSD: payload.Cost.TotalCostUSD,
	}, nil
}

// codexNotifyPayload is the JSON codex appends as the final notify argv
// argument (codex-rs legacy_notify): kebab-case keys, one event type.
type codexNotifyPayload struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread-id"`
	TurnID   string `json:"turn-id"`
	CWD      string `json:"cwd"`
}

// ParseCodexNotifyPayload maps a codex notify invocation onto a HookEvent.
// Codex passes the payload as the final argv argument; the rollout path is not
// in the payload and is resolved from the thread id by the monitor.
func ParseCodexNotifyPayload(args []string) (HookEvent, error) {
	if len(args) == 0 {
		return HookEvent{}, fmt.Errorf("codex notify payload argument is missing")
	}
	raw := args[len(args)-1]
	var payload codexNotifyPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return HookEvent{}, fmt.Errorf("parse codex notify payload: %w", err)
	}
	if payload.Type == "" {
		return HookEvent{}, fmt.Errorf("codex notify payload has no type")
	}
	return HookEvent{
		Provider:  "codex",
		Event:     payload.Type,
		SessionID: payload.ThreadID,
		CWD:       payload.CWD,
		Detail:    payload.TurnID,
	}, nil
}

// PostHookEvent delivers one hook event to a captain serve instance. Callers
// own the context deadline.
func PostHookEvent(ctx context.Context, baseURL string, ev HookEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode hook event: %w", err)
	}
	url := strings.TrimRight(baseURL, "/") + "/api/captain/hooks/" + ev.Provider
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build hook event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("deliver hook event: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("deliver hook event: %s returned %s", url, resp.Status)
	}
	return nil
}

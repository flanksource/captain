// Package deliver sends a message into an agent session that is already running
// and that captain did not launch, through the channel that agent listens on:
// Claude Code's messaging socket, cmux's hook-delivered agent inbox,
// or the Codex message queue. It never types into the
// agent's prompt and never resumes the session in a second process.
package deliver

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/session/process"
)

type Channel string

const (
	ChannelClaudeSocket Channel = "claude-socket"
	ChannelCodexQueue   Channel = "codex-queue"
	ChannelCmuxMessage  Channel = "cmux-message"
)

// Status is how far delivery is known to have got: sent into the agent's input
// (the socket acknowledges no more), or queued by cmux or codex for the agent to
// read at its next step.
type Status string

const (
	StatusSent   Status = "sent"
	StatusQueued Status = "queued"
)

const (
	EnvMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	EnvMessagingToken  = "CLAUDE_CODE_MESSAGING_TOKEN"
	EnvClaudePID       = "CLAUDE_PID"
	envCodexThread     = "CODEX_THREAD_ID"
)

// MessagingKeys are the environment variables a delivery reads off the live
// agent process: claude's socket and token, plus the session identity keys that
// prove the process still runs the target session. They are read at delivery
// time and never stored, since the token authenticates to the agent's inbox.
var MessagingKeys = []string{"CLAUDE_CODE_SESSION_ID", envCodexThread, EnvClaudePID, EnvMessagingSocket, EnvMessagingToken}

// ClaudeMessagingEnvironment picks the environment claude exported to its
// children: claude creates its messaging socket after it starts, so its own
// environment never holds it, but a child stamped CLAUDE_PID=claudePID does
// (cmux's `hooks claude inbox-wait` helper for as long as the session lives).
// An environment stamped with another CLAUDE_PID was inherited from an
// ancestor claude and is skipped.
func ClaudeMessagingEnvironment(claudePID int, children []map[string]string) map[string]string {
	owner := strconv.Itoa(claudePID)
	for _, env := range children {
		if env[EnvClaudePID] == owner && env[EnvMessagingSocket] != "" {
			return env
		}
	}
	return nil
}

// Target is the live agent a message goes to.
type Target struct {
	// Source is the agent: claude or codex.
	Source string
	// SessionID is the provider's session id (claude session, codex thread).
	SessionID string
	// Environment is the live process's MessagingKeys.
	Environment map[string]string
	Surface     *process.Surface
}

type Result struct {
	Channel Channel `json:"channel"`
	Status  Status  `json:"status"`
}

// CodexRunner runs the codex CLI with args.
type CodexRunner func(ctx context.Context, args ...string) error

// Deliverer holds the clients each channel uses; a nil field uses the default.
type Deliverer struct {
	Cmux  *cmux.Client
	Codex CodexRunner
	Dial  func(ctx context.Context, socket string) (net.Conn, error)
}

// ChooseChannel picks Claude's messaging socket, the Codex queue, or the cmux
// inbox. Codex can run in a cmux pane with its inbox hooks disabled, and cmux
// exposes no per-session drain readiness check.
func ChooseChannel(target Target) (Channel, error) {
	env := target.Environment
	switch {
	case target.Source == "claude" && env[EnvMessagingSocket] != "":
		if env[EnvMessagingToken] == "" {
			return "", fmt.Errorf("claude session %s has %s but no %s", target.SessionID, EnvMessagingSocket, EnvMessagingToken)
		}
		return ChannelClaudeSocket, nil
	case target.Source == "codex":
		if target.SessionID == "" {
			return "", fmt.Errorf("codex session has no session id")
		}
		return ChannelCodexQueue, nil
	case target.Surface != nil && target.Surface.SurfaceID != "":
		return ChannelCmuxMessage, nil
	case target.Source == "claude":
		return "", fmt.Errorf("claude session %s has no child process carrying its %s and is not in a cmux pane", target.SessionID, EnvMessagingSocket)
	default:
		return "", fmt.Errorf("session %s (%s) has no delivery channel: not claude, codex, or in a cmux pane", target.SessionID, target.Source)
	}
}

func (d Deliverer) Deliver(ctx context.Context, target Target, text string) (Result, error) {
	channel, err := ChooseChannel(target)
	if err != nil {
		return Result{}, err
	}
	switch channel {
	case ChannelClaudeSocket:
		err = d.sendClaudeSocket(ctx, target.Environment[EnvMessagingSocket], target.Environment[EnvMessagingToken], text)
		return Result{Channel: channel, Status: StatusSent}, err
	case ChannelCmuxMessage:
		err = d.messageCmux(ctx, target, text)
		return Result{Channel: channel, Status: StatusQueued}, err
	default:
		err = d.queueCodex(ctx, target.SessionID, text)
		return Result{Channel: channel, Status: StatusQueued}, err
	}
}

// Package deliver sends a message into an agent session that is already running
// and that captain did not launch, through the channel that agent listens on:
// Claude Code's messaging socket, the Codex message queue, or, for any agent in
// a cmux pane, typing into its surface. It never resumes the session in a
// second process.
package deliver

import (
	"context"
	"fmt"
	"net"

	"github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/session/process"
)

type Channel string

const (
	ChannelClaudeSocket Channel = "claude-socket"
	ChannelCodexQueue   Channel = "codex-queue"
	ChannelCmux         Channel = "cmux"
)

// Status is how far delivery is known to have got: sent into the agent's input
// (neither the socket nor the surface acknowledges more), or queued by codex for
// its next turn.
type Status string

const (
	StatusSent   Status = "sent"
	StatusQueued Status = "queued"
)

const (
	EnvMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	EnvMessagingToken  = "CLAUDE_CODE_MESSAGING_TOKEN"
	envCodexThread     = "CODEX_THREAD_ID"
)

// MessagingKeys are the environment variables a delivery reads off the live
// agent process. They are read at delivery time and never stored, since the
// token authenticates to the agent's inbox.
var MessagingKeys = []string{"CLAUDE_CODE_SESSION_ID", envCodexThread, EnvMessagingSocket, EnvMessagingToken}

// Target is the live agent a message goes to.
type Target struct {
	// Source is the agent: claude or codex.
	Source string
	// SessionID is the provider's session id (claude session, codex thread).
	SessionID string
	// SessionFile is the agent's transcript, which cmux delivery watches grow.
	SessionFile string
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

// ChooseChannel picks the agent's native channel, else its cmux surface.
func ChooseChannel(target Target) (Channel, error) {
	env := target.Environment
	switch {
	case target.Source == "claude" && env[EnvMessagingSocket] != "":
		if env[EnvMessagingToken] == "" {
			return "", fmt.Errorf("claude session %s has %s but no %s", target.SessionID, EnvMessagingSocket, EnvMessagingToken)
		}
		return ChannelClaudeSocket, nil
	case target.Source == "codex" && env[envCodexThread] != "":
		return ChannelCodexQueue, nil
	case target.Surface != nil && target.Surface.SurfaceID != "" && target.Surface.WorkspaceID != "":
		return ChannelCmux, nil
	case target.Source == "claude":
		return "", fmt.Errorf("claude session %s has no %s and is not in a cmux pane", target.SessionID, EnvMessagingSocket)
	case target.Source == "codex":
		return "", fmt.Errorf("codex session %s has no %s and is not in a cmux pane", target.SessionID, envCodexThread)
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
	case ChannelCodexQueue:
		err = d.queueCodex(ctx, target.Environment[envCodexThread], text)
		return Result{Channel: channel, Status: StatusQueued}, err
	default:
		err = d.submitCmux(ctx, target, text)
		return Result{Channel: channel, Status: StatusSent}, err
	}
}

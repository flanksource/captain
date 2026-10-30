package deliver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// claudeLineLimit is the longest line Claude Code's messaging socket reads
// before it drops the connection.
const claudeLineLimit = 1 << 20

const claudeSocketTimeout = 10 * time.Second

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// sendClaudeSocket writes the auth line and one user message to Claude Code's
// messaging socket, one JSON object per line. The socket never answers: it logs
// a rejected line on its own side, so a clean write and close is all that can
// be confirmed here.
func (d Deliverer) sendClaudeSocket(ctx context.Context, socket, token, text string) error {
	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		return err
	}
	message, err := json.Marshal(map[string]any{"type": "user", "message": claudeMessage{Role: "user", Content: text}})
	if err != nil {
		return err
	}
	if len(message) >= claudeLineLimit {
		return fmt.Errorf("message is %d bytes; claude's messaging socket line limit is %d", len(message), claudeLineLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, claudeSocketTimeout)
	defer cancel()
	dial := d.Dial
	if dial == nil {
		dial = func(ctx context.Context, socket string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}
	}
	conn, err := dial(ctx, socket)
	if err != nil {
		return fmt.Errorf("connect to claude messaging socket %s: %w", socket, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	payload := append(append(append(auth, '\n'), message...), '\n')
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("write to claude messaging socket %s: %w", socket, err)
	}
	return nil
}

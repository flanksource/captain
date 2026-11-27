package deliver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// claudeLineLimit is the longest line Claude Code's messaging socket reads
// before it drops the connection.
const claudeLineLimit = 1 << 20

const claudeSocketTimeout = 10 * time.Second

var errSocketUnavailable = errors.New("claude socket unavailable before sending")

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
		return fmt.Errorf("%w: connect to claude messaging socket %s: %w", errSocketUnavailable, socket, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("%w: set socket write deadline: %w", errSocketUnavailable, err)
		}
	}
	payload := append(append(append(auth, '\n'), message...), '\n')
	n, err := conn.Write(payload)
	if err != nil && n == 0 {
		return fmt.Errorf("%w: write to claude messaging socket %s: %w", errSocketUnavailable, socket, err)
	}
	if err != nil {
		return fmt.Errorf("write to claude messaging socket %s: %w", socket, err)
	}
	if n != len(payload) {
		return fmt.Errorf("short write to claude messaging socket %s: wrote %d of %d bytes", socket, n, len(payload))
	}
	return nil
}

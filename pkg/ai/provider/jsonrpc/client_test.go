package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipePair wires a client to an in-memory peer: the client writes requests to
// cw (peer reads cr) and reads responses from sr (peer writes sw).
func pipePair(t *testing.T, h Handlers) (c *Client, peerIn *bufio.Scanner, peerOut io.Writer) {
	t.Helper()
	cr, cw := io.Pipe() // client stdin  → peer reads
	sr, sw := io.Pipe() // peer stdout   → client reads
	t.Cleanup(func() { _ = cw.Close(); _ = sw.Close() })

	c = New(cw, sr, false, h)
	scanner := bufio.NewScanner(cr)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	return c, scanner, sw
}

func writeFrame(t *testing.T, w io.Writer, f Frame) {
	t.Helper()
	f.JSONRPC = "2.0"
	b, err := json.Marshal(f)
	require.NoError(t, err)
	_, err = w.Write(append(b, '\n'))
	require.NoError(t, err)
}

func TestClient_CallRoundTrip(t *testing.T) {
	c, peerIn, peerOut := pipePair(t, Handlers{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Peer: read the request, echo the id back with a fixed result.
	go func() {
		for peerIn.Scan() {
			var f Frame
			if json.Unmarshal(peerIn.Bytes(), &f) != nil {
				continue
			}
			if f.Method == "add" {
				writeFrame(t, peerOut, Frame{ID: f.ID, Result: json.RawMessage(`{"sum":3}`)})
			}
		}
	}()

	res, err := c.Call(ctx, "add", map[string]int{"a": 1, "b": 2})
	require.NoError(t, err)
	assert.JSONEq(t, `{"sum":3}`, string(res))
}

func TestClient_CallPropagatesRPCError(t *testing.T) {
	c, peerIn, peerOut := pipePair(t, Handlers{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	go func() {
		for peerIn.Scan() {
			var f Frame
			if json.Unmarshal(peerIn.Bytes(), &f) != nil {
				continue
			}
			writeFrame(t, peerOut, Frame{ID: f.ID, Error: &RPCError{Code: -32001, Message: "overloaded"}})
		}
	}()

	_, err := c.Call(ctx, "turn/start", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overloaded")
	assert.Contains(t, err.Error(), "-32001")
}

func TestClient_ServerNotification(t *testing.T) {
	got := make(chan string, 1)
	c, _, peerOut := pipePair(t, Handlers{
		OnNotification: func(method string, _ json.RawMessage) { got <- method },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	writeFrame(t, peerOut, Frame{Method: "turn/completed", Params: json.RawMessage(`{"ok":true}`)})

	select {
	case m := <-got:
		assert.Equal(t, "turn/completed", m)
	case <-ctx.Done():
		t.Fatal("notification not delivered")
	}
}

func TestClient_ServerRequestGetsReply(t *testing.T) {
	c, peerIn, peerOut := pipePair(t, Handlers{
		OnRequest: func(method string, _ json.RawMessage) (any, *RPCError) {
			assert.Equal(t, "permission/request", method)
			return map[string]string{"decision": "approve"}, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// Peer issues a server→client request with a string id.
	writeFrame(t, peerOut, Frame{ID: json.RawMessage(`"r1"`), Method: "permission/request", Params: json.RawMessage(`{"tool":"Bash"}`)})

	require.True(t, peerIn.Scan(), "expected a reply from the client")
	var reply Frame
	require.NoError(t, json.Unmarshal(peerIn.Bytes(), &reply))
	assert.JSONEq(t, `"r1"`, string(reply.ID))
	assert.JSONEq(t, `{"decision":"approve"}`, string(reply.Result))
}

func TestClient_CloseUnblocksPending(t *testing.T) {
	// stdin accepts writes (no peer reader needed); stdout never yields data.
	sr, _ := io.Pipe()
	c := New(io.Discard, sr, false, Handlers{})

	errc := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), "noreply", nil)
		errc <- err
	}()

	// Give the Call time to register as pending before closing.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, c.Close())

	select {
	case err := <-errc:
		assert.ErrorIs(t, err, ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not unblock on Close")
	}
}

func TestIDKey_NumberAndString(t *testing.T) {
	assert.Equal(t, "5", idKey(json.RawMessage(`5`)))
	assert.Equal(t, "abc", idKey(json.RawMessage(`"abc"`)))
}

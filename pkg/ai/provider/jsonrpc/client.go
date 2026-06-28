package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned to in-flight Call/Notify once the client's read loop has
// stopped (process exit, EOF, or Close).
var ErrClosed = errors.New("jsonrpc: client closed")

// maxLineBytes bounds a single JSON-RPC frame. Matches the codex_cli scanner
// limit; agent tool payloads (large diffs, file contents) can be sizeable.
const maxLineBytes = 4 * 1024 * 1024

// Client speaks newline-delimited JSON-RPC 2.0 over a child's stdin/stdout.
// A single Run goroutine owns the read side; Call/Notify are safe for concurrent
// use and serialize writes internally.
type Client struct {
	w           io.Writer
	r           io.Reader
	omitVersion bool
	h           Handlers

	wmu     sync.Mutex // serializes frame writes (one JSON object per line)
	pending sync.Map   // idKey(string) -> chan Frame
	nextID  atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}
}

// New builds a client over the given stdin (writer) and stdout (reader) of a
// child process. omitVersion drops the "jsonrpc":"2.0" field on outbound frames
// (required by codex app-server). Call Run to start the read loop.
func New(stdin io.Writer, stdout io.Reader, omitVersion bool, h Handlers) *Client {
	return &Client{
		w:           stdin,
		r:           stdout,
		omitVersion: omitVersion,
		h:           h,
		closed:      make(chan struct{}),
	}
}

// Run reads frames until the reader hits EOF/error or ctx is cancelled, routing
// responses to their waiting Call, and server-initiated traffic to Handlers. It
// returns the terminating error (nil on clean EOF). Run also runs Close on exit
// so pending calls unblock.
func (c *Client) Run(ctx context.Context) error {
	defer c.Close()

	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(c.r)
		scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
		for scanner.Scan() {
			// Copy: scanner reuses its buffer between Scan calls.
			b := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- b:
			case <-ctx.Done():
				return
			}
		}
		scanErr <- scanner.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-scanErr:
			return err
		case line := <-lines:
			c.dispatch(line)
		}
	}
}

func (c *Client) dispatch(line []byte) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return // ignore non-JSON / heartbeat noise
	}
	switch {
	case f.isResponse():
		if chAny, ok := c.pending.LoadAndDelete(idKey(f.ID)); ok {
			chAny.(chan Frame) <- f
		}
	case f.isRequest():
		c.handleServerRequest(f)
	case f.isNotification():
		if c.h.OnNotification != nil {
			c.h.OnNotification(f.Method, f.Params)
		}
	}
}

func (c *Client) handleServerRequest(f Frame) {
	if c.h.OnRequest == nil {
		_ = c.write(Frame{ID: f.ID, Error: &RPCError{Code: -32601, Message: "method not found: " + f.Method}})
		return
	}
	result, rpcErr := c.h.OnRequest(f.Method, f.Params)
	reply := Frame{ID: f.ID}
	if rpcErr != nil {
		reply.Error = rpcErr
	} else if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			reply.Error = &RPCError{Code: -32603, Message: "marshal result: " + err.Error()}
		} else {
			reply.Result = raw
		}
	} else {
		reply.Result = json.RawMessage("null")
	}
	_ = c.write(reply)
}

// Call issues a request and blocks until the matching response arrives, ctx is
// cancelled, or the client closes. A non-nil RPCError in the response is
// returned as an error.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	n := c.nextID.Add(1)
	key := strconv.FormatInt(n, 10)
	id := json.RawMessage(key)

	ch := make(chan Frame, 1)
	c.pending.Store(key, ch)
	defer c.pending.Delete(key)

	f := Frame{ID: id, Method: method}
	if err := c.encodeParams(&f, params); err != nil {
		return nil, err
	}
	if err := c.write(f); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("jsonrpc %q: error %d: %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrClosed
	}
}

// Notify sends an id-less notification and returns once it is written.
func (c *Client) Notify(method string, params any) error {
	f := Frame{Method: method}
	if err := c.encodeParams(&f, params); err != nil {
		return err
	}
	return c.write(f)
}

// Close stops the client and unblocks any pending Call with ErrClosed. It is
// idempotent and safe to call from any goroutine.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *Client) encodeParams(f *Frame, params any) error {
	if params == nil {
		return nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("jsonrpc marshal params: %w", err)
	}
	f.Params = raw
	return nil
}

func (c *Client) write(f Frame) error {
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	if !c.omitVersion {
		f.JSONRPC = "2.0"
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("jsonrpc marshal frame: %w", err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("jsonrpc write: %w", err)
	}
	return nil
}

// idKey normalizes a raw JSON id (number or string) to a stable map key so a
// response matches the request that allocated the id, regardless of how the peer
// re-serialized it.
func idKey(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case string:
		return n
	default:
		return string(raw)
	}
}

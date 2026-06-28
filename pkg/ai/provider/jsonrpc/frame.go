// Package jsonrpc implements a minimal newline-delimited JSON-RPC 2.0 client
// over a child process's stdin/stdout. It is the shared transport for captain's
// agent-backed providers: the Claude Agent SDK process (pkg/ai/provider/claudeagent)
// and `codex app-server` (pkg/ai/provider/codex_appserver).
//
// The transport is fully bidirectional: the client issues id-correlated requests
// (Call) and id-less notifications (Notify), while the server may push its own
// notifications (Handlers.OnNotification) and id-bearing requests such as tool
// approvals (Handlers.OnRequest). codex app-server omits the "jsonrpc" field on
// the wire, so New takes an omitVersion toggle.
package jsonrpc

import "encoding/json"

// Frame is the union wire shape. A frame is a request/notification when Method
// is set, and a response when Result or Error is set. ID is absent for
// notifications and present for requests and their responses.
type Frame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// isResponse reports whether the frame carries a response payload for a prior
// request (it has an id and either a result or an error, and no method).
func (f Frame) isResponse() bool {
	return f.Method == "" && len(f.ID) > 0 && (len(f.Result) > 0 || f.Error != nil)
}

// isRequest reports whether the frame is a server→client request (method + id).
func (f Frame) isRequest() bool { return f.Method != "" && len(f.ID) > 0 }

// isNotification reports whether the frame is a server→client notification
// (method, no id).
func (f Frame) isNotification() bool { return f.Method != "" && len(f.ID) == 0 }

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// Handlers receives server-initiated traffic. Either field may be nil, in which
// case that traffic is ignored (a request with no handler is answered with a
// method-not-found error so the peer is not left waiting).
type Handlers struct {
	// OnNotification is invoked for every id-less server→client message.
	OnNotification func(method string, params json.RawMessage)
	// OnRequest is invoked for every id-bearing server→client request (e.g. an
	// approval prompt). The returned value is marshalled as the result; a
	// non-nil *RPCError is sent as the error instead.
	OnRequest func(method string, params json.RawMessage) (any, *RPCError)
}

// Package callertools exposes caller-owned Go tool handlers to out-of-process
// agent runtimes through a private authenticated MCP endpoint.
package callertools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	endpointPath           = "/mcp"
	serverName             = "captain"
	defaultApprovalTimeout = 5 * time.Minute
	maxMCPRequestBytes     = 4 << 20
	// RemoteEndpointPrefix is mounted by captain serve for task-scoped caller
	// tool capabilities. The random capability ID selects authority; the bearer
	// secret still authenticates it.
	RemoteEndpointPrefix = "/caller-tools/"
	TaskHeader           = "X-Captain-Task"
	AgentHeader          = "X-Captain-Agent"
	// ToolUseIDInputKey carries an out-of-process provider's tool call ID
	// through clients that cannot attach MCP request metadata. The runtime
	// removes it before policy, schema validation, and handler execution.
	ToolUseIDInputKey = "__captain_tool_use_id"
)

var callertoolsLog = logger.GetLogger("ai")

// AuditEvent records a capability decision without request inputs, results, or
// bearer values.
type AuditEvent struct {
	Action string
	TaskID string
	Agent  string
	Tool   string
	Result string
	Reason string
}

// Options defines one private caller-tool capability.
type Options struct {
	Definitions []api.ToolDefinition
	Preferences api.ToolPreferences
	// Policy is the ordered, last-match-wins rule list layered after Preferences.
	Policy     api.PermissionPolicy
	CanUseTool api.PermissionFunc
	SessionID  string
	ExpiresAt  time.Time
	// ValidateCredential rechecks the persisted lease on every request and
	// immediately before a tool handler executes.
	ValidateCredential func(context.Context) error
	Audit              func(AuditEvent)
	// ObserveDelegatedTool publishes the tool-use lifecycle reconstructed from
	// an authenticated remote MCP call. Local providers publish their own events.
	ObserveDelegatedTool func(context.Context, api.Event) error

	ApprovalTimeout time.Duration
}

// Runtime owns one loopback-only MCP server and its in-memory bearer
// credential. Closing it revokes the capability by shutting down the listener.
type Runtime struct {
	definitions          map[string]api.ToolDefinition
	schemas              map[string]*jsonschema.Schema
	canUseTool           api.PermissionFunc
	validate             func(context.Context) error
	sessionID            string
	token                string
	tokenHash            [sha256.Size]byte
	expiresAt            time.Time
	audit                func(AuditEvent)
	observeDelegatedTool func(context.Context, api.Event) error

	approvalTimeout time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	revoked         atomic.Bool

	endpoint api.CallerToolEndpoint
	server   *http.Server
	listener net.Listener
	mcpHTTP  http.Handler

	closeOnce sync.Once
	closeErr  error
}

type delegationContextKey struct{}

type delegatedCapability struct {
	runtime   *Runtime
	id        string
	tokenHash [sha256.Size]byte
	binding   api.CallerToolBinding
	tools     map[string]struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	revoked   atomic.Bool
	expired   atomic.Bool
}

var remoteCapabilities = struct {
	sync.RWMutex
	values map[string]*delegatedCapability
}{values: map[string]*delegatedCapability{}}

var remoteHandlerEnabled atomic.Bool

// New validates and resolves the tool policy before starting a private server.
func New(options Options) (*Runtime, error) {
	if !options.ExpiresAt.IsZero() && !options.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("caller-tool credential expiry must be in the future")
	}
	if options.ApprovalTimeout < 0 {
		return nil, fmt.Errorf("caller-tool approval timeout cannot be negative")
	}
	if options.ApprovalTimeout == 0 {
		options.ApprovalTimeout = defaultApprovalTimeout
	}
	definitions, err := aitools.ResolveDefinitions(options.Definitions, aitools.ResolveOptions{Preferences: options.Preferences, Policy: options.Policy})
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, fmt.Errorf("caller-tool runtime requires at least one enabled tool")
	}
	token, err := capabilityToken(options.SessionID)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for caller tools: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{
		definitions:          make(map[string]api.ToolDefinition, len(definitions)),
		schemas:              make(map[string]*jsonschema.Schema, len(definitions)),
		canUseTool:           options.CanUseTool,
		validate:             options.ValidateCredential,
		audit:                options.Audit,
		observeDelegatedTool: options.ObserveDelegatedTool,
		sessionID:            options.SessionID,
		token:                token,
		tokenHash:            sha256.Sum256([]byte(token)),
		expiresAt:            options.ExpiresAt,
		approvalTimeout:      options.ApprovalTimeout,
		ctx:                  ctx,
		cancel:               cancel,
		listener:             listener,
	}
	// Validation stays in the handler because out-of-process providers add the
	// synthetic tool-use ID that must be removed before strict schema checks.
	mcpServer := server.NewMCPServer(
		"captain-caller-tools",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithToolFilter(runtime.filterTools),
	)
	for _, definition := range definitions {
		runtime.definitions[definition.Name] = definition
		tool, schema, err := mcpTool(definition)
		if err != nil {
			cancel()
			_ = listener.Close()
			return nil, err
		}
		runtime.schemas[definition.Name] = schema
		mcpServer.AddTool(tool, runtime.handler(definition))
	}
	handler := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithStateLess(true),
		server.WithEndpointPath(endpointPath),
	)
	runtime.mcpHTTP = runtime.guardToolName(handler)
	runtime.server = &http.Server{
		Handler:           runtime.authorizeLocal(runtime.mcpHTTP),
		ReadHeaderTimeout: 5 * time.Second,
	}
	runtime.endpoint = api.CallerToolEndpoint{
		Name: serverName,
		URL:  "http://" + listener.Addr().String() + endpointPath,
		Headers: map[string]string{
			"Authorization": "Bearer " + token,
		},
		Delegate: runtime.delegate,
	}
	go func() {
		_ = runtime.server.Serve(listener)
	}()
	return runtime, nil
}

// Endpoint returns a copy so callers cannot mutate the runtime's credential.
func (r *Runtime) Endpoint() api.CallerToolEndpoint {
	endpoint := r.endpoint
	endpoint.Headers = cloneHeaders(r.endpoint.Headers)
	return endpoint
}

// RemoteHandler serves delegated capabilities from the Captain supervisor.
// Mount it at RemoteEndpointPrefix outside generic API-token middleware: each
// request is authenticated by its own task capability instead.
func RemoteHandler() http.Handler {
	remoteHandlerEnabled.Store(true)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		id, ok := remoteCapabilityID(request.URL.Path)
		if !ok {
			http.NotFound(w, request)
			return
		}
		remoteCapabilities.RLock()
		capability := remoteCapabilities.values[id]
		remoteCapabilities.RUnlock()
		if capability == nil {
			writeCredentialRejection(w)
			return
		}
		capability.serveHTTP(w, request)
	})
}

// CredentialHash returns the SHA-256 bearer hash persisted by an authority.
// The plaintext capability remains confined to Endpoint headers.
func (r *Runtime) CredentialHash() []byte {
	hash := make([]byte, len(r.tokenHash))
	copy(hash, r.tokenHash[:])
	return hash
}

// Close revokes the endpoint and is safe to call more than once.
func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.Revoke()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.closeErr = r.server.Shutdown(ctx)
	})
	return r.closeErr
}

// Revoke invalidates the capability immediately and cancels active calls.
func (r *Runtime) Revoke() {
	if r.revoked.CompareAndSwap(false, true) {
		r.cancel()
		var capabilities []*delegatedCapability
		remoteCapabilities.Lock()
		for id, capability := range remoteCapabilities.values {
			if capability.runtime == r {
				delete(remoteCapabilities.values, id)
				capabilities = append(capabilities, capability)
			}
		}
		remoteCapabilities.Unlock()
		for _, capability := range capabilities {
			capability.revoke("parent_revoked")
		}
	}
}

func (r *Runtime) delegate(_ context.Context, binding api.CallerToolBinding) (*api.CallerToolDelegation, error) {
	if !remoteHandlerEnabled.Load() {
		return nil, fmt.Errorf("remote caller tools require captain serve to mount the authenticated talkback handler")
	}
	if strings.TrimSpace(binding.TaskID) == "" || strings.TrimSpace(binding.Agent) == "" {
		return nil, fmt.Errorf("remote caller-tool capability requires task and agent bindings")
	}
	if len(binding.ToolNames) == 0 {
		return nil, fmt.Errorf("remote caller-tool capability requires at least one delegated tool")
	}
	if !binding.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("remote caller-tool capability expiry must be in the future")
	}
	if err := r.validateActive(context.Background()); err != nil {
		return nil, err
	}
	tools := make(map[string]struct{}, len(binding.ToolNames))
	for _, name := range binding.ToolNames {
		name = strings.TrimSpace(name)
		if _, authorized := r.definitions[name]; authorized {
			tools[name] = struct{}{}
		} else {
			r.auditEvent(binding, AuditEvent{
				Action: "issuance", Tool: name, Result: "denied", Reason: "parent_not_authorized",
			})
		}
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("none of the requested caller tools are authorized by the supervisor")
	}
	id, err := randomID(18)
	if err != nil {
		return nil, fmt.Errorf("generate remote caller-tool capability ID: %w", err)
	}
	secret, err := randomID(32)
	if err != nil {
		return nil, fmt.Errorf("generate remote caller-tool credential: %w", err)
	}
	token := "cap_" + id + "." + secret
	capabilityCtx, capabilityCancel := context.WithCancel(r.ctx)
	capability := &delegatedCapability{
		runtime: r, id: id, tokenHash: sha256.Sum256([]byte(token)), binding: binding,
		tools: tools, ctx: capabilityCtx, cancel: capabilityCancel,
	}
	remoteCapabilities.Lock()
	if !r.active() {
		remoteCapabilities.Unlock()
		capabilityCancel()
		return nil, fmt.Errorf("caller-tool credential is inactive")
	}
	remoteCapabilities.values[id] = capability
	remoteCapabilities.Unlock()
	time.AfterFunc(time.Until(binding.ExpiresAt), capability.expire)
	r.auditEvent(binding, AuditEvent{Action: "issuance", Result: "issued"})
	delegation := &api.CallerToolDelegation{
		Endpoint: api.CallerToolEndpoint{
			Name: serverName,
			// The sidecar extracts this path and replaces the placeholder origin
			// with the enrolled supervisor URL; this value is never dialed.
			URL: "http://127.0.0.1" + RemoteEndpointPrefix + id + endpointPath,
			Headers: map[string]string{
				"Authorization": "Bearer " + token,
				TaskHeader:      binding.TaskID,
				AgentHeader:     binding.Agent,
			},
		},
		Revoke: func() {
			remoteCapabilities.Lock()
			if current := remoteCapabilities.values[id]; current == capability {
				delete(remoteCapabilities.values, id)
			}
			remoteCapabilities.Unlock()
			capability.revoke("task_completed")
		},
	}
	return delegation, nil
}

func (r *Runtime) filterTools(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	filtered := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if r.toolAuthorized(ctx, tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (r *Runtime) handler(definition api.ToolDefinition) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !r.toolAuthorized(ctx, definition.Name) {
			r.auditCall(ctx, definition.Name, "denied", "tool_not_delegated")
			return nil, fmt.Errorf("caller tool %q is not authorized", definition.Name)
		}
		callCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.ctx, cancel)
		defer stop()
		defer cancel()
		capability, delegated := ctx.Value(delegationContextKey{}).(*delegatedCapability)
		if delegated {
			stopCapability := context.AfterFunc(capability.ctx, cancel)
			defer stopCapability()
		}
		input := request.GetArguments()
		if input == nil {
			input = map[string]any{}
		}
		toolUseID, generatedToolUseID, err := toolUseID(request, input)
		if err != nil {
			r.auditCall(ctx, definition.Name, "denied", "invalid_tool_use_id")
			return mcp.NewToolResultError(err.Error()), nil
		}
		delegatedObserved := false
		fail := func(message, result, reason string) (*mcp.CallToolResult, error) {
			if delegatedObserved {
				eventErr := r.observeDelegated(callCtx, api.Event{
					Kind: api.EventToolResult, Tool: definition.Name, ToolCallID: toolUseID,
					Text: message, Success: false, Delegated: true,
				})
				if eventErr != nil {
					r.auditCall(ctx, definition.Name, "error", "event_delivery_failed")
					return mcp.NewToolResultErrorf("%s (record delegated tool result: %v)", message, eventErr), nil
				}
			}
			r.auditCall(ctx, definition.Name, result, reason)
			return mcp.NewToolResultError(message), nil
		}
		if delegated {
			if err := r.observeDelegated(callCtx, api.Event{
				Kind: api.EventToolUse, Tool: definition.Name, ToolCallID: toolUseID, Input: input, Delegated: true,
			}); err != nil {
				r.auditCall(ctx, definition.Name, "error", "event_delivery_failed")
				return mcp.NewToolResultErrorf("record delegated caller-tool use: %v", err), nil
			}
			delegatedObserved = true
		}
		if err := r.validateInput(definition.Name, input); err != nil {
			return fail(err.Error(), "denied", "invalid_input")
		}
		if definition.NeedsApproval() {
			if r.canUseTool == nil {
				return fail("tool approval is required but no approval broker is configured", "denied", "approval_broker_unavailable")
			}
			approvalCtx, approvalCancel := context.WithTimeout(callCtx, r.approvalTimeout)
			decision, err := r.canUseTool(approvalCtx, api.PermissionRequest{
				Tool: definition.Name, Input: input, ToolUseID: toolUseID,
				ToolUseIDGenerated: generatedToolUseID, Delegated: delegated, SessionID: r.sessionID,
			})
			approvalCancel()
			if err != nil {
				return fail(err.Error(), "denied", "approval_failed")
			}
			if !decision.Allow {
				message := decision.Message
				if message == "" {
					message = "tool call denied"
				}
				return fail(message, "denied", "approval_denied")
			}
			if decision.UpdatedInput != nil {
				input = decision.UpdatedInput
			}
		}
		if err := r.validateInput(definition.Name, input); err != nil {
			return fail(err.Error(), "denied", "invalid_input")
		}
		if err := r.validateActive(callCtx); err != nil {
			return fail(err.Error(), "denied", "capability_inactive")
		}
		output, err := definition.Handler(callCtx, input)
		if err != nil {
			return fail(err.Error(), "error", "handler_failed")
		}
		result, err := mcp.NewToolResultJSON(output)
		if err != nil {
			return fail(fmt.Sprintf("marshal caller tool %q result: %v", definition.Name, err), "error", "result_encoding_failed")
		}
		if delegatedObserved {
			encoded, err := json.Marshal(output)
			if err != nil {
				return fail(fmt.Sprintf("marshal caller tool %q result: %v", definition.Name, err), "error", "result_encoding_failed")
			}
			if err := r.observeDelegated(callCtx, api.Event{
				Kind: api.EventToolResult, Tool: definition.Name, ToolCallID: toolUseID,
				Text: string(encoded), Success: true, Delegated: true,
			}); err != nil {
				r.auditCall(ctx, definition.Name, "error", "event_delivery_failed")
				return mcp.NewToolResultErrorf("record delegated caller-tool result: %v", err), nil
			}
		}
		r.auditCall(ctx, definition.Name, "allowed", "")
		return result, nil
	}
}

func (r *Runtime) observeDelegated(ctx context.Context, event api.Event) error {
	if r.observeDelegatedTool == nil {
		return nil
	}
	return r.observeDelegatedTool(ctx, event)
}

func (r *Runtime) authorizeLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "caller-tool endpoint requires loopback access", http.StatusForbidden)
			return
		}
		if strings.TrimSpace(request.Header.Get("Origin")) != "" {
			http.Error(w, "caller-tool endpoint does not accept browser origins", http.StatusForbidden)
			return
		}
		actual := request.Header.Get("Authorization")
		expected := "Bearer " + r.token
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 ||
			r.validateActive(request.Context()) != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "invalid caller-tool credential", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (r *Runtime) guardToolName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Body == nil {
			next.ServeHTTP(w, request)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxMCPRequestBytes+1))
		if err != nil || len(body) > maxMCPRequestBytes {
			http.Error(w, "caller-tool MCP request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		var call struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &call) == nil && call.Method == "tools/call" {
			if !r.toolAuthorized(request.Context(), call.Params.Name) {
				r.auditCall(request.Context(), call.Params.Name, "denied", "tool_not_delegated")
				http.Error(w, fmt.Sprintf("caller tool %q is not authorized", call.Params.Name), http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, request)
	})
}

func (r *Runtime) active() bool {
	return !r.revoked.Load() && (r.expiresAt.IsZero() || time.Now().Before(r.expiresAt))
}

func (r *Runtime) validateActive(ctx context.Context) error {
	if !r.active() {
		return fmt.Errorf("caller-tool credential is inactive")
	}
	if capability, ok := ctx.Value(delegationContextKey{}).(*delegatedCapability); ok && !capability.active() {
		return fmt.Errorf("delegated caller-tool credential is inactive")
	}
	if r.validate != nil {
		if err := r.validate(ctx); err != nil {
			return fmt.Errorf("validate caller-tool credential: %w", err)
		}
	}
	return nil
}

func (r *Runtime) toolAuthorized(ctx context.Context, name string) bool {
	if _, ok := r.definitions[name]; !ok {
		return false
	}
	capability, ok := ctx.Value(delegationContextKey{}).(*delegatedCapability)
	if !ok {
		return true
	}
	_, ok = capability.tools[name]
	return ok
}

func remoteCapabilityID(path string) (string, bool) {
	trimmed, ok := strings.CutPrefix(path, RemoteEndpointPrefix)
	if !ok {
		return "", false
	}
	id, suffix, ok := strings.Cut(trimmed, "/")
	return id, ok && id != "" && suffix == strings.TrimPrefix(endpointPath, "/")
}

func (capability *delegatedCapability) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if strings.TrimSpace(request.Header.Get("Origin")) != "" {
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "authentication", Result: "denied", Reason: "browser_origin"})
		http.Error(w, "caller-tool endpoint does not accept browser origins", http.StatusForbidden)
		return
	}
	presented, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	actual := sha256.Sum256([]byte(strings.TrimSpace(presented)))
	if !ok || subtle.ConstantTimeCompare(actual[:], capability.tokenHash[:]) != 1 {
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "authentication", Result: "denied", Reason: "invalid_credential"})
		writeCredentialRejection(w)
		return
	}
	if request.Header.Get(TaskHeader) != capability.binding.TaskID {
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "authentication", Result: "denied", Reason: "wrong_task"})
		http.Error(w, "caller-tool capability is not valid for this task", http.StatusForbidden)
		return
	}
	if request.Header.Get(AgentHeader) != capability.binding.Agent {
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "authentication", Result: "denied", Reason: "wrong_agent"})
		http.Error(w, "caller-tool capability is not valid for this agent", http.StatusForbidden)
		return
	}
	if !capability.active() || capability.runtime.validateActive(request.Context()) != nil {
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "authentication", Result: "denied", Reason: "inactive"})
		writeCredentialRejection(w)
		return
	}
	forward := request.Clone(context.WithValue(request.Context(), delegationContextKey{}, capability))
	forward.URL = cloneURL(request.URL)
	forward.URL.Path = endpointPath
	forward.URL.RawPath = ""
	capability.runtime.mcpHTTP.ServeHTTP(w, forward)
}

func (capability *delegatedCapability) active() bool {
	if capability.revoked.Load() || capability.expired.Load() {
		return false
	}
	if time.Now().Before(capability.binding.ExpiresAt) {
		return true
	}
	capability.expire()
	return false
}

func (capability *delegatedCapability) expire() {
	if capability.revoked.Load() || !capability.expired.CompareAndSwap(false, true) {
		return
	}
	remoteCapabilities.Lock()
	if remoteCapabilities.values[capability.id] == capability {
		delete(remoteCapabilities.values, capability.id)
	}
	remoteCapabilities.Unlock()
	capability.cancel()
	capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "expiry", Result: "expired"})
}

func (capability *delegatedCapability) revoke(reason string) {
	if capability.revoked.CompareAndSwap(false, true) {
		capability.cancel()
		capability.runtime.auditEvent(capability.binding, AuditEvent{Action: "revocation", Result: "revoked", Reason: reason})
	}
}

func (r *Runtime) auditCall(ctx context.Context, tool, result, reason string) {
	capability, _ := ctx.Value(delegationContextKey{}).(*delegatedCapability)
	if capability == nil {
		return
	}
	r.auditEvent(capability.binding, AuditEvent{Action: "call", Tool: tool, Result: result, Reason: reason})
}

func (r *Runtime) auditEvent(binding api.CallerToolBinding, event AuditEvent) {
	event.TaskID = binding.TaskID
	event.Agent = binding.Agent
	if r.audit != nil {
		r.audit(event)
		return
	}
	callertoolsLog.Infof("caller-tool capability action=%s task=%s agent=%s tool=%s result=%s reason=%s",
		event.Action, event.TaskID, event.Agent, event.Tool, event.Result, event.Reason)
}

func writeCredentialRejection(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "invalid caller-tool credential", http.StatusUnauthorized)
}

func cloneURL(value *url.URL) *url.URL {
	cloned := *value
	return &cloned
}

func mcpTool(definition api.ToolDefinition) (mcp.Tool, *jsonschema.Schema, error) {
	schema := definition.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return mcp.Tool{}, nil, fmt.Errorf("marshal caller tool %q schema: %w", definition.Name, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return mcp.Tool{}, nil, fmt.Errorf("decode caller tool %q schema: %w", definition.Name, err)
	}
	compiler := jsonschema.NewCompiler()
	resourceURL := "mem:///captain/caller-tools/" + definition.Name + "/input-schema.json"
	if err := compiler.AddResource(resourceURL, document); err != nil {
		return mcp.Tool{}, nil, fmt.Errorf("register caller tool %q schema: %w", definition.Name, err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return mcp.Tool{}, nil, fmt.Errorf("compile caller tool %q schema: %w", definition.Name, err)
	}
	tool := mcp.NewToolWithRawSchema(definition.Name, definition.Description, raw)
	tool.Annotations = mcp.ToolAnnotation{
		ReadOnlyHint: definition.ReadOnlyHint, DestructiveHint: definition.DestructiveHint,
		IdempotentHint: definition.IdempotentHint,
	}
	return tool, compiled, nil
}

func (r *Runtime) validateInput(toolName string, input map[string]any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal caller tool %q input: %w", toolName, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode caller tool %q input: %w", toolName, err)
	}
	if err := r.schemas[toolName].Validate(document); err != nil {
		return fmt.Errorf("caller tool %q input is invalid: %w", toolName, err)
	}
	return nil
}

func capabilityToken(sessionID string) (string, error) {
	secret, err := randomID(32)
	if err != nil {
		return "", fmt.Errorf("generate caller-tool credential: %w", err)
	}
	return "cap_" + capabilityIdentity(sessionID) + "." + secret, nil
}

func toolUseID(request mcp.CallToolRequest, input map[string]any) (string, bool, error) {
	inputID := ""
	if value, exists := input[ToolUseIDInputKey]; exists {
		delete(input, ToolUseIDInputKey)
		var ok bool
		inputID, ok = value.(string)
		if !ok || strings.TrimSpace(inputID) == "" {
			return "", false, fmt.Errorf("caller-tool provider ID must be a non-empty string")
		}
	}
	metadataID := ""
	if request.Params.Meta != nil {
		if value, ok := request.Params.Meta.AdditionalFields["toolUseId"].(string); ok && strings.TrimSpace(value) != "" {
			metadataID = value
		}
	}
	if inputID != "" && metadataID != "" && inputID != metadataID {
		return "", false, fmt.Errorf("caller-tool provider ID conflicts with MCP metadata")
	}
	if metadataID != "" {
		return metadataID, false, nil
	}
	if inputID != "" {
		return inputID, false, nil
	}
	id, err := randomID(16)
	if err != nil {
		return "", false, fmt.Errorf("generate caller-tool call ID: %w", err)
	}
	return "mcp_" + id, true, nil
}

func randomID(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func capabilityIdentity(sessionID string) string {
	identity := strings.Map(func(value rune) rune {
		switch {
		case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z', value >= '0' && value <= '9', value == '-', value == '_':
			return value
		default:
			return '_'
		}
	}, strings.TrimSpace(sessionID))
	if identity == "" {
		return "run"
	}
	return identity
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}

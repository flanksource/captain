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
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	endpointPath           = "/mcp"
	serverName             = "captain"
	defaultApprovalTimeout = 5 * time.Minute
	// ToolUseIDInputKey carries an out-of-process provider's tool call ID
	// through clients that cannot attach MCP request metadata. The runtime
	// removes it before policy, schema validation, and handler execution.
	ToolUseIDInputKey = "__captain_tool_use_id"
)

// Options defines one private caller-tool capability.
type Options struct {
	Definitions []api.ToolDefinition
	Preferences api.ToolPreferences
	CanUseTool  api.PermissionFunc
	SessionID   string
	ExpiresAt   time.Time
	// ValidateCredential rechecks the persisted lease on every request and
	// immediately before a tool handler executes.
	ValidateCredential func(context.Context) error

	ApprovalTimeout time.Duration
}

// Runtime owns one loopback-only MCP server and its in-memory bearer
// credential. Closing it revokes the capability by shutting down the listener.
type Runtime struct {
	definitions map[string]api.ToolDefinition
	schemas     map[string]*jsonschema.Schema
	canUseTool  api.PermissionFunc
	validate    func(context.Context) error
	sessionID   string
	token       string
	tokenHash   [sha256.Size]byte
	expiresAt   time.Time

	approvalTimeout time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	revoked         atomic.Bool

	endpoint api.CallerToolEndpoint
	server   *http.Server
	listener net.Listener

	closeOnce sync.Once
	closeErr  error
}

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
	definitions, err := aitools.ResolveDefinitions(options.Definitions, options.Preferences)
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
		definitions:     make(map[string]api.ToolDefinition, len(definitions)),
		schemas:         make(map[string]*jsonschema.Schema, len(definitions)),
		canUseTool:      options.CanUseTool,
		validate:        options.ValidateCredential,
		sessionID:       options.SessionID,
		token:           token,
		tokenHash:       sha256.Sum256([]byte(token)),
		expiresAt:       options.ExpiresAt,
		approvalTimeout: options.ApprovalTimeout,
		ctx:             ctx,
		cancel:          cancel,
		listener:        listener,
	}
	mcpServer := server.NewMCPServer(
		"captain-caller-tools",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithToolFilter(runtime.filterTools),
		server.WithInputSchemaValidation(),
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
	runtime.server = &http.Server{
		Handler:           runtime.authorize(handler),
		ReadHeaderTimeout: 5 * time.Second,
	}
	runtime.endpoint = api.CallerToolEndpoint{
		Name: serverName,
		URL:  "http://" + listener.Addr().String() + endpointPath,
		Headers: map[string]string{
			"Authorization": "Bearer " + token,
		},
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
	}
}

func (r *Runtime) filterTools(_ context.Context, tools []mcp.Tool) []mcp.Tool {
	filtered := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if _, ok := r.definitions[tool.Name]; ok {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (r *Runtime) handler(definition api.ToolDefinition) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := r.definitions[definition.Name]; !ok {
			return nil, fmt.Errorf("caller tool %q is not authorized", definition.Name)
		}
		callCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.ctx, cancel)
		defer stop()
		defer cancel()
		input := request.GetArguments()
		if input == nil {
			input = map[string]any{}
		}
		toolUseID, generatedToolUseID, err := toolUseID(request, input)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if definition.NeedsApproval() {
			if r.canUseTool == nil {
				return mcp.NewToolResultError("tool approval is required but no approval broker is configured"), nil
			}
			approvalCtx, approvalCancel := context.WithTimeout(callCtx, r.approvalTimeout)
			decision, err := r.canUseTool(approvalCtx, api.PermissionRequest{
				Tool: definition.Name, Input: input, ToolUseID: toolUseID,
				ToolUseIDGenerated: generatedToolUseID, SessionID: r.sessionID,
			})
			approvalCancel()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !decision.Allow {
				message := decision.Message
				if message == "" {
					message = "tool call denied"
				}
				return mcp.NewToolResultError(message), nil
			}
			if decision.UpdatedInput != nil {
				input = decision.UpdatedInput
			}
		}
		if err := r.validateActive(callCtx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := r.validateInput(definition.Name, input); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		output, err := definition.Handler(callCtx, input)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		result, err := mcp.NewToolResultJSON(output)
		if err != nil {
			return mcp.NewToolResultErrorf("marshal caller tool %q result: %v", definition.Name, err), nil
		}
		return result, nil
	}
}

func (r *Runtime) authorize(next http.Handler) http.Handler {
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

func (r *Runtime) active() bool {
	return !r.revoked.Load() && (r.expiresAt.IsZero() || time.Now().Before(r.expiresAt))
}

func (r *Runtime) validateActive(ctx context.Context) error {
	if !r.active() {
		return fmt.Errorf("caller-tool credential is inactive")
	}
	if r.validate != nil {
		if err := r.validate(ctx); err != nil {
			return fmt.Errorf("validate caller-tool credential: %w", err)
		}
	}
	return nil
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

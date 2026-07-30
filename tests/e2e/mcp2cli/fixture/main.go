// Package main provides the authenticated MCP process used by Captain's
// black-box tests. Its stdout is reserved for one readiness record.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultUsername = "captain-e2e"
	defaultPassword = "captain-e2e-password"
	endpointPath    = "/mcp"
)

type contextKey struct{}

var usernameContextKey contextKey

type config struct {
	username     string
	password     string
	auditLogPath string
}

type auditRecord struct {
	Auth       string `json:"auth"`
	HTTPMethod string `json:"http_method"`
	MCPMethod  string `json:"mcp_method,omitempty"`
	Path       string `json:"path"`
	Username   string `json:"username,omitempty"`
}

// auditor owns the append-only JSONL boundary shared by concurrent HTTP requests.
type auditor struct {
	file *os.File
	mu   sync.Mutex
}

func newAuditor(path string) (*auditor, error) {
	if path == "" {
		return &auditor{}, nil
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	return &auditor{file: file}, nil
}

func (a *auditor) close() error {
	if a.file == nil {
		return nil
	}
	return a.file.Close()
}

func (a *auditor) write(record auditRecord) error {
	if a.file == nil {
		return nil
	}

	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.file.Write(line); err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	return nil
}

// authHandler is the fixture's trust boundary: only authenticated requests reach MCP.
type authHandler struct {
	next     http.Handler
	username string
	password string
	audit    *auditor
	logger   *log.Logger
}

func (h authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, ok := r.BasicAuth()
	authenticated := ok &&
		subtle.ConstantTimeCompare([]byte(username), []byte(h.username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(h.password)) == 1

	record := auditRecord{
		Auth:       "rejected",
		HTTPMethod: r.Method,
		Path:       r.URL.Path,
	}
	if !authenticated {
		if err := h.audit.write(record); err != nil {
			h.logger.Printf("write audit record: %v", err)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="captain-e2e-mcp"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	record.Auth = "accepted"
	record.Username = username
	mcpMethod, err := readMCPMethod(r)
	if err != nil {
		h.logger.Printf("read MCP method for audit: %v", err)
	} else {
		record.MCPMethod = mcpMethod
	}
	if err := h.audit.write(record); err != nil {
		h.logger.Printf("write audit record: %v", err)
	}

	ctx := context.WithValue(r.Context(), usernameContextKey, username)
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

// readMCPMethod observes the JSON-RPC method without consuming the downstream body.
func readMCPMethod(r *http.Request) (string, error) {
	if r.Method != http.MethodPost || r.Body == nil {
		return "", nil
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var request struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "", err
	}
	return request.Method, nil
}

type echoArguments struct {
	Message   string `json:"message"`
	Count     int    `json:"count"`
	Uppercase bool   `json:"uppercase"`
	Format    string `json:"format"`
}

type echoResult struct {
	Username  string `json:"username"`
	Message   string `json:"message"`
	Count     int    `json:"count"`
	Uppercase bool   `json:"uppercase"`
	Format    string `json:"format"`
	Output    string `json:"output"`
}

// newMCPServer defines the fixture's public tool schema and handlers.
func newMCPServer() *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"captain-e2e-fixture",
		"1.0.0",
		server.WithToolCapabilities(false),
	)
	mcpServer.AddTool(
		mcp.NewTool(
			"echo",
			mcp.WithDescription("Echo a message through deterministic fixture transformations."),
			mcp.WithString(
				"message",
				mcp.Required(),
				mcp.Description("Message to echo."),
			),
			mcp.WithInteger(
				"count",
				mcp.Description("Number of times to repeat the message."),
				mcp.Min(1),
				mcp.Max(10),
			),
			mcp.WithBoolean(
				"uppercase",
				mcp.Description("Uppercase the message before repeating it."),
			),
			mcp.WithString(
				"format",
				mcp.Description("Join repeated messages compactly or on separate lines."),
				mcp.Enum("compact", "lines"),
			),
		),
		echo,
	)
	return mcpServer
}

func echo(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var arguments echoArguments
	if err := request.BindArguments(&arguments); err != nil {
		return mcp.NewToolResultErrorf("invalid arguments: %v", err), nil
	}

	if arguments.Count == 0 {
		arguments.Count = 1
	}
	if arguments.Format == "" {
		arguments.Format = "compact"
	}

	message := arguments.Message
	if arguments.Uppercase {
		message = strings.ToUpper(message)
	}
	messages := make([]string, arguments.Count)
	for i := range messages {
		messages[i] = message
	}

	separator := ""
	if arguments.Format == "lines" {
		separator = "\n"
	}
	result := echoResult{
		Username:  usernameFromContext(ctx),
		Message:   message,
		Count:     arguments.Count,
		Uppercase: arguments.Uppercase,
		Format:    arguments.Format,
		Output:    strings.Join(messages, separator),
	}
	return mcp.NewToolResultJSON(result)
}

func usernameFromContext(ctx context.Context) string {
	username, _ := ctx.Value(usernameContextKey).(string)
	return username
}

func loadConfig() config {
	return config{
		username:     envOrDefault("MCP_FIXTURE_USERNAME", defaultUsername),
		password:     envOrDefault("MCP_FIXTURE_PASSWORD", defaultPassword),
		auditLogPath: os.Getenv("MCP_FIXTURE_AUDIT_LOG"),
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// run owns the process boundary: one loopback listener, one readiness record, and bounded shutdown.
func run() error {
	cfg := loadConfig()
	logger := log.New(os.Stderr, "mcp-fixture: ", log.LstdFlags)

	audit, err := newAuditor(cfg.auditLogPath)
	if err != nil {
		return err
	}
	defer audit.close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	mcpHandler := server.NewStreamableHTTPServer(
		newMCPServer(),
		server.WithStateLess(true),
	)
	mux := http.NewServeMux()
	mux.Handle(endpointPath, authHandler{
		next:     mcpHandler,
		username: cfg.username,
		password: cfg.password,
		audit:    audit,
		logger:   logger,
	})
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	readiness, err := json.Marshal(map[string]string{
		"url": "http://" + listener.Addr().String() + endpointPath,
	})
	if err != nil {
		return fmt.Errorf("marshal readiness: %w", err)
	}
	if _, err := fmt.Fprintln(os.Stdout, string(readiness)); err != nil {
		return fmt.Errorf("write readiness: %w", err)
	}

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve after shutdown: %w", err)
		}
		return nil
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

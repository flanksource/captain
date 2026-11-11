package callertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// CLIRun is the caller-tool server one CLI run connects to. The endpoint's
// credential only ever leaves this process through 0600 files or the agent's
// environment, never its argv.
type CLIRun struct {
	endpoint api.CallerToolEndpoint
	// tools names the served tools; empty for a pre-issued endpoint, whose
	// catalog belongs to its issuer.
	tools   []string
	runtime *Runtime
	files   []string
}

// StartCLI serves cfg's caller tools for one CLI run of req. A pre-issued
// cfg.CallerTools endpoint is used as is; otherwise cfg.Tools are resolved
// against the request's preferences and policy and served by a runtime that
// ctx owns: handlers inherit its values and its end cancels calls in flight.
// It returns nil when the run has no caller tools.
func StartCLI(ctx context.Context, cfg api.Config, req api.Spec) (*CLIRun, error) {
	if cfg.CallerTools != nil {
		if err := cfg.CallerTools.Validate(); err != nil {
			return nil, err
		}
		endpoint := *cfg.CallerTools
		endpoint.Headers = cloneHeaders(cfg.CallerTools.Headers)
		return &CLIRun{endpoint: endpoint}, nil
	}
	if len(cfg.Tools) == 0 {
		return nil, nil
	}
	definitions, err := aitools.ResolveDefinitions(cfg.Tools, aitools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy})
	if err != nil {
		return nil, fmt.Errorf("caller tools: %w", err)
	}
	if len(definitions) == 0 {
		return nil, nil
	}
	approvalTimeout, err := req.Permissions.ParseApprovalTimeout()
	if err != nil {
		return nil, fmt.Errorf("caller tools: %w", err)
	}
	runtime, err := New(Options{
		Context: ctx, Definitions: definitions, OnApproval: cfg.OnApproval,
		SessionID: firstNonEmpty(cfg.CaptainSessionID, req.SessionID, cfg.SessionID), ApprovalTimeout: approvalTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("start caller tools: %w", err)
	}
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return &CLIRun{endpoint: runtime.Endpoint(), tools: names, runtime: runtime}, nil
}

// Endpoint is where the run's caller-tool server listens and the credential a
// client presents to it, for a run that relays it to an agent elsewhere.
func (r *CLIRun) Endpoint() api.CallerToolEndpoint {
	endpoint := r.endpoint
	endpoint.Headers = cloneHeaders(r.endpoint.Headers)
	return endpoint
}

// Close revokes the runtime this run owns and removes the credential files it
// wrote. It is safe on a nil run.
func (r *CLIRun) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.runtime != nil {
		errs = append(errs, r.runtime.Close())
	}
	for _, path := range r.files {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	r.files = nil
	return errors.Join(errs...)
}

// ClaudeAllowedTools are the permission rules that pre-approve the served
// tools in claude. An ask tool is allowed too: the runtime's own approval gate
// asks, so claude must not ask a second time. A pre-issued endpoint's catalog
// is unknown here, so its whole server is allowed.
func (r *CLIRun) ClaudeAllowedTools() []string {
	prefix := "mcp__" + r.endpoint.Name
	if len(r.tools) == 0 {
		return []string{prefix}
	}
	rules := make([]string, 0, len(r.tools))
	for _, name := range r.tools {
		rules = append(rules, prefix+"__"+name)
	}
	return rules
}

// ClaudeMCPConfigFile writes the claude --mcp-config document for the server
// to a 0600 temp file that Close removes.
func (r *CLIRun) ClaudeMCPConfigFile() (string, error) {
	document, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		r.endpoint.Name: map[string]any{"type": "http", "url": r.endpoint.URL, "headers": r.endpoint.Headers},
	}})
	if err != nil {
		return "", fmt.Errorf("encode caller-tool MCP config: %w", err)
	}
	return r.writeSecretFile("captain-caller-tools-*.json", document)
}

// CodexConfig renders the codex `-c` override that registers the server. Its
// headers are read from the environment CodexEnv names, so the credential
// stays out of argv. replaceAll swaps the whole mcp_servers table for this one
// server, which is how mcp.disabled keeps only captain's caller tools.
func (r *CLIRun) CodexConfig(replaceAll bool) (string, error) {
	headers := map[string]string{}
	for _, name := range sortedHeaderNames(r.endpoint.Headers) {
		headers[name] = codexHeaderEnv(name)
	}
	server, err := tomlInlineTable([]tomlField{
		{"url", r.endpoint.URL}, {"env_http_headers", headers}, {"required", true},
		{"enabled", true}, {"default_tools_approval_mode", "approve"},
	})
	if err != nil {
		return "", err
	}
	if replaceAll {
		return "mcp_servers={" + r.endpoint.Name + "=" + server + "}", nil
	}
	return "mcp_servers." + r.endpoint.Name + "=" + server, nil
}

// CodexEnv is the KEY=VALUE environment CodexConfig's env_http_headers read.
func (r *CLIRun) CodexEnv() []string {
	names := sortedHeaderNames(r.endpoint.Headers)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, codexHeaderEnv(name)+"="+r.endpoint.Headers[name])
	}
	return env
}

// CodexEnvFiles writes each CodexEnv value to its own 0600 temp file, keyed by
// variable name, for a launcher that must not show the value (a pasted shell
// command reads it back with $(cat file)). Close removes them.
func (r *CLIRun) CodexEnvFiles() (map[string]string, error) {
	files := map[string]string{}
	for _, name := range sortedHeaderNames(r.endpoint.Headers) {
		path, err := r.writeSecretFile("captain-caller-tools-env-*", []byte(r.endpoint.Headers[name]))
		if err != nil {
			return nil, err
		}
		files[codexHeaderEnv(name)] = path
	}
	return files, nil
}

func (r *CLIRun) writeSecretFile(pattern string, content []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create caller-tool credential file: %w", err)
	}
	path := f.Name()
	r.files = append(r.files, path)
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write caller-tool credential file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close caller-tool credential file: %w", err)
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func codexHeaderEnv(header string) string {
	return "CAPTAIN_CALLER_TOOLS_" + strings.ToUpper(strings.Map(func(value rune) rune {
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
			return value
		}
		return '_'
	}, header))
}

func sortedHeaderNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type tomlField struct {
	key   string
	value any
}

// tomlInlineTable renders fields as a TOML inline table. JSON strings and
// booleans are valid TOML values; a string map becomes a nested inline table
// with quoted keys.
func tomlInlineTable(fields []tomlField) (string, error) {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		value, err := tomlValue(field.value)
		if err != nil {
			return "", fmt.Errorf("encode caller-tool codex config %s: %w", field.key, err)
		}
		parts = append(parts, field.key+"="+value)
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func tomlValue(value any) (string, error) {
	table, ok := value.(map[string]string)
	if !ok {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	parts := make([]string, 0, len(table))
	for _, key := range sortedHeaderNames(table) {
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", err
		}
		encodedValue, err := json.Marshal(table[key])
		if err != nil {
			return "", err
		}
		parts = append(parts, string(encodedKey)+"="+string(encodedValue))
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

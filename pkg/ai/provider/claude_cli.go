package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

type ClaudeCLI struct {
	model string
}

func NewClaudeCLI(model string) *ClaudeCLI {
	if model == "" {
		model = "claude-code-sonnet"
	}
	return &ClaudeCLI{model: model}
}

func (c *ClaudeCLI) GetModel() string       { return c.model }
func (c *ClaudeCLI) GetBackend() ai.Backend { return ai.BackendClaudeCLI }

type claudeCLIResponse struct {
	Result       string          `json:"result,omitempty"`
	Structured   any             `json:"structured,omitempty"`
	Usage        *claudeCLIUsage `json:"usage,omitempty"`
	Error        string          `json:"error,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	CostUSD      float64         `json:"cost_usd,omitempty"`
	DurationMS   float64         `json:"duration_ms,omitempty"`
	DurationAPI  float64         `json:"duration_api_ms,omitempty"`
	NumTurns     int             `json:"num_turns,omitempty"`
	TotalCostUSD float64         `json:"total_cost,omitempty"`
}

type claudeCLIUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
}

func (c *ClaudeCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	timeout := 60 * time.Second
	if req.StructuredOutput != nil {
		timeout = 120 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if ctxTimeout := time.Until(deadline); ctxTimeout < timeout {
			timeout = ctxTimeout
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	actualModel := MapClaudeCodeModel(c.model)

	args := []string{
		"-p",
		"--output-format", "json",
		"--model", actualModel,
		"--max-turns", "1",
		"--no-session-persistence",
	}

	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}

	if req.StructuredOutput != nil {
		schema, err := GenerateJSONSchema(req.StructuredOutput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		schemaBytes, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal schema: %w", err)
		}
		args = append(args, "--json-schema", string(schemaBytes))
	}

	args = append(args, req.Prompt)

	stdoutData, stderrData, err := runClaudeCLI(ctx, args)
	if err != nil {
		return nil, err
	}

	var cliResp claudeCLIResponse
	stdoutStr := string(stdoutData)
	if idx := strings.Index(stdoutStr, "{"); idx >= 0 {
		stdoutStr = stdoutStr[idx:]
	}

	if err := json.Unmarshal([]byte(stdoutStr), &cliResp); err != nil {
		return nil, fmt.Errorf("failed to parse CLI response: %w (output: %s)", err, stdoutStr)
	}
	_ = stderrData

	if cliResp.IsError || cliResp.Error != "" {
		msg := cliResp.Error
		if msg == "" {
			msg = cliResp.Result
		}
		return nil, fmt.Errorf("CLI returned error: %s", msg)
	}

	var structuredData any
	if req.StructuredOutput != nil {
		if cliResp.Structured == nil {
			return nil, fmt.Errorf("%w: no structured data in response", ai.ErrSchemaValidation)
		}
		structBytes, err := json.Marshal(cliResp.Structured)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal structured response: %w", err)
		}
		if err := UnmarshalWithCleanup(string(structBytes), req.StructuredOutput); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
	}

	usage := ai.Usage{}
	if cliResp.Usage != nil {
		usage = ai.Usage{
			InputTokens:      cliResp.Usage.InputTokens,
			OutputTokens:     cliResp.Usage.OutputTokens,
			ReasoningTokens:  cliResp.Usage.ReasoningTokens,
			CacheReadTokens:  cliResp.Usage.CacheReadTokens,
			CacheWriteTokens: cliResp.Usage.CacheWriteTokens,
		}
	}

	text := cliResp.Result
	if req.StructuredOutput != nil {
		text = ""
	}

	return &ai.Response{
		Text:           text,
		StructuredData: structuredData,
		Model:          c.model,
		Backend:        ai.BackendClaudeCLI,
		Usage:          usage,
		Duration:       time.Since(start),
		Raw:            cliResp,
	}, nil
}

func runClaudeCLI(ctx context.Context, args []string) (stdout []byte, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = clearNestingEnv(os.Environ())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, "", fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, "", fmt.Errorf("failed to start claude: %w", err)
	}

	stdoutCh := make(chan []byte, 1)
	stderrCh := make(chan string, 1)
	errCh := make(chan error, 2)

	go func() {
		data, err := io.ReadAll(stdoutPipe)
		if err != nil {
			errCh <- fmt.Errorf("failed to read stdout: %w", err)
			return
		}
		stdoutCh <- data
	}()

	go func() {
		data, err := io.ReadAll(stderrPipe)
		if err != nil {
			errCh <- fmt.Errorf("failed to read stderr: %w", err)
			return
		}
		stderrCh <- string(data)
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var stdoutData []byte
	var stderrData string
	var waitErr error

	for range 3 {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, "", fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
		case e := <-errCh:
			return nil, "", e
		case data := <-stdoutCh:
			stdoutData = data
		case data := <-stderrCh:
			stderrData = data
		case e := <-waitCh:
			waitErr = e
		}
	}

	if waitErr != nil {
		return nil, stderrData, HandleExitError(GetExitCode(waitErr), ParseStderr(stderrData))
	}

	return stdoutData, stderrData, nil
}

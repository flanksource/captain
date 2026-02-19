package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

type CodexCLI struct {
	model string
}

func NewCodexCLI(model string) *CodexCLI {
	if model == "" {
		model = "codex"
	}
	return &CodexCLI{model: model}
}

func (c *CodexCLI) GetModel() string      { return c.model }
func (c *CodexCLI) GetBackend() ai.Backend { return ai.BackendCodexCLI }

type codexCLIRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
}

type codexCLIResponse struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func (c *CodexCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if ctxTimeout := time.Until(deadline); ctxTimeout < timeout {
			timeout = ctxTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cliReq := codexCLIRequest{
		Prompt: req.Prompt,
		Model:  c.model,
	}
	reqBytes, err := json.Marshal(cliReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	stdoutData, _, err := runCLI(ctx, "codex", reqBytes)
	if err != nil {
		return nil, err
	}

	var cliResp codexCLIResponse
	stdoutStr := string(stdoutData)
	if idx := strings.Index(stdoutStr, "{"); idx >= 0 {
		stdoutStr = stdoutStr[idx:]
	}

	if err := json.Unmarshal([]byte(stdoutStr), &cliResp); err != nil {
		return nil, fmt.Errorf("failed to parse codex response: %w (output: %s)", err, stdoutStr)
	}

	if cliResp.Error != "" {
		return nil, fmt.Errorf("codex CLI error: %s", cliResp.Error)
	}

	usage := ai.Usage{}
	if cliResp.Usage != nil {
		usage.InputTokens = cliResp.Usage.InputTokens
		usage.OutputTokens = cliResp.Usage.OutputTokens
	}

	return &ai.Response{
		Text:     cliResp.Text,
		Model:    c.model,
		Backend:  ai.BackendCodexCLI,
		Usage:    usage,
		Duration: time.Since(start),
		Raw:      cliResp,
	}, nil
}

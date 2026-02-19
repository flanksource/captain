package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

type GeminiCLI struct {
	model string
}

func NewGeminiCLI(model string) *GeminiCLI {
	if model == "" {
		model = "gemini-cli-pro"
	}
	return &GeminiCLI{model: model}
}

func (g *GeminiCLI) GetModel() string      { return g.model }
func (g *GeminiCLI) GetBackend() ai.Backend { return ai.BackendGeminiCLI }

type geminiCLIResponse struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func (g *GeminiCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if ctxTimeout := time.Until(deadline); ctxTimeout < timeout {
			timeout = ctxTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cliReq := map[string]string{
		"prompt": req.Prompt,
		"model":  g.model,
	}
	reqBytes, err := json.Marshal(cliReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	stdoutData, _, err := runCLI(ctx, "gemini", reqBytes)
	if err != nil {
		return nil, err
	}

	var cliResp geminiCLIResponse
	stdoutStr := string(stdoutData)
	if idx := strings.Index(stdoutStr, "{"); idx >= 0 {
		stdoutStr = stdoutStr[idx:]
	}

	if err := json.Unmarshal([]byte(stdoutStr), &cliResp); err != nil {
		return nil, fmt.Errorf("failed to parse gemini response: %w (output: %s)", err, stdoutStr)
	}

	if cliResp.Error != "" {
		return nil, fmt.Errorf("gemini CLI error: %s", cliResp.Error)
	}

	usage := ai.Usage{}
	if cliResp.Usage != nil {
		usage.InputTokens = cliResp.Usage.InputTokens
		usage.OutputTokens = cliResp.Usage.OutputTokens
	}

	return &ai.Response{
		Text:     cliResp.Text,
		Model:    g.model,
		Backend:  ai.BackendGeminiCLI,
		Usage:    usage,
		Duration: time.Since(start),
		Raw:      cliResp,
	}, nil
}

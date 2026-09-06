package verify

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// judgeVerdict is the structured output an LLM judge returns.
type judgeVerdict struct {
	OK       bool   `json:"ok"`
	Reason   string `json:"reason"`
	Feedback string `json:"feedback"`
}

// LLMJudgeVerifier asks an LLM (via a .prompt template with an {ok,reason,
// feedback} output schema) to judge the result of an agent turn. Use it when a
// pass/fail signal is subjective rather than a command exit code.
type LLMJudgeVerifier struct {
	Provider ai.Provider
	Prompt   *prompt.Template
	// Data builds the template input from the run's cwd and changed files
	// (e.g. embed the diff). When nil, {"cwd","changed"} are passed.
	Data func(cwd string, changed []string) map[string]any
}

func (j *LLMJudgeVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	if j.Provider == nil || j.Prompt == nil {
		return Verdict{}, fmt.Errorf("LLMJudgeVerifier: Provider and Prompt are required")
	}
	data := map[string]any{"cwd": cwd, "changed": changed}
	if j.Data != nil {
		data = j.Data(cwd, changed)
	}

	out := &judgeVerdict{}
	req, _, err := j.Prompt.Render(prompt.RenderOptions{Data: data, Output: out})
	if err != nil {
		return Verdict{}, err
	}
	started := time.Now()
	if _, err := j.Provider.Execute(ctx, req); err != nil {
		return Verdict{}, err
	}
	// One leaf per judgement; the Plugin names the report after the hook
	// ("judge:<prompt path>") since the template does not carry its path.
	report := api.NewNodeReport(api.VerifyKindPrompt, "", api.VerifyNode{
		// The framework is the report's own kind, not a second name for it: a
		// renderer grouping a mixed tree by framework would otherwise show a
		// judge's node and a judge report as two different families.
		Name: "llm judge", Framework: api.VerifyKindPrompt, Passed: out.OK, Failed: !out.OK,
		Message: out.Reason, Duration: time.Since(started),
	})
	report.Feedback = out.Feedback
	return Verdict{OK: out.OK, Reason: out.Reason, Feedback: out.Feedback, Report: &report}, nil
}

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	clickyapi "github.com/flanksource/clicky/api"
)

func TestExecuteSyncRunMultiModelsParallel(t *testing.T) {
	withTestCaptainDB(t)

	old := executePromptRequestFunc
	t.Cleanup(func() { executePromptRequestFunc = old })

	started := make(chan struct{})
	var calls atomic.Int32
	executePromptRequestFunc = func(ctx context.Context, req ai.Request, cfg ai.Config, _ time.Duration, noStream bool) (any, error) {
		if noStream {
			t.Errorf("multi-model run should preserve streaming unless --no-stream is set")
		}
		if calls.Add(1) == 2 {
			close(started)
		}
		select {
		case <-started:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
			return nil, errors.New("executor was not called concurrently")
		}
		return AIPromptResult{
			Text:             cfg.Model.Name,
			StructuredOutput: map[string]any{"model": cfg.Model.Name},
			Model:            cfg.Model.Name,
			Provider:         cfg.Model.Provider.Name,
			Mode:             string(cfg.Model.Mode),
			Dir:              req.Cwd(),
			SessionID:        "session-" + cfg.Model.Name,
			HistoryFile:      filepath.Join(req.Cwd(), ".history", cfg.Model.Name+".jsonl"),
			InputTokens:      1,
			Output:           2,
			CostUSD:          0.01,
			Duration:         "1ms",
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rendered := testRenderedPrompt(api.Model{Name: "claude-sonnet-5", Mode: api.ModeAPI})
	rendered.Input.SetCwd(t.TempDir())
	opts := AIPromptOptions{MultiModels: []string{"cli:sonnet-5,cmux:opus"}}
	rendered, err := testRenderedVariants(rendered.Input, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executeSyncRun(ctx, rendered, opts)
	if err != nil {
		t.Fatalf("executeSyncRun: %v", err)
	}
	if got.Status != "completed" || got.Total != 2 || got.Succeeded != 2 || got.Failed != 0 {
		t.Fatalf("batch status = %s total=%d succeeded=%d failed=%d", got.Status, got.Total, got.Succeeded, got.Failed)
	}
	want := []struct {
		provider string
		mode     api.RuntimeMode
		model    string
	}{
		{api.Anthropic.Name, api.ModeCLI, "claude-sonnet-5"},
		{api.Anthropic.Name, api.ModeCmux, "claude-opus-5"},
	}
	for i, w := range want {
		if got.Runs[i].Provider != w.provider || got.Runs[i].Mode != string(w.mode) || got.Runs[i].Model != w.model {
			t.Fatalf("run[%d] = %s %s/%s, want %s %s/%s", i, got.Runs[i].Provider, got.Runs[i].Mode, got.Runs[i].Model, w.provider, w.mode, w.model)
		}
	}
	if got.InputTokens != 2 || got.OutputTokens != 4 {
		t.Fatalf("aggregate tokens = %d/%d, want 2/4", got.InputTokens, got.OutputTokens)
	}
	if got.Runs[0].Dir == "" || !strings.Contains(got.Runs[0].HistoryFile, ".history") {
		t.Fatalf("run metadata missing dir/history: %+v", got.Runs[0])
	}
	if got.Runs[0].CostUSD != 0.01 || got.Runs[0].InputTokens != 1 || got.Runs[0].OutputTokens != 2 {
		t.Fatalf("run usage/cost missing: %+v", got.Runs[0])
	}
	if got.Runs[0].StructuredOutput["model"] != got.Runs[0].Model {
		t.Fatalf("run structured output missing: %+v", got.Runs[0])
	}
}

func TestExecuteSyncRunMultiModelsHonorsNoStream(t *testing.T) {
	old := executePromptRequestFunc
	t.Cleanup(func() { executePromptRequestFunc = old })

	executePromptRequestFunc = func(_ context.Context, _ ai.Request, cfg ai.Config, _ time.Duration, noStream bool) (any, error) {
		if !noStream {
			t.Errorf("multi-model run should pass explicit --no-stream through")
		}
		return AIPromptResult{
			Text:     "ok",
			Model:    cfg.Model.Name,
			Provider: cfg.Model.Provider.Name,
			Mode:     string(cfg.Model.Mode),
			Duration: "1ms",
		}, nil
	}

	rendered := testRenderedPrompt(api.Model{Name: "claude-sonnet-5", Mode: api.ModeAPI})
	opts := AIPromptOptions{MultiModels: []string{"cli:sonnet-5"}, NoStream: true}
	rendered, err := testRenderedVariants(rendered.Input, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executeSyncRun(context.Background(), rendered, opts)
	if err != nil {
		t.Fatalf("executeSyncRun: %v", err)
	}
	if got.Status != "completed" || got.Succeeded != 1 {
		t.Fatalf("batch status = %+v", got)
	}
}

func TestExecuteSyncRunMultiModelsPartialFailure(t *testing.T) {
	withTestCaptainDB(t)

	old := executePromptRequestFunc
	t.Cleanup(func() { executePromptRequestFunc = old })

	executePromptRequestFunc = func(_ context.Context, _ ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
		if cfg.Model.Mode == api.ModeCmux {
			return nil, errors.New("cmux unavailable")
		}
		return AIPromptResult{
			Text:        "ok",
			Model:       cfg.Model.Name,
			Mode:        string(cfg.Model.Mode),
			InputTokens: 3,
			Output:      4,
			Duration:    "2ms",
		}, nil
	}

	rendered := testRenderedPrompt(api.Model{Name: "gpt-5.5", Mode: api.ModeAPI})
	opts := AIPromptOptions{MultiModels: []string{"api:gpt-5.5,cmux:gpt-5.5"}}
	rendered, err := testRenderedVariants(rendered.Input, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executeSyncRun(context.Background(), rendered, opts)
	if err != nil {
		t.Fatalf("executeSyncRun: %v", err)
	}
	if got.Status != "partial" || got.Succeeded != 1 || got.Failed != 1 {
		t.Fatalf("batch status = %s succeeded=%d failed=%d", got.Status, got.Succeeded, got.Failed)
	}
	if got.Runs[1].Status != "failed" || !strings.Contains(got.Runs[1].Error, "cmux unavailable") {
		t.Fatalf("failed run = %+v", got.Runs[1])
	}
}

func TestExecuteSyncRunMultiModelsRejectsResume(t *testing.T) {
	rendered := testRenderedPrompt(api.Model{Name: "gpt-5.5", Mode: api.ModeAPI})
	rendered.Input.SessionID = "session-1"
	opts := AIPromptOptions{MultiModels: []string{"api:gpt-5.5,cmux:gpt-5.5"}}
	rendered, err := testRenderedVariants(rendered.Input, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executeSyncRun(context.Background(), rendered, opts)
	if err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("error = %v, want --resume rejection", err)
	}
}

func TestVariantModelUsesSelectorEffort(t *testing.T) {
	selector := api.Model{Name: "gpt-5.6-terra", Mode: api.ModeCmux, Effort: api.EffortUltra}
	got := renderVariant(PromptRenderResult{}, testRuntimeVariant(selector)).Input.Model
	if got.Name != selector.Name || got.Provider != selector.Provider || got.Mode != selector.Mode || got.Effort != api.EffortUltra {
		t.Fatalf("variant = %+v, want selector model/runtime/effort", got)
	}
}

func TestPromptRunResultPrettyRendersRunsAsColumns(t *testing.T) {
	result := PromptRunResult{
		Status:    "partial",
		Total:     2,
		Succeeded: 1,
		Failed:    1,
		Runs: []PromptRunItem{
			{
				Selector:     "api:sonnet-5",
				Status:       "completed",
				Provider:     "anthropic",
				Mode:         "api",
				Model:        "claude-sonnet-5",
				Dir:          "/repo",
				SessionID:    "0123456789abcdef",
				HistoryFile:  "/repo/.claude/session.jsonl",
				InputTokens:  8,
				OutputTokens: 14,
				CostUSD:      0.0002,
				Duration:     "1s",
				Text:         "api ok",
			},
			{
				Selector: "cmux:opus",
				Status:   "failed",
				Provider: "anthropic",
				Mode:     "cmux",
				Model:    "claude-opus-4-8",
				Duration: "2s",
				Error:    "cmux unavailable",
			},
		},
	}

	pretty := result.Pretty()
	table := firstPrettyTable(t, pretty)
	wantFields := []string{"metric", "run1", "run2"}
	if strings.Join(table.FieldNames, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("field names = %v, want %v", table.FieldNames, wantFields)
	}
	if len(table.Headers) != 3 {
		t.Fatalf("headers = %d, want 3", len(table.Headers))
	}
	wantHeaders := []string{"Metric", "api:sonnet-5", "cmux:opus"}
	for i, want := range wantHeaders {
		if got := table.Headers[i].String(); got != want {
			t.Fatalf("header[%d] = %q, want %q", i, got, want)
		}
	}

	tests := []struct {
		metric string
		run1   string
		run2   string
	}{
		{"Status", "completed", "failed"},
		{"Provider", "anthropic", "anthropic"},
		{"Mode", "api", "cmux"},
		{"Model", "claude-sonnet-5", "claude-opus-4-8"},
		{"Error", "", "cmux unavailable"},
		{"Duration", "1s", "2s"},
		{"Tokens", "8/14", ""},
		{"Cost", "$0.0002", ""},
		{"Session", "0123456789ab", ""},
		{"History", "/repo/.claude/session.jsonl", ""},
		{"Dir", "/repo", ""},
	}
	for _, tt := range tests {
		row := tableRowByMetric(t, table, tt.metric)
		if row["run1"].String() != tt.run1 || row["run2"].String() != tt.run2 {
			t.Fatalf("%s row = %q / %q, want %q / %q", tt.metric, row["run1"].String(), row["run2"].String(), tt.run1, tt.run2)
		}
	}
	if output := pretty.String(); !strings.Contains(output, "Response — api:sonnet-5\napi ok") {
		t.Fatalf("response block missing after metadata table: %q", output)
	}
}

func TestHistoryFileForRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	got := historyFileForRun(api.Anthropic, api.ModeCLI, "claude-session", cwd)
	if want := filepath.Join(home, ".claude", "projects"); !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "claude-session.jsonl") {
		t.Fatalf("claude history path = %q, want under %q", got, want)
	}

	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "08")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	codexFile := filepath.Join(codexDir, "codex-session.jsonl")
	if err := os.WriteFile(codexFile, []byte(`{"type":"session_meta","payload":{"id":"codex-session","cwd":"`+cwd+`"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write codex session: %v", err)
	}
	if got := historyFileForRun(api.OpenAI, api.ModeAgent, "codex-session", cwd); got != codexFile {
		t.Fatalf("codex history path = %q, want %q", got, codexFile)
	}
}

func firstPrettyTable(t *testing.T, text clickyapi.Text) clickyapi.TextTable {
	t.Helper()
	for _, child := range text.Children {
		if table, ok := child.(clickyapi.TextTable); ok {
			return table
		}
	}
	t.Fatalf("no table child in %#v", text.Children)
	return clickyapi.TextTable{}
}

func tableRowByMetric(t *testing.T, table clickyapi.TextTable, metric string) clickyapi.TableRow {
	t.Helper()
	for _, row := range table.Rows {
		if row["metric"].String() == metric {
			return row
		}
	}
	t.Fatalf("no %q metric row in %#v", metric, table.Rows)
	return nil
}

func testRenderedPrompt(model api.Model) PromptRenderResult {
	req := ai.Request{
		Model:  model,
		Prompt: api.Prompt{User: "hello"},
		Budget: api.Budget{Timeout: "1s"},
	}
	cfg := ai.Config{Model: model}
	provider := ""
	if model.Provider != nil {
		provider = model.Provider.Name
	}
	return PromptRenderResult{
		Name:     "test",
		Model:    model.Name,
		Provider: provider,
		Mode:     string(model.Mode),
		Input:    req,
		Config:   cfg,
	}
}

func testRuntimeVariant(model api.Model) AIRuntimeResolved {
	spec := api.Spec{Model: model}
	return AIRuntimeResolved{Request: spec, Config: ai.Config{Model: model}, Resolution: api.ResolvedSpec{Spec: spec}}
}

func testRenderedVariants(spec api.Spec, options AIPromptOptions) (PromptRenderResult, error) {
	return completePromptRender(promptRenderInput{
		Record: promptRecord{Rel: "test.prompt"}, Content: "hello", Options: options,
		Layers:   []api.SpecLayer{api.PromptSpecLayer("test.prompt", spec)},
		Runtimes: fallbackModelsFromFlags(options.MultiModels),
	})
}

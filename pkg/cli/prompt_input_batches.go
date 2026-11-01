package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
)

// inputItem is one input file of a run: a positional file, or an --attach
// entry, which a batch also splits and which stays an attachment.
type inputItem struct {
	Path   string
	Attach bool
}

// InputBatchesError reports the batched runs that failed. It renders as the
// whole result, so every run's row is shown while the command still fails.
type InputBatchesError struct {
	Result PromptRunResult `json:"result"`
}

func (e *InputBatchesError) Error() string {
	return fmt.Sprintf("%d of %d batched runs failed", e.Result.Failed, e.Result.Total)
}

func (e *InputBatchesError) Pretty() clickyapi.Text { return e.Result.Pretty() }

// runInputFiles runs a prompt over its input files: one run with every file,
// or, when the spec sets batch, one run per batch.size files.
func runInputFiles(ctx context.Context, invocation PromptInvocation) (PromptRunResult, error) {
	if err := validateInputFiles(invocation.Options.InputFiles); err != nil {
		return PromptRunResult{}, err
	}
	stdin, err := invocationStdin(ctx, invocation)
	if err != nil {
		return PromptRunResult{}, err
	}
	plan, err := planInputBatch(ctx, invocation, stdin)
	if err != nil {
		return PromptRunResult{}, err
	}
	if plan.Input.Batch == nil {
		invocation.Stdin = strings.NewReader(stdin)
		rendered, err := RenderPrompt(ctx, invocation)
		if err != nil {
			return PromptRunResult{}, err
		}
		return runRendered(ctx, rendered, invocation.Options)
	}
	items, err := inputItems(invocation.Options)
	if err != nil {
		return PromptRunResult{}, err
	}
	if _, err := ParsePromptVariables(chunkOptions(invocation.Options, items), invocation.Options.Vars, stdin, false); err != nil {
		return PromptRunResult{}, err
	}
	chunks := slices.Collect(slices.Chunk(items, plan.Input.Batch.Size))
	return executeInputBatches(ctx, inputBatches{Invocation: invocation, Stdin: stdin, Plan: plan, Chunks: chunks})
}

// planInputBatch renders the prompt without its input hooks to read the
// resolved batch, refusing a batch the run cannot split.
func planInputBatch(ctx context.Context, invocation PromptInvocation, stdin string) (PromptRenderResult, error) {
	invocation.Options.deferInputHooks = true
	invocation.Stdin = strings.NewReader(stdin)
	plan, err := RenderPrompt(ctx, invocation)
	if err != nil || plan.Input.Batch == nil {
		return plan, err
	}
	if len(plan.Runtimes) > 0 || len(invocation.Options.MultiModels) > 0 {
		return PromptRenderResult{}, errors.New("batch cannot be combined with a multi-runtime run (runtimes or --multi-models): run each runtime's batch separately")
	}
	return plan, nil
}

func invocationStdin(ctx context.Context, invocation PromptInvocation) (string, error) {
	if invocation.Stdin == nil {
		return readStdinIfCLI(ctx)
	}
	data, err := io.ReadAll(invocation.Stdin)
	return string(data), err
}

// validateInputFiles requires every positional input file to be an existing
// regular file.
func validateInputFiles(files []string) error {
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("input file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("input file %q is not a regular file", path)
		}
	}
	return nil
}

// inputItems are the files a batch splits: the positional files, then each
// --attach entry.
func inputItems(opts AIPromptOptions) ([]inputItem, error) {
	items := make([]inputItem, 0, len(opts.InputFiles))
	for _, path := range opts.InputFiles {
		items = append(items, inputItem{Path: path})
	}
	refs, err := attachmentRefsFromFlags(opts.Attach)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		items = append(items, inputItem{Path: firstNonEmpty(ref.Path, ref.URL, ref.ID), Attach: true})
	}
	return items, nil
}

// chunkOptions are opts for one batch: its files bound as vars, and only its
// --attach entries attached.
func chunkOptions(opts AIPromptOptions, chunk []inputItem) AIPromptOptions {
	opts.InputFiles, opts.Attach = inputPaths(chunk), nil
	for _, item := range chunk {
		if item.Attach {
			opts.Attach = append(opts.Attach, csvField(item.Path))
		}
	}
	return opts
}

func inputPaths(chunk []inputItem) []string {
	paths := make([]string, len(chunk))
	for i, item := range chunk {
		paths[i] = item.Path
	}
	return paths
}

// csvField quotes value as one RFC 4180 field, the form --attach values parse.
func csvField(value string) string {
	var out strings.Builder
	writer := csv.NewWriter(&out)
	if err := writer.Write([]string{value}); err != nil {
		panic(fmt.Sprintf("encode attachment %q: %v", value, err))
	}
	writer.Flush()
	return strings.TrimSuffix(out.String(), "\n")
}

type inputBatches struct {
	Invocation PromptInvocation
	Stdin      string
	Plan       PromptRenderResult
	Chunks     [][]inputItem
}

// executeInputBatches runs each batch as its own recorded run, up to
// batch.concurrency at a time. A failed batch fails the command once every
// batch has run.
func executeInputBatches(ctx context.Context, batches inputBatches) (PromptRunResult, error) {
	start := time.Now()
	printBlock, err := inputBlockPrinter(clicky.Flags.FormatOptions)
	if err != nil {
		return PromptRunResult{}, err
	}
	planned := make([]PromptRunItem, len(batches.Chunks))
	for i, chunk := range batches.Chunks {
		planned[i].Inputs = inputPaths(chunk)
	}
	base := commonInputDir(planned)
	group := task.StartGroup[PromptRunItem]("prompt "+batches.Plan.Name,
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabels(batches.Plan, "batch")),
		task.WithConcurrency(batches.Plan.Input.Batch.Workers()),
	)
	tasks := make([]task.TypedTask[PromptRunItem], len(batches.Chunks))
	for i, chunk := range batches.Chunks {
		tasks[i] = group.Add(inputChunkName(chunk, base), func(_ flanksourceContext.Context, t *task.Task) (PromptRunItem, error) {
			item, err := runInputChunk(ai.ContextWithLogger(t.Context(), t), t, batches, chunk)
			item = settleInputRun(item, chunk, err)
			if printBlock != nil {
				printBlock(item, base)
			}
			return item, err
		}, task.WithContext(ctx))
	}
	runs := make([]PromptRunItem, len(tasks))
	for i, typed := range tasks {
		item, err := typed.GetResult()
		runs[i] = settleInputRun(item, batches.Chunks[i], err)
	}
	result := inputBatchesResult(runs, time.Since(start))
	if result.Failed > 0 {
		return result, &InputBatchesError{Result: result}
	}
	return result, nil
}

// inputChunkName labels a batch's task with its input files relative to the
// batch's common directory, the names its printed block uses.
func inputChunkName(chunk []inputItem, base string) string {
	names := make([]string, len(chunk))
	for i, item := range chunk {
		names[i] = relativeInput(item.Path, base)
	}
	return strings.Join(names, ",")
}

// settleInputRun is a finished batch's item: failed with err when the run did
// not record its own error, and naming its chunk's input files.
func settleInputRun(item PromptRunItem, chunk []inputItem, err error) PromptRunItem {
	if err != nil && item.Error == "" {
		item.Status, item.Error = "failed", err.Error()
	}
	item.Inputs = inputPaths(chunk)
	return item
}

// runInputChunk renders the prompt for one batch — its input hooks may skip
// it — and runs it.
func runInputChunk(ctx context.Context, t *task.Task, batches inputBatches, chunk []inputItem) (PromptRunItem, error) {
	invocation := batches.Invocation
	invocation.Options = chunkOptions(invocation.Options, chunk)
	invocation.Stdin = strings.NewReader(batches.Stdin)
	item := PromptRunItem{Inputs: inputPaths(chunk)}
	rendered, err := RenderPrompt(ctx, invocation)
	if err == nil && rendered.Skip == nil && rendered.ValidationError != "" {
		err = errors.New(rendered.ValidationError)
	}
	if err != nil {
		item.Status, item.Error = "failed", err.Error()
		return item, err
	}
	if rendered.Skip != nil {
		item.Status, item.SkipReason = "skipped", rendered.Skip.Reason
		return item, nil
	}
	logRuntimeWarnings(rendered.Resolution.Warnings)
	result, err := executeSyncRunSingleDirect(ctx, t, rendered, invocation.Options, nil)
	item = inputRunItem(item, rendered, result)
	if err != nil {
		item.Status, item.Error = "failed", err.Error()
	}
	return item, err
}

func inputRunItem(item PromptRunItem, rendered PromptRenderResult, result PromptRunResult) PromptRunItem {
	item.RunID, item.Status = result.RunID, result.Status
	item.Model = firstNonEmpty(result.Model, rendered.Model)
	item.Provider = firstNonEmpty(result.Provider, rendered.Provider)
	item.Mode = firstNonEmpty(result.Mode, rendered.Mode)
	item.Text, item.StructuredOutput = result.Text, result.StructuredOutput
	item.SessionID, item.Dir, item.HistoryFile = result.SessionID, result.Dir, result.HistoryFile
	item.InputTokens, item.OutputTokens, item.CostUSD, item.Duration = result.InputTokens, result.OutputTokens, result.CostUSD, result.Duration
	return item
}

// inputBatchesResult totals the batched runs: tokens and cost over every run
// that executed, status partial when some but not all failed.
func inputBatchesResult(runs []PromptRunItem, elapsed time.Duration) PromptRunResult {
	result := PromptRunResult{Total: len(runs), Runs: runs, Duration: elapsed.Round(time.Millisecond).String()}
	for _, run := range runs {
		switch {
		case run.Status == "skipped":
			result.Skipped++
			continue
		case run.Error != "" || run.Status == "failed":
			result.Failed++
		default:
			result.Succeeded++
		}
		result.InputTokens += run.InputTokens
		result.OutputTokens += run.OutputTokens
		result.CostUSD += run.CostUSD
	}
	switch result.Failed {
	case 0:
		result.Status = "completed"
	case result.Total:
		result.Status = "failed"
	default:
		result.Status = "partial"
	}
	return result
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

type promptDirsContextKey struct{}

var registerPromptEntityOnce sync.Once

type PromptListOptions struct {
	Query  string `flag:"query" help:"Search prompt name, description, model, or path"`
	Source string `flag:"source" help:"Filter by source: all|embedded|local|<source-id>"`
}

type PromptVariable struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type PromptSummary struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	SourceKind  string           `json:"sourceKind"`
	SourceID    string           `json:"sourceId"`
	Source      string           `json:"source"`
	Path        string           `json:"path"`
	RelPath     string           `json:"relPath"`
	Writable    bool             `json:"writable"`
	Model       string           `json:"model,omitempty"`
	Mode        string           `json:"mode,omitempty"`
	Runtimes    []api.Model      `json:"runtimes,omitempty"`
	Variables   []PromptVariable `json:"variables,omitempty"`
	ParseError  string           `json:"parseError,omitempty"`
	UpdatedAt   string           `json:"updatedAt,omitempty"`
	// Version identifies the exact content this summary describes; writes echo
	// it back as baseVersion so a concurrent edit is reported, not clobbered.
	Version string `json:"version,omitempty"`
}

func (p PromptSummary) GetID() string   { return p.ID }
func (p PromptSummary) GetName() string { return p.Name }

func (p PromptSummary) Columns() []clickyapi.ColumnDef {
	return []clickyapi.ColumnDef{
		clickyapi.Column("name").Label("Name").Build(),
		clickyapi.Column("source").Label("Source").Build(),
		clickyapi.Column("relPath").Label("Path").MaxWidth(54).Build(),
		clickyapi.Column("model").Label("Model").Build(),
		clickyapi.Column("description").Label("Description").MaxWidth(80).Build(),
	}
}

func (p PromptSummary) Row() map[string]any {
	return map[string]any{
		"name":        p.Name,
		"source":      p.Source,
		"relPath":     p.RelPath,
		"model":       p.Model,
		"description": p.Description,
	}
}

type PromptDetail struct {
	PromptSummary
	Content      string              `json:"content"`
	InputSchema  map[string]any      `json:"inputSchema,omitempty"`
	InputDefault map[string]any      `json:"inputDefault,omitempty"`
	OutputSchema map[string]any      `json:"outputSchema,omitempty"`
	Metadata     map[string]any      `json:"metadata,omitempty"`
	Run          PromptRenderRequest `json:"run"`
}

type PromptWriteRequest struct {
	Target  string `json:"target"`
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
	Content string `json:"content"`
	// BaseVersion is the PromptSummary.Version the editor loaded; an update whose
	// base no longer matches the file is refused as a conflict.
	BaseVersion string `json:"baseVersion,omitempty"`
}

type PromptRenderRequest struct {
	Variables map[string]any `json:"variables,omitempty"`
	Spec      *api.Spec      `json:"spec,omitempty"`
	Runtimes  []api.Model    `json:"runtimes,omitempty"`
	Chat      bool           `json:"chat,omitempty"`
	// Content, when set, is an unsaved draft rendered in place of the saved file.
	Content string `json:"content,omitempty"`
}

type PromptRenderResult struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Model           string         `json:"model,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	User            string         `json:"user,omitempty"`
	System          string         `json:"system,omitempty"`
	Input           ai.Request     `json:"input"`
	Config          ai.Config      `json:"config"`
	InputSchema     map[string]any `json:"inputSchema,omitempty"`
	InputDefault    map[string]any `json:"inputDefault,omitempty"`
	OutputSchema    map[string]any `json:"outputSchema,omitempty"`
	Runtimes        []api.Model    `json:"runtimes,omitempty"`
	ValidationError string         `json:"validationError,omitempty"`
}

// PromptActionFlags is the full flag surface for `captain prompt run|render` —
// the same knobs as `captain ai prompt` (AIRuntimeOptions) plus the prompt-body
// fields — so the two commands are one. The positional (a discovered name,
// .prompt filepath, or registry id) is the prompt source; --prompt/-p and stdin
// are alternatives.
type PromptActionFlags struct {
	AIRuntimeOptions

	Prompt       string   `flag:"prompt" help:"Prompt text (or @file); alternative to the positional" short:"p"`
	System       string   `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string   `flag:"append-system" help:"Append text to the default system prompt"`
	Var          []string `flag:"var" help:"Template variable key=value (repeatable)" short:"V"`
	Attach       []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	Vars         string   `flag:"vars" help:"JSON object of template variables (HTTP callers)"`
	MultiModels  []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M"`
	Timeout      string   `flag:"timeout" help:"Request timeout (default 120s; a relocating sandbox waits for the remote agent instead)"`
	NoStream     bool     `flag:"no-stream" help:"Disable streaming; print only the final text (CLI)"`
}

func (PromptActionFlags) ClickyActionFlags() {}

type promptSource struct {
	Kind     string
	ID       string
	Label    string
	Root     string
	WalkRoot string
	FS       fs.FS
	Writable bool
	Implicit bool
}

type promptRecord struct {
	Source    promptSource
	ID        string
	Path      string
	Rel       string
	UpdatedAt string
}

type promptRef struct {
	Kind     string
	SourceID string
	RelPath  string
}

type promptInspection struct {
	Metadata     map[string]any
	InputSchema  map[string]any
	InputDefault map[string]any
	OutputSchema map[string]any
	Runtimes     []api.Model
	Variables    []PromptVariable
}

func RegisterPromptEntity() {
	registerPromptEntityOnce.Do(func() {
		clicky.NewEntity[PromptSummary, PromptListOptions, PromptDetail]("prompt").
			Aliases("prompts").
			ToolGroup("captain.prompts").
			ListWithContext(listPrompts).
			GetWithContext(getPrompt).
			CreateWithContext(createPrompt).
			UpdateWithContext(updatePrompt).
			DeleteWithContext(deletePrompt).
			WithAction(clicky.ActionWithFlagsAndContext("render", PromptActionFlags{}, renderPromptAction).
				WithShort("Render a prompt (id, name, .prompt file, --prompt/-p, or stdin) without calling a model").
				WithOptionalID()).
			WithAction(clicky.ActionWithFlagsAndContext("run", PromptActionFlags{}, runPromptAction).
				WithShort("Run a prompt (id, name, .prompt file, --prompt/-p, or stdin)").
				WithOptionalID()).
			WithAction(clicky.ActionWithFlagsAndContext("observe", PromptObserveFlags{}, observePromptAction).
				WithShort("Run exactly one runtime and emit a captain.observation/v1 JSON document").
				WithOptionalID()).
			Register()
	})
}

func ContextWithPromptDirs(ctx context.Context, dirs []string) context.Context {
	return context.WithValue(ctx, promptDirsContextKey{}, append([]string(nil), dirs...))
}

func PromptDirsMiddleware(next http.Handler, dirs []string) http.Handler {
	if len(dirs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(ContextWithPromptDirs(r.Context(), dirs)))
	})
}

func listPrompts(ctx context.Context, opts PromptListOptions) ([]PromptSummary, error) {
	records, err := listPromptRecords(ctx)
	if err != nil {
		return nil, err
	}
	filter := strings.ToLower(strings.TrimSpace(opts.Query))
	sourceFilter := strings.ToLower(strings.TrimSpace(opts.Source))
	var out []PromptSummary
	for _, record := range records {
		summary, err := promptSummary(record)
		if err != nil {
			return nil, err
		}
		if !promptSourceMatches(summary, sourceFilter) || !promptMatches(summary, filter) {
			continue
		}
		out = append(out, summary)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceKind != out[j].SourceKind {
			return out[i].SourceKind < out[j].SourceKind
		}
		return out[i].RelPath < out[j].RelPath
	})
	return out, nil
}

func getPrompt(ctx context.Context, id string) (PromptDetail, error) {
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptDetail{}, err
	}
	return promptDetail(record)
}

func createPrompt(ctx context.Context, body map[string]any) (PromptDetail, error) {
	var req PromptWriteRequest
	if err := decodePromptBody(ctx, body, &req); err != nil {
		return PromptDetail{}, err
	}
	return writeNewLocalPrompt(ctx, req)
}

func writeNewLocalPrompt(ctx context.Context, req PromptWriteRequest) (PromptDetail, error) {
	sources, err := buildPromptSources(ctx)
	if err != nil {
		return PromptDetail{}, err
	}
	source, ok := firstWritableSource(sources)
	if req.Target != "" {
		source, ok = writableSourceByID(sources, req.Target)
	}
	if !ok {
		return PromptDetail{}, fmt.Errorf("no writable prompt source configured")
	}
	rel, err := normalizeWriteRelPath(req.RelPath, req.Name)
	if err != nil {
		return PromptDetail{}, err
	}
	full, err := safeLocalPromptPath(source, rel)
	if err != nil {
		return PromptDetail{}, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return PromptDetail{}, fmt.Errorf("prompt content cannot be empty")
	}
	if _, err := os.Stat(full); err == nil {
		return PromptDetail{}, fmt.Errorf("prompt %s already exists", rel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return PromptDetail{}, fmt.Errorf("stat %s: %w", full, err)
	}
	record := promptRecord{Source: source, ID: encodePromptID(source.Kind, source.ID, rel), Path: full, Rel: rel}
	if _, err := parsedPromptDetail(record, req.Content); err != nil {
		return PromptDetail{}, invalidPromptError(err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return PromptDetail{}, fmt.Errorf("ensure prompt directory: %w", err)
	}
	if err := writePromptFileAtomic(full, req.Content); err != nil {
		return PromptDetail{}, err
	}
	return promptDetail(record)
}

func updatePrompt(ctx context.Context, id string, body map[string]any) (PromptDetail, error) {
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptDetail{}, err
	}
	if !record.Source.Writable {
		return PromptDetail{}, fmt.Errorf("prompt source %q is read-only; use create to save a copy", record.Source.Label)
	}
	var req PromptWriteRequest
	if err := decodePromptBody(ctx, body, &req); err != nil {
		return PromptDetail{}, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return PromptDetail{}, fmt.Errorf("prompt content cannot be empty")
	}
	full, err := safeLocalPromptPath(record.Source, record.Rel)
	if err != nil {
		return PromptDetail{}, err
	}
	if err := checkPromptBaseVersion(record, req.BaseVersion); err != nil {
		return PromptDetail{}, err
	}
	if _, err := parsedPromptDetail(record, req.Content); err != nil {
		return PromptDetail{}, invalidPromptError(err)
	}
	if err := writePromptFileAtomic(full, req.Content); err != nil {
		return PromptDetail{}, err
	}
	record.UpdatedAt = ""
	return promptDetail(record)
}

func deletePrompt(ctx context.Context, id string) error {
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return err
	}
	if !record.Source.Writable {
		return fmt.Errorf("embedded prompts are read-only")
	}
	full, err := safeLocalPromptPath(record.Source, record.Rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("delete prompt: %w", err)
	}
	return nil
}

// renderPromptAction renders a prompt for `captain prompt render`. HTTP callers
// pass a structured api.Spec in the body (rich overlay via overlayRuntimeSpec);
// the CLI passes flat flags (overlayCLI) plus filepath/-p/stdin sources.
func renderPromptAction(ctx context.Context, id string, flags map[string]string) (PromptRenderResult, error) {
	if _, isHTTP := clickyrpc.RequestFromContext(ctx); isHTTP {
		req, err := readRenderRequest(ctx, flags)
		if err != nil {
			return PromptRenderResult{}, err
		}
		return renderPrompt(ctx, id, req)
	}
	opts, err := actionFlagsToOptions(flags)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return renderPromptCLI(ctx, id, opts, flags["vars"], readStdinIfCLI(ctx))
}

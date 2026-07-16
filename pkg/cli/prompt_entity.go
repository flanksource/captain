package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/collections"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
	dp "github.com/google/dotprompt/go/dotprompt"
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
	Backend     string           `json:"backend,omitempty"`
	Variables   []PromptVariable `json:"variables,omitempty"`
	ParseError  string           `json:"parseError,omitempty"`
	UpdatedAt   string           `json:"updatedAt,omitempty"`
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
	Content      string         `json:"content"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	InputDefault map[string]any `json:"inputDefault,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type PromptWriteRequest struct {
	Target  string `json:"target"`
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type PromptRenderRequest struct {
	Variables map[string]any `json:"variables,omitempty"`
	Spec      *api.Spec      `json:"spec,omitempty"`
	Runtimes  []api.Model    `json:"runtimes,omitempty"`
	Chat      bool           `json:"chat,omitempty"`
}

type PromptRenderResult struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Model           string         `json:"model,omitempty"`
	Backend         string         `json:"backend,omitempty"`
	User            string         `json:"user,omitempty"`
	System          string         `json:"system,omitempty"`
	Input           ai.Request     `json:"input"`
	Config          ai.Config      `json:"config"`
	InputSchema     map[string]any `json:"inputSchema,omitempty"`
	InputDefault    map[string]any `json:"inputDefault,omitempty"`
	OutputSchema    map[string]any `json:"outputSchema,omitempty"`
	ValidationError string         `json:"validationError,omitempty"`
}

// PromptActionFlags is the full flag surface for `captain prompt run|render` —
// the same knobs as `captain ai prompt` (AIRuntimeOptions) plus the prompt-body
// fields — so the two commands are one. The positional (a .prompt filepath or a
// registry id) is the prompt source; --prompt/-p and stdin are alternatives.
type PromptActionFlags struct {
	AIRuntimeOptions

	Prompt       string   `flag:"prompt" help:"Prompt text (or @file); alternative to the positional" short:"p"`
	System       string   `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string   `flag:"append-system" help:"Append text to the default system prompt"`
	Var          []string `flag:"var" help:"Template variable key=value (repeatable)" short:"V"`
	Attach       []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	Vars         string   `flag:"vars" help:"JSON object of template variables (HTTP callers)"`
	MultiModels  []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M"`
	Timeout      string   `flag:"timeout" help:"Request timeout" default:"120s"`
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
	Source promptSource
	ID     string
	Path   string
	Rel    string
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
				WithShort("Render a prompt (id, .prompt file, --prompt/-p, or stdin) without calling a model").
				WithOptionalID()).
			WithAction(clicky.ActionWithFlagsAndContext("run", PromptActionFlags{}, runPromptAction).
				WithShort("Run a prompt (id, .prompt file, --prompt/-p, or stdin)").
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
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return PromptDetail{}, fmt.Errorf("ensure prompt directory: %w", err)
	}
	if err := os.WriteFile(full, []byte(req.Content), 0o644); err != nil {
		return PromptDetail{}, fmt.Errorf("write prompt: %w", err)
	}
	return promptDetail(promptRecord{Source: source, ID: encodePromptID(source.Kind, source.ID, rel), Path: full, Rel: rel})
}

func updatePrompt(ctx context.Context, id string, body map[string]any) (PromptDetail, error) {
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptDetail{}, err
	}
	var req PromptWriteRequest
	if err := decodePromptBody(ctx, body, &req); err != nil {
		return PromptDetail{}, err
	}
	if strings.TrimSpace(req.Content) == "" {
		return PromptDetail{}, fmt.Errorf("prompt content cannot be empty")
	}
	if !record.Source.Writable {
		if strings.TrimSpace(req.RelPath) == "" {
			req.RelPath = localForkRelPath(record)
		}
		return writeNewLocalPrompt(ctx, req)
	}
	full, err := safeLocalPromptPath(record.Source, record.Rel)
	if err != nil {
		return PromptDetail{}, err
	}
	if err := os.WriteFile(full, []byte(req.Content), 0o644); err != nil {
		return PromptDetail{}, fmt.Errorf("write prompt: %w", err)
	}
	return promptDetail(record)
}

// localForkRelPath derives the destination path for a read-only (embedded)
// prompt saved into a writable source, stripping the source walk root so an
// embedded "testdata/commit.prompt" lands as "commit.prompt".
func localForkRelPath(record promptRecord) string {
	rel := record.Rel
	if root := record.Source.WalkRoot; root != "" {
		rel = strings.TrimPrefix(rel, root+"/")
	}
	return rel
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

// renderPrompt is the HTTP/Spec render path: overlay a structured api.Spec (the
// web UI's rich runtime overrides) onto the rendered template.
func renderPrompt(ctx context.Context, id string, renderReq PromptRenderRequest) (PromptRenderResult, error) {
	if strings.TrimSpace(id) == "" {
		return renderEphemeralPrompt(renderReq)
	}
	record, err := resolvePromptRecord(ctx, id)
	if err != nil {
		return PromptRenderResult{}, err
	}
	content, err := readPromptContent(record)
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars := renderReq.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	req, cfg, err := promptlib.Load(content).Render(vars, nil)
	if err != nil {
		return PromptRenderResult{}, err
	}
	req.Prompt.Source = record.Rel
	if renderReq.Spec != nil {
		overlayRuntimeSpec(&req, &cfg, *renderReq.Spec)
	}
	applyPromptDefaults(&req, &cfg)
	cwd, err := os.Getwd()
	if err != nil {
		return PromptRenderResult{}, fmt.Errorf("get working directory: %w", err)
	}
	if err := normalizePromptContextDir(&req, cwd); err != nil {
		return PromptRenderResult{}, err
	}
	return finalizeRenderResult(record, content, req, cfg)
}

func renderEphemeralPrompt(renderReq PromptRenderRequest) (PromptRenderResult, error) {
	record := promptRecord{
		Source: promptSource{Kind: "ephemeral", ID: "ephemeral", Label: "Ephemeral"},
		ID:     "",
		Path:   "<ephemeral>",
		Rel:    "scratch.prompt",
	}
	content := ephemeralPromptContent()
	var req ai.Request
	var cfg ai.Config
	if renderReq.Spec != nil {
		overlayRuntimeSpec(&req, &cfg, *renderReq.Spec)
	}
	if req.Prompt.Source == "" {
		req.Prompt.Source = "<ephemeral>"
	}
	applyPromptDefaults(&req, &cfg)
	cwd, err := os.Getwd()
	if err != nil {
		return PromptRenderResult{}, fmt.Errorf("get working directory: %w", err)
	}
	if err := normalizePromptContextDir(&req, cwd); err != nil {
		return PromptRenderResult{}, err
	}
	return finalizeRenderResult(record, content, req, cfg)
}

func ephemeralPromptContent() string {
	return `---
name: Scratch Prompt
description: Ephemeral prompt
---
{{role "user"}}
`
}

// renderPromptCLI is the CLI render path: load from id | .prompt filepath | -p |
// stdin and overlay the flat CLI flags (overlayCLI).
func renderPromptCLI(ctx context.Context, id string, opts AIPromptOptions, varsJSON, stdin string) (PromptRenderResult, error) {
	content, source, usedStdin, record, err := loadPromptContent(ctx, id, opts, stdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	vars, err := promptVars(opts, varsJSON, stdin, usedStdin)
	if err != nil {
		return PromptRenderResult{}, err
	}
	req, cfg, err := renderLoadedContent(content, source, vars, opts)
	if err != nil {
		return PromptRenderResult{}, err
	}
	return finalizeRenderResult(record, content, req, cfg)
}

// finalizeRenderResult packages the rendered request/config + prompt detail into
// a PromptRenderResult and sets the validation error (shared by both paths).
func finalizeRenderResult(record promptRecord, content string, req ai.Request, cfg ai.Config) (PromptRenderResult, error) {
	// Normalize a comma-separated model into a clean primary + fallbacks so the
	// displayed Model is a single name, then catch a mistyped model (primary or any
	// fallback) at render time, not just on run.
	req.Model = req.ExpandCSV()
	cfg.Model = cfg.Model.ExpandCSV()
	var err error
	req.Model, err = ai.ResolveModelSelectors(req.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	cfg.Model, err = ai.ResolveModelSelectors(cfg.Model)
	if err != nil {
		return PromptRenderResult{}, err
	}
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	detail, err := promptDetailFromContent(record, content)
	if err != nil {
		return PromptRenderResult{}, err
	}
	result := PromptRenderResult{
		ID:           detail.ID,
		Name:         detail.Name,
		Model:        cfg.Model.Name,
		Backend:      string(cfg.Model.Backend),
		User:         req.Prompt.User,
		System:       req.Prompt.System,
		Input:        req,
		Config:       cfg,
		InputSchema:  detail.InputSchema,
		InputDefault: detail.InputDefault,
		OutputSchema: detail.OutputSchema,
	}
	switch {
	case req.Prompt.User == "" && len(req.Prompt.Attachments) == 0 && !req.IsVerifyOnly():
		result.ValidationError = "prompt text or attachment required"
	case cfg.Model.Name == "":
		result.ValidationError = "no model: set prompt frontmatter, pass a model override, or run 'captain configure'"
	default:
		if err := req.Validate(); err != nil {
			result.ValidationError = err.Error()
		}
	}
	return result, nil
}

func overlayRuntimeSpec(req *ai.Request, cfg *ai.Config, spec api.Spec) {
	if spec.Name != "" {
		req.Name = spec.Name
		cfg.Model.Name = spec.Name
		req.ID = spec.ID
		cfg.Model.ID = spec.ID
		req.Backend = spec.Backend
		cfg.Model.Backend = spec.Backend
	} else {
		if spec.ID != "" {
			req.ID = spec.ID
			cfg.Model.ID = spec.ID
		}
		if spec.Backend != "" {
			req.Backend = spec.Backend
			cfg.Model.Backend = spec.Backend
		}
	}
	if spec.Temperature != nil {
		req.Temperature = spec.Temperature
		cfg.Model.Temperature = spec.Temperature
	}
	if spec.Effort != "" {
		req.Effort = spec.Effort
		cfg.Model.Effort = spec.Effort
	}
	if len(spec.Fallbacks) > 0 {
		req.Fallbacks = spec.Fallbacks
		cfg.Model.Fallbacks = spec.Fallbacks
	}
	req.NoCache = req.NoCache || spec.NoCache
	cfg.NoCache = cfg.NoCache || spec.NoCache
	if spec.Budget.Cost > 0 {
		req.Budget.Cost = spec.Budget.Cost
		cfg.Budget.Cost = spec.Budget.Cost
	}
	if spec.Budget.MaxTokens > 0 {
		req.Budget.MaxTokens = spec.Budget.MaxTokens
		cfg.Budget.MaxTokens = spec.Budget.MaxTokens
	}
	if spec.Budget.MaxTurns > 0 {
		req.Budget.MaxTurns = spec.Budget.MaxTurns
		cfg.Budget.MaxTurns = spec.Budget.MaxTurns
	}
	if spec.Budget.Timeout != "" {
		req.Budget.Timeout = spec.Budget.Timeout
		cfg.Budget.Timeout = spec.Budget.Timeout
	}

	if spec.Prompt.User != "" {
		req.Prompt.User = spec.Prompt.User
	}
	if spec.Prompt.System != "" {
		req.Prompt.System = spec.Prompt.System
	}
	if spec.Prompt.AppendSystem != "" {
		req.Prompt.AppendSystem = spec.Prompt.AppendSystem
	}
	if spec.Prompt.Source != "" {
		req.Prompt.Source = spec.Prompt.Source
	}
	req.Prompt.Metadata = mergeStringMaps(req.Prompt.Metadata, spec.Prompt.Metadata)
	if len(spec.Prompt.SchemaJSON) > 0 {
		req.Prompt.SchemaJSON = spec.Prompt.SchemaJSON
	}
	if spec.Prompt.SchemaStrictness != "" {
		req.Prompt.SchemaStrictness = spec.Prompt.SchemaStrictness
	}
	if spec.Workflow != nil {
		req.Workflow = spec.Workflow
	}

	if spec.Permissions.Mode != "" {
		req.Permissions.Mode = spec.Permissions.Mode
	}
	req.Permissions.Presets = mergePresets(req.Permissions.Presets, spec.Permissions.Presets)
	toolPolicies := spec.Permissions.Tools.Policies()
	if len(toolPolicies) > 0 {
		req.Permissions.Tools.Allow = nil
		req.Permissions.Tools.Deny = nil
		req.Permissions.Tools.Modes = nil
		for _, tool := range sortedStringKeys(toolPolicies) {
			switch toolPolicies[tool] {
			case api.ToolPolicyAllow:
				req.Permissions.Tools.Allow = append(req.Permissions.Tools.Allow, tool)
			case api.ToolPolicyDeny:
				req.Permissions.Tools.Deny = append(req.Permissions.Tools.Deny, tool)
			case api.ToolPolicyAsk:
				if req.Permissions.Tools.Modes == nil {
					req.Permissions.Tools.Modes = map[string]api.ToolMode{}
				}
				req.Permissions.Tools.Modes[tool] = api.ToolModeAsk
			case api.ToolPolicyAuto:
				if req.Permissions.Tools.Modes == nil {
					req.Permissions.Tools.Modes = map[string]api.ToolMode{}
				}
				req.Permissions.Tools.Modes[tool] = api.ToolModeEnabled
			}
		}
	}
	req.Permissions.Tools.Modes = mergeToolModes(req.Permissions.Tools.Modes, spec.Permissions.Tools.Modes)
	req.Permissions.MCP.Disabled = req.Permissions.MCP.Disabled || spec.Permissions.MCP.Disabled
	if servers := spec.Permissions.MCP.EnabledServers(); len(servers) > 0 {
		req.Permissions.MCP.Servers = servers
	}
	if len(spec.Permissions.Plugins) > 0 {
		req.Permissions.Plugins = enabledResourcePolicies(spec.Permissions.Plugins)
	}

	skills := append([]string(nil), spec.Memory.Skills...)
	skills = append(skills, spec.Permissions.Skills.Enabled()...)
	if len(skills) > 0 {
		req.Memory.Skills = dedupeStrings(skills)
	}
	req.Memory.SkipProject = req.Memory.SkipProject || spec.Memory.SkipProject
	req.Memory.SkipUser = req.Memory.SkipUser || spec.Memory.SkipUser
	req.Memory.SkipSkills = req.Memory.SkipSkills || spec.Memory.SkipSkills
	req.Memory.SkipHooks = req.Memory.SkipHooks || spec.Memory.SkipHooks
	req.Memory.SkipMemory = req.Memory.SkipMemory || spec.Memory.SkipMemory
	req.Memory.Bare = req.Memory.Bare || spec.Memory.Bare

	if spec.Setup != nil {
		req.Setup = spec.Setup
	}

	if spec.SessionID != "" {
		req.SessionID = spec.SessionID
		cfg.SessionID = spec.SessionID
	}
}

func applyPromptDefaults(req *ai.Request, cfg *ai.Config) {
	savedCfg := loadSavedConfig()
	saved := savedCfg.AI
	promptModel := req.Model
	if promptModel.Name == "" {
		promptModel.Name = cfg.Model.Name
	}
	if promptModel.ID == "" {
		promptModel.ID = cfg.Model.ID
	}
	if promptModel.Backend == "" {
		promptModel.Backend = cfg.Model.Backend
	}
	identity := selectModelIdentity(
		api.Model{Name: saved.Model, Backend: api.Backend(saved.Backend)},
		api.Model{Name: promptModel.Name, ID: promptModel.ID, Backend: promptModel.Backend},
	)
	req.Name, req.ID, req.Backend = identity.Name, identity.ID, identity.Backend
	if cfg.Model.Effort != api.EffortNone {
		// An effort-qualified model selector (for example agent:sol:high)
		// is model-local and intentionally overrides the request-wide flag/default.
		req.Effort = cfg.Model.Effort
	} else if req.Effort == "" {
		req.Effort = api.Effort(firstNonEmpty(string(cfg.Model.Effort), saved.ReasoningEffort))
	}
	req.NoCache = req.NoCache || saved.NoCache
	if req.Budget.MaxTokens == 0 {
		req.Budget.MaxTokens = firstPositive(cfg.Budget.MaxTokens, saved.MaxTokens, 4096)
	}
	if req.Budget.Cost == 0 {
		req.Budget.Cost = firstPositiveFloat(cfg.Budget.Cost, saved.BudgetUSD)
	}

	cfg.Model = req.Model
	cfg.Budget = req.Budget
	cfg.NoCache = req.NoCache
	if isZeroSchemaRepair(cfg.SchemaRepair) {
		cfg.SchemaRepair = schemaRepairConfig(savedCfg.Prompts.SchemaRepair)
	}
}

func mergeStringMaps(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]string, collections.SafeAdd(len(base), len(overlay)))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func mergeToolModes(base, overlay map[string]api.ToolMode) map[string]api.ToolMode {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]api.ToolMode, collections.SafeAdd(len(base), len(overlay)))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func mergePresets(base, overlay []api.Preset) []api.Preset {
	if len(overlay) == 0 {
		return base
	}
	seen := make(map[api.Preset]bool, collections.SafeAdd(len(base), len(overlay)))
	out := make([]api.Preset, 0, collections.SafeAdd(len(base), len(overlay)))
	for _, preset := range base {
		if seen[preset] {
			continue
		}
		seen[preset] = true
		out = append(out, preset)
	}
	for _, preset := range overlay {
		if seen[preset] {
			continue
		}
		seen[preset] = true
		out = append(out, preset)
	}
	return out
}

func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func enabledResourcePolicies(in api.ResourcePolicies) api.ResourcePolicies {
	out := api.ResourcePolicies{}
	for _, key := range sortedStringKeys(in) {
		if in[key] == api.ResourceEnabled {
			out[key] = api.ResourceEnabled
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func readRenderRequest(ctx context.Context, flags map[string]string) (PromptRenderRequest, error) {
	var req PromptRenderRequest
	if err := decodePromptBody(ctx, map[string]any{}, &req); err != nil {
		return PromptRenderRequest{}, err
	}
	if req.Variables == nil {
		req.Variables = map[string]any{}
	}
	if err := mergePromptActionFlags(&req, flags); err != nil {
		return PromptRenderRequest{}, err
	}
	return req, nil
}

func mergePromptActionFlags(req *PromptRenderRequest, flags map[string]string) error {
	if len(flags) == 0 {
		return nil
	}
	if raw := strings.TrimSpace(flags["vars"]); raw != "" {
		var vars map[string]any
		if err := json.Unmarshal([]byte(raw), &vars); err != nil {
			return fmt.Errorf("parse --vars JSON: %w", err)
		}
		req.Variables = vars
	}
	if v := strings.TrimSpace(flags["model"]); v != "" {
		ensureRenderSpec(req).Name = v
	}
	if v := strings.TrimSpace(flags["fallback"]); v != "" {
		ensureRenderSpec(req).Fallbacks = fallbackModelsFromFlags([]string{v})
	}
	if v := strings.TrimSpace(flags["backend"]); v != "" {
		ensureRenderSpec(req).Backend = api.Backend(v)
	}
	if v := strings.TrimSpace(flags["timeout"]); v != "" {
		ensureRenderSpec(req).Budget.Timeout = v
	}
	if v := strings.TrimSpace(flags["max-tokens"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid --max-tokens %q: %w", v, err)
		}
		ensureRenderSpec(req).Budget.MaxTokens = n
	}
	return nil
}

func ensureRenderSpec(req *PromptRenderRequest) *api.Spec {
	if req.Spec == nil {
		req.Spec = &api.Spec{}
	}
	return req.Spec
}

func decodePromptBody(ctx context.Context, flat map[string]any, dst any) error {
	if r, ok := clickyrpc.RequestFromContext(ctx); ok && r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if len(strings.TrimSpace(string(body))) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(dst); err != nil {
				return fmt.Errorf("decode request body: %w", err)
			}
			return nil
		}
	}
	if len(flat) == 0 {
		return nil
	}
	data, err := json.Marshal(flat)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode command body: %w", err)
	}
	return nil
}

func runtimeTimeout(raw string) time.Duration {
	timeout, _ := time.ParseDuration(raw)
	if timeout <= 0 {
		return 120 * time.Second
	}
	return timeout
}

func listPromptRecords(ctx context.Context) ([]promptRecord, error) {
	sources, err := buildPromptSources(ctx)
	if err != nil {
		return nil, err
	}
	var records []promptRecord
	for _, source := range sources {
		recs, err := listPromptRecordsFromSource(source)
		if err != nil {
			return nil, err
		}
		records = append(records, recs...)
	}
	return records, nil
}

func listPromptRecordsFromSource(source promptSource) ([]promptRecord, error) {
	var records []promptRecord
	add := func(rel string, info fs.FileInfo) {
		rel = filepath.ToSlash(rel)
		path := rel
		if source.Root != "" {
			path = filepath.Join(source.Root, filepath.FromSlash(rel))
		}
		updatedAt := ""
		if info != nil && !info.ModTime().IsZero() {
			updatedAt = info.ModTime().Format(time.RFC3339)
		}
		records = append(records, promptRecord{
			Source: source,
			ID:     encodePromptID(source.Kind, source.ID, rel),
			Path:   path + "\x00" + updatedAt,
			Rel:    rel,
		})
	}

	if source.FS != nil {
		err := fs.WalkDir(source.FS, source.WalkRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".prompt") {
				return nil
			}
			info, _ := d.Info()
			add(path, info)
			return nil
		})
		return records, err
	}

	err := filepath.WalkDir(source.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(name, ".prompt") {
			return nil
		}
		rel, err := filepath.Rel(source.Root, path)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		add(rel, info)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) && source.Implicit {
		return records, nil
	}
	return records, err
}

func resolvePromptRecord(ctx context.Context, id string) (promptRecord, error) {
	if looksLikePromptPath(id) {
		return filePromptRecord(id)
	}
	ref, err := decodePromptID(id)
	if err != nil {
		return promptRecord{}, err
	}
	sources, err := buildPromptSources(ctx)
	if err != nil {
		return promptRecord{}, err
	}
	for _, source := range sources {
		if source.Kind != ref.Kind || source.ID != ref.SourceID {
			continue
		}
		path := ref.RelPath
		if source.Root != "" {
			path = filepath.Join(source.Root, filepath.FromSlash(ref.RelPath))
		}
		return promptRecord{Source: source, ID: id, Path: path, Rel: ref.RelPath}, nil
	}
	return promptRecord{}, fmt.Errorf("prompt source %q not found", ref.SourceID)
}

// looksLikePromptPath reports whether id is a filesystem path rather than a
// base64 registry id. Registry ids are base64-raw-url (no ".", "/", or leading
// "."), so a .prompt suffix, a path separator, or a leading "." marks a path.
func looksLikePromptPath(id string) bool {
	return strings.HasSuffix(id, ".prompt") ||
		strings.ContainsRune(id, os.PathSeparator) ||
		strings.HasPrefix(id, ".")
}

// filePromptRecord resolves an ad-hoc .prompt file path (not a registered id)
// into a record readable via readPromptContent/safeLocalPromptPath. Mirrors the
// captain-ai-prompt file loader so `captain prompt run|render ./x.prompt` works.
func filePromptRecord(id string) (promptRecord, error) {
	abs, err := filepath.Abs(id)
	if err != nil {
		return promptRecord{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return promptRecord{}, fmt.Errorf("prompt file %s: %w", id, err)
	}
	if info.IsDir() {
		return promptRecord{}, fmt.Errorf("%s is a directory, not a .prompt file", id)
	}
	return promptRecord{
		Source: promptSource{Kind: "file", ID: "file", Label: "File", Root: filepath.Dir(abs)},
		ID:     id,
		Path:   abs,
		Rel:    filepath.Base(abs),
	}, nil
}

func promptSummary(record promptRecord) (PromptSummary, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptSummary{}, err
	}
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		summary = basePromptSummary(record)
		summary.ParseError = err.Error()
	}
	if idx := strings.LastIndex(record.Path, "\x00"); idx >= 0 {
		summary.UpdatedAt = strings.TrimPrefix(record.Path[idx+1:], "\x00")
	}
	return summary, nil
}

func promptDetail(record promptRecord) (PromptDetail, error) {
	content, err := readPromptContent(record)
	if err != nil {
		return PromptDetail{}, err
	}
	return promptDetailFromContent(record, content)
}

func promptDetailFromContent(record promptRecord, content string) (PromptDetail, error) {
	summary, err := promptSummaryFromContent(record, content)
	if err != nil {
		return PromptDetail{}, err
	}
	inspection, err := inspectPrompt(content, nil)
	if err != nil {
		return PromptDetail{}, err
	}
	return PromptDetail{
		PromptSummary: summary,
		Content:       content,
		InputSchema:   inspection.InputSchema,
		InputDefault:  inspection.InputDefault,
		OutputSchema:  inspection.OutputSchema,
		Metadata:      inspection.Metadata,
	}, nil
}

func promptSummaryFromContent(record promptRecord, content string) (PromptSummary, error) {
	tmpl := promptlib.Load(content)
	req, cfg, err := tmpl.Render(map[string]any{}, nil)
	if err != nil {
		return PromptSummary{}, err
	}
	inspection, err := inspectPrompt(content, nil)
	if err != nil {
		return PromptSummary{}, err
	}
	summary := basePromptSummary(record)
	if v, ok := inspection.Metadata["name"].(string); ok && strings.TrimSpace(v) != "" {
		summary.Name = strings.TrimSpace(v)
	}
	if v, ok := inspection.Metadata["description"].(string); ok {
		summary.Description = strings.TrimSpace(v)
	}
	summary.Model = firstNonEmpty(cfg.Model.Name, req.Name)
	summary.Backend = firstNonEmpty(string(cfg.Model.Backend), string(req.Backend))
	summary.Variables = inspection.Variables
	return summary, nil
}

func basePromptSummary(record promptRecord) PromptSummary {
	name := strings.TrimSuffix(filepath.Base(record.Rel), ".prompt")
	return PromptSummary{
		ID:         record.ID,
		Name:       name,
		SourceKind: record.Source.Kind,
		SourceID:   record.Source.ID,
		Source:     record.Source.Label,
		Path:       displayPromptPath(record),
		RelPath:    record.Rel,
		Writable:   record.Source.Writable,
	}
}

func displayPromptPath(record promptRecord) string {
	if idx := strings.LastIndex(record.Path, "\x00"); idx >= 0 {
		return record.Path[:idx]
	}
	return record.Path
}

func readPromptContent(record promptRecord) (string, error) {
	if record.Source.FS != nil {
		data, err := fs.ReadFile(record.Source.FS, record.Rel)
		if err != nil {
			return "", fmt.Errorf("read embedded prompt %s: %w", record.Rel, err)
		}
		return string(data), nil
	}
	full, err := safeLocalPromptPath(record.Source, record.Rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", full, err)
	}
	return string(data), nil
}

func inspectPrompt(content string, data map[string]any) (promptInspection, error) {
	if data == nil {
		data = map[string]any{}
	}
	rendered, err := dp.NewDotprompt(nil).Render(content, &dp.DataArgument{Input: data}, nil)
	if err != nil {
		return promptInspection{}, err
	}
	metadata := map[string]any{}
	if rendered.Raw != nil {
		for k, v := range rendered.Raw {
			metadata[k] = v
		}
	}
	if rendered.Name != "" {
		metadata["name"] = rendered.Name
	}
	if rendered.Description != "" {
		metadata["description"] = rendered.Description
	}
	if rendered.Model != "" {
		metadata["model"] = rendered.Model
	}
	inputSchema := anyToMap(rendered.Input.Schema)
	inputDefault := map[string]any{}
	for k, v := range rendered.Input.Default {
		inputDefault[k] = v
	}
	return promptInspection{
		Metadata:     metadata,
		InputSchema:  inputSchema,
		InputDefault: inputDefault,
		OutputSchema: anyToMap(rendered.Output.Schema),
		Variables:    variablesFromSchema(inputSchema),
	}, nil
}

func anyToMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func variablesFromSchema(schema map[string]any) []PromptVariable {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	required := map[string]bool{}
	if raw, ok := schema["required"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				required[s] = true
			}
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var vars []PromptVariable
	for _, name := range keys {
		prop, _ := props[name].(map[string]any)
		item := PromptVariable{Name: name, Required: required[name]}
		if v, ok := prop["type"].(string); ok {
			item.Type = v
		}
		if v, ok := prop["description"].(string); ok {
			item.Description = v
		}
		vars = append(vars, item)
	}
	return vars
}

func promptSourceMatches(summary PromptSummary, source string) bool {
	switch source {
	case "", "all":
		return true
	case "embedded":
		return summary.SourceKind == "embedded"
	case "local":
		return summary.SourceKind == "local"
	default:
		return summary.SourceID == source || strings.EqualFold(summary.Source, source)
	}
}

func promptMatches(summary PromptSummary, filter string) bool {
	if filter == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		summary.Name,
		summary.Description,
		summary.Source,
		summary.Path,
		summary.RelPath,
		summary.Model,
		summary.Backend,
	}, "\n"))
	return strings.Contains(haystack, filter)
}

func buildPromptSources(ctx context.Context) ([]promptSource, error) {
	sources := []promptSource{{
		Kind:     "embedded",
		ID:       "embedded",
		Label:    "Embedded examples",
		WalkRoot: "testdata",
		FS:       promptlib.Examples,
		Writable: false,
	}}

	seen := map[string]bool{}
	addLocal := func(raw, base string) error {
		dir, err := resolvePromptDir(raw, base)
		if err != nil {
			return err
		}
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		sources = append(sources, promptSource{
			Kind:     "local",
			ID:       hashPromptDir(dir),
			Label:    dir,
			Root:     dir,
			Writable: true,
		})
		return nil
	}

	cfg, exists, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	if exists {
		configPath, err := captainconfig.Path()
		if err != nil {
			return nil, err
		}
		base := filepath.Dir(configPath)
		for _, dir := range cfg.Prompts.Dirs {
			if err := addLocal(dir, base); err != nil {
				return nil, err
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for _, dir := range promptDirsFromContext(ctx) {
		if err := addLocal(dir, cwd); err != nil {
			return nil, err
		}
	}
	if _, ok := firstWritableSource(sources); !ok {
		dir := filepath.Join(cwd, ".captain", "prompts")
		sources = append(sources, promptSource{
			Kind:     "local",
			ID:       hashPromptDir(dir),
			Label:    dir,
			Root:     dir,
			Writable: true,
			Implicit: true,
		})
	}
	return sources, nil
}

func promptDirsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if dirs, ok := ctx.Value(promptDirsContextKey{}).([]string); ok {
		return dirs
	}
	return nil
}

func resolvePromptDir(raw, base string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", fmt.Errorf("prompt dir cannot be empty")
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch {
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, dir[2:])
		default:
			return "", fmt.Errorf("unsupported home-relative prompt dir %q", raw)
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("prompt dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("prompt dir %s is not a directory", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func writableSourceByID(sources []promptSource, id string) (promptSource, bool) {
	for _, source := range sources {
		if source.ID == id && source.Writable {
			return source, true
		}
	}
	return promptSource{}, false
}

func firstWritableSource(sources []promptSource) (promptSource, bool) {
	for _, source := range sources {
		if source.Writable {
			return source, true
		}
	}
	return promptSource{}, false
}

func safeLocalPromptPath(source promptSource, rel string) (string, error) {
	cleanRel := strings.TrimPrefix(filepath.Clean(filepath.FromSlash(rel)), string(filepath.Separator))
	if cleanRel == "." || cleanRel == "" || filepath.IsAbs(rel) || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || cleanRel == ".." {
		return "", fmt.Errorf("invalid prompt path %q", rel)
	}
	if filepath.Ext(cleanRel) != ".prompt" {
		return "", fmt.Errorf("prompt path must end with .prompt")
	}
	full := filepath.Join(source.Root, cleanRel)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(source.Root, abs)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || relToRoot == ".." || filepath.IsAbs(relToRoot) {
		return "", fmt.Errorf("prompt path escapes source root")
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("prompt symlinks are not supported")
	}
	return abs, nil
}

func normalizeWriteRelPath(relPath, name string) (string, error) {
	rel := strings.TrimSpace(relPath)
	if rel == "" {
		rel = slugPromptName(name)
	}
	if rel == "" {
		return "", fmt.Errorf("prompt name or path required")
	}
	if !strings.HasSuffix(rel, ".prompt") {
		rel += ".prompt"
	}
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if strings.HasPrefix(rel, "../") || rel == ".." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("invalid prompt path %q", relPath)
	}
	return rel, nil
}

func slugPromptName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func encodePromptID(kind, sourceID, rel string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "\x00" + sourceID + "\x00" + filepath.ToSlash(rel)))
}

func decodePromptID(id string) (promptRef, error) {
	data, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return promptRef{}, fmt.Errorf("invalid prompt id: %w", err)
	}
	parts := strings.SplitN(string(data), "\x00", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return promptRef{}, fmt.Errorf("invalid prompt id")
	}
	return promptRef{Kind: parts[0], SourceID: parts[1], RelPath: filepath.ToSlash(parts[2])}, nil
}

func hashPromptDir(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:12]
}

func ValidatePromptDirs(dirs []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if _, err := resolvePromptDir(dir, cwd); err != nil {
			return err
		}
	}
	return nil
}

var _ clicky.EntityItem = PromptSummary{}
var _ clickyapi.TableProvider = PromptSummary{}

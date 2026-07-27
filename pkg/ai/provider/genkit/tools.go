package genkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flanksource/captain/pkg/ai"
	captools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

// maxToolTurns caps the model↔tool iteration so a misbehaving loop terminates.
const (
	maxToolTurns            = 16
	anthropicMaxStrictTools = 20
)

// SupportsCallerTools reports that the genkit provider can expose and execute
// api.Config.Tools. It satisfies api.ToolCapableProvider.
func (p *Provider) SupportsCallerTools() bool { return true }

// toolOptions builds the genkit generate options for the caller-supplied tools
// on the provider config. Each tool runs in-process via its handler; emit, when
// non-nil, receives EventToolUse / EventPermission / EventToolResult as the
// model calls them. Returns nil when there are no caller tools, so a run with
// none is byte-for-byte unchanged.
func (p *Provider) toolOptions(preferences api.ToolPreferences, emit func(ai.Event)) ([]gkai.GenerateOption, error) {
	definitions, err := resolveToolDefinitions(p.cfg.Tools, preferences)
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, nil
	}
	if p.backend == ai.BackendAnthropic {
		if count := explicitStrictToolCount(definitions); count > anthropicMaxStrictTools {
			log.Warnf("Anthropic supports at most %d strict tools; keeping %d of %d explicit opt-ins strict and sending %d as non-strict", anthropicMaxStrictTools, anthropicMaxStrictTools, count, count-anthropicMaxStrictTools)
		}
		definitions = anthropicStrictToolDefinitions(definitions)
	}
	refs := make([]gkai.ToolRef, 0, len(definitions))
	for i := range definitions {
		refs = append(refs, p.genkitTool(definitions[i], emit, p.toolCorrelation))
	}
	return []gkai.GenerateOption{gkai.WithTools(refs...), gkai.WithMaxTurns(maxToolTurns)}, nil
}

func resolveToolDefinitions(definitions []api.ToolDefinition, preferences api.ToolPreferences) ([]api.ToolDefinition, error) {
	if err := preferences.Validate(); err != nil {
		return nil, err
	}
	selected := make([]api.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return nil, fmt.Errorf("genkit tool name cannot be empty")
		}
		if definition.Handler == nil {
			return nil, fmt.Errorf("genkit tool %q has no handler", definition.Name)
		}
		mode, err := effectiveToolMode(definition, preferences)
		if err != nil {
			return nil, err
		}
		if mode == api.ToolModeOff {
			continue
		}
		definition.DefaultPermission = mode
		selected = append(selected, definition)
	}
	return selected, nil
}

func effectiveToolMode(definition api.ToolDefinition, preferences api.ToolPreferences) (api.ToolMode, error) {
	defaultMode := api.ToolModeAuto
	if definition.DefaultPermission != "" {
		var ok bool
		defaultMode, ok = api.NormalizeToolMode(definition.DefaultPermission)
		if !ok {
			return "", fmt.Errorf("genkit tool %q has invalid default permission %q", definition.Name, definition.DefaultPermission)
		}
	}
	info := captools.ToolInfo{Name: definition.Name, Group: definition.Group}
	if preferred, ok := captools.EffectivePreference(preferences, info); ok && preferred != api.ToolModeAuto {
		return preferred, nil
	}
	return defaultMode, nil
}

func anthropicStrictToolDefinitions(definitions []api.ToolDefinition) []api.ToolDefinition {
	type candidate struct {
		index int
		def   api.ToolDefinition
	}
	candidates := make([]candidate, 0, len(definitions))
	for i, definition := range definitions {
		if definition.Strict != nil && *definition.Strict {
			candidates = append(candidates, candidate{index: i, def: definition})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if left, right := strictRiskRank(candidates[i].def), strictRiskRank(candidates[j].def); left != right {
			return left < right
		}
		if candidates[i].def.Name != candidates[j].def.Name {
			return candidates[i].def.Name < candidates[j].def.Name
		}
		return candidates[i].index < candidates[j].index
	})
	strict := make(map[int]bool, min(len(candidates), anthropicMaxStrictTools))
	for _, candidate := range candidates[:min(len(candidates), anthropicMaxStrictTools)] {
		strict[candidate.index] = true
	}
	shaped := make([]api.ToolDefinition, len(definitions))
	for i, definition := range definitions {
		value := strict[i]
		definition.Strict = &value
		shaped[i] = definition
	}
	return shaped
}

func explicitStrictToolCount(definitions []api.ToolDefinition) int {
	count := 0
	for _, definition := range definitions {
		if definition.Strict != nil && *definition.Strict {
			count++
		}
	}
	return count
}

func strictRiskRank(definition api.ToolDefinition) int {
	switch {
	case definition.DestructiveHint != nil && *definition.DestructiveHint:
		return 0
	case definition.IdempotentHint != nil && !*definition.IdempotentHint:
		return 1
	case definition.ReadOnlyHint != nil && !*definition.ReadOnlyHint:
		return 2
	case definition.ReadOnlyHint == nil:
		return 3
	default:
		return 4
	}
}

// genkitTool wraps one api.ToolDefinition as an ephemeral genkit tool. Ephemeral
// (NewTool, passed via WithTools) rather than genkit.DefineTool:
// the genkit instance is cached and shared per (backend, apiKey), so registering
// tools globally would leak them across unrelated requests.
func (p *Provider) genkitTool(def api.ToolDefinition, emit func(ai.Event), correlation *toolEventCorrelation) gkai.ToolRef {
	handler := func(tc *gkai.ToolContext, input any) (any, error) {
		return p.runTool(tc.Context, def, input, emit, correlation)
	}
	options := []gkai.ToolOption{gkai.WithInputSchema(def.InputSchema)}
	if def.Strict != nil {
		options = append(options, gkai.WithStrictSchema(*def.Strict))
	}
	return gkai.NewTool(def.Name, def.Description, handler, options...)
}

// runTool gates a caller tool through Config.CanUseTool (when it needs approval),
// runs its handler, and publishes the use/permission/result events. On a denied
// or errored call it feeds a message back to the model as the tool result so the
// model can react rather than the whole generation failing.
func (p *Provider) runTool(
	ctx context.Context,
	def api.ToolDefinition,
	input any,
	emit func(ai.Event),
	correlation *toolEventCorrelation,
) (any, error) {
	args := toArgsMap(input)
	callID := ""
	if emit != nil {
		if correlation == nil {
			return nil, fmt.Errorf("genkit tool %q execution has no correlation state", def.Name)
		}
		var err error
		callID, err = correlation.begin(def.Name, args)
		if err != nil {
			return nil, err
		}
		emit(ai.Event{Kind: ai.EventToolUse, Tool: def.Name, Input: args, ToolCallID: callID, Model: p.cfg.Model.Name})
	}

	if def.NeedsApproval() && !gkai.IsToolResumed(ctx) {
		if emit != nil {
			emit(ai.Event{Kind: ai.EventPermission, Tool: def.Name, Input: args, ToolCallID: callID, Model: p.cfg.Model.Name})
		}
		if p.cfg.CanUseTool == nil {
			return nil, gkai.NewToolInterruptError(map[string]any{"approvalRequired": true})
		}
		decision, err := p.cfg.CanUseTool(ctx, api.PermissionRequest{
			Tool:      def.Name,
			Input:     args,
			ToolUseID: callID,
			SessionID: p.cfg.SessionID,
		})
		if err != nil {
			return p.toolDenied(def.Name, callID, err.Error(), emit, correlation)
		}
		if !decision.Allow {
			msg := decision.Message
			if msg == "" {
				msg = "tool call denied"
			}
			return p.toolDenied(def.Name, callID, msg, emit, correlation)
		}
		if decision.UpdatedInput != nil {
			args = decision.UpdatedInput
		}
	}

	out, err := def.Handler(ctx, args)
	if err != nil {
		if emitErr := p.emitToolResult(ai.Event{Kind: ai.EventToolResult, Tool: def.Name, ToolCallID: callID, Success: false, Text: err.Error(), Model: p.cfg.Model.Name}, emit, correlation); emitErr != nil {
			return nil, emitErr
		}
		// Feed the error back as the tool result rather than aborting the turn.
		return map[string]any{"error": err.Error()}, nil
	}

	if err := p.emitToolResult(ai.Event{Kind: ai.EventToolResult, Tool: def.Name, ToolCallID: callID, Success: true, Text: toolOutputText(out), Model: p.cfg.Model.Name}, emit, correlation); err != nil {
		return nil, err
	}
	return out, nil
}

// toolDenied emits a failed EventToolResult and returns the deny reason to the
// model as the tool output (not an error, so the model can adapt).
func (p *Provider) toolDenied(name, callID, reason string, emit func(ai.Event), correlation *toolEventCorrelation) (any, error) {
	if err := p.emitToolResult(ai.Event{Kind: ai.EventToolResult, Tool: name, ToolCallID: callID, Success: false, Text: reason, Model: p.cfg.Model.Name}, emit, correlation); err != nil {
		return nil, err
	}
	return map[string]any{"denied": true, "reason": reason}, nil
}

func (p *Provider) emitToolResult(event ai.Event, emit func(ai.Event), correlation *toolEventCorrelation) error {
	if emit == nil {
		return nil
	}
	if err := correlation.finish(event.ToolCallID); err != nil {
		return err
	}
	emit(event)
	return nil
}

// toArgsMap normalizes genkit's decoded tool input into a JSON object map.
func toArgsMap(input any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	if m, ok := input.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// toolOutputText renders a handler's return value as the EventToolResult text.
func toolOutputText(out any) string {
	if out == nil {
		return ""
	}
	if s, ok := out.(string); ok {
		return s
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("%v", out)
	}
	return string(data)
}

// static assertion: the genkit provider advertises caller-tool support.
var _ api.ToolCapableProvider = (*Provider)(nil)

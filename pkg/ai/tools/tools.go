// Package tools is captain's home for the chat tool registry's data model and
// approval policy: the tool definition/info types, the per-request tool mode and
// preferences, the tool catalog DTO, and the approval-decision logic. It is
// genkit- and clicky-free — the genkit binding (registering these as model tools)
// and the clicky-RPC→tool mapping live in the consumer (clicky/aichat), which
// imports this package. Clicky-specific metadata (the originating verb/method/
// path) rides in the opaque Annotations map rather than as typed fields.
package tools

import (
	"context"
	"sort"

	"github.com/flanksource/captain/pkg/api"
)

// ToolMode controls how a tool is exposed for one request.
type ToolMode = api.ToolMode

const (
	ToolModeOn   = api.ToolModeOn
	ToolModeAsk  = api.ToolModeAsk
	ToolModeOff  = api.ToolModeOff
	ToolModeAuto = api.ToolModeAuto
)

// NormalizeToolMode canonicalizes a mode string. The bool is false for an
// unrecognized value.
func NormalizeToolMode(mode ToolMode) (ToolMode, bool) {
	return api.NormalizeToolMode(mode)
}

// DefaultPermissionMode resolves a mode to its canonical value, defaulting an
// unset/unknown value to Auto (defer to the approval policy).
func DefaultPermissionMode(mode ToolMode) ToolMode {
	if normalized, ok := NormalizeToolMode(mode); ok {
		return normalized
	}
	return ToolModeAuto
}

// ApprovalDecisionForMode maps a resolved mode to an approve/auto decision. The
// second bool is false only for Auto, which defers to the policy.
func ApprovalDecisionForMode(mode ToolMode) (require bool, handled bool) {
	switch DefaultPermissionMode(mode) {
	case ToolModeOn:
		return false, true
	case ToolModeAsk:
		return true, true
	case ToolModeOff:
		return false, true
	case ToolModeAuto:
		return false, false
	default:
		return false, false
	}
}

// ToolPreferences carries the clicky-ui tool preference payload. The UI sends
// "on", "ask", "off", or "auto".
type ToolPreferences = api.ToolPreferences

// ToolInfo is the concrete tool being considered for approval and preference
// resolution. Clicky-RPC specifics (verb/method/path/operation) live in
// Annotations, not as typed fields.
type ToolInfo struct {
	Name string
	// Group is the tool-group this tool belongs to. When non-empty the
	// preferences UI presents the group as one entry governing every member.
	Group             string
	Parent            string
	Icon              string
	DefaultPermission ToolMode
	Strict            *bool
	ReadOnlyHint      *bool
	DestructiveHint   *bool
	IdempotentHint    *bool
	// Annotations carries opaque caller metadata (e.g. clicky/verb, clicky/method,
	// clicky/path, clicky/operation) for policies that want the raw values.
	Annotations map[string]string
}

// Annotation returns the named annotation (empty when absent).
func (i ToolInfo) Annotation(key string) string {
	if i.Annotations == nil {
		return ""
	}
	return i.Annotations[key]
}

// ToolDefinition describes an app-owned tool registered alongside clicky RPC and
// MCP tools. Handlers should return JSON-serializable values.
type ToolDefinition struct {
	Name              string
	Description       string
	InputSchema       map[string]any
	Parent            string
	Icon              string
	DefaultPermission ToolMode
	Strict            *bool
	ReadOnlyHint      *bool
	DestructiveHint   *bool
	IdempotentHint    *bool
	// Group places this custom tool in a tool-group so the preferences UI presents
	// it under the group rather than individually.
	Group string
	// Annotations carries opaque caller metadata (see ToolInfo.Annotations).
	Annotations map[string]string
	Handler     func(context.Context, any) (any, error)
}

// ApprovalPolicy reports whether a tool call must be approved before it runs.
type ApprovalPolicy func(toolName string, input any) bool

// ToolApprovalPolicy is the metadata-aware approval hook; it takes precedence
// over ApprovalPolicy when both are configured.
type ToolApprovalPolicy func(tool ToolInfo, input any) bool

// ApprovalPredicate is the internal gate type shared by the resolvers.
type ApprovalPredicate func(tool ToolInfo, input any) bool

// ResolveApprovalPolicy picks the effective gate: an explicit tool policy wins,
// then a name-based policy, then an exact-name list, else nil (auto-approve).
func ResolveApprovalPolicy(toolPolicy ToolApprovalPolicy, policy ApprovalPolicy, names []string) ApprovalPredicate {
	if toolPolicy != nil {
		return ApprovalPredicate(toolPolicy)
	}
	if policy != nil {
		return func(tool ToolInfo, input any) bool {
			return policy(tool.Name, input)
		}
	}
	return RequireApprovalFor(names)
}

// RequireApprovalFor builds a predicate requiring approval for exactly the named
// tools. An empty list yields nil (auto-approve everything).
func RequireApprovalFor(names []string) ApprovalPredicate {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(tool ToolInfo, _ any) bool {
		return set[tool.Name]
	}
}

type toolRuntimeConfig struct {
	preferences     ToolPreferences
	defaultApproval ApprovalPredicate
}

type toolRuntimeContextKey struct{}

// WithRuntime stores the per-request tool preferences + default approval gate on
// the context so a tool handler can resolve its approval decision.
func WithRuntime(ctx context.Context, prefs ToolPreferences, defaultApproval ApprovalPredicate) context.Context {
	return context.WithValue(ctx, toolRuntimeContextKey{}, toolRuntimeConfig{preferences: prefs, defaultApproval: defaultApproval})
}

func runtimeConfig(ctx context.Context) (toolRuntimeConfig, bool) {
	cfg, ok := ctx.Value(toolRuntimeContextKey{}).(toolRuntimeConfig)
	return cfg, ok
}

// ShouldRequireApproval resolves whether a tool call must be approved, honoring
// (in order) the per-request preference, the tool's default permission, the
// runtime's default approval gate, and finally the supplied fallback.
func ShouldRequireApproval(ctx context.Context, fallback ApprovalPredicate, tool ToolInfo, input any) bool {
	if ctx != nil {
		if cfg, ok := runtimeConfig(ctx); ok {
			if mode, ok := EffectivePreference(cfg.preferences, tool); ok {
				if decision, handled := ApprovalDecisionForMode(mode); handled {
					return decision
				}
			}
			if decision, handled := ApprovalDecisionForMode(DefaultPermissionMode(tool.DefaultPermission)); handled {
				return decision
			}
			if cfg.defaultApproval != nil {
				return cfg.defaultApproval(tool, input)
			}
		}
	}
	if decision, handled := ApprovalDecisionForMode(DefaultPermissionMode(tool.DefaultPermission)); handled {
		return decision
	}
	if fallback == nil {
		return false
	}
	return fallback(tool, input)
}

// EffectivePreference resolves the ToolMode for a tool: an exact tool-name
// preference wins, else the tool's group preference; ungrouped tools resolve by
// their own name.
func EffectivePreference(prefs ToolPreferences, info ToolInfo) (ToolMode, bool) {
	if mode, ok := NormalizedPreference(prefs, info.Name); ok {
		return mode, true
	}
	if info.Group != "" {
		return NormalizedPreference(prefs, info.Group)
	}
	return "", false
}

// NormalizedPreference looks up and normalizes a preference by key.
func NormalizedPreference(prefs ToolPreferences, name string) (ToolMode, bool) {
	if len(prefs) == 0 {
		return "", false
	}
	mode, ok := prefs[name]
	if !ok {
		return "", false
	}
	return NormalizeToolMode(mode)
}

// ToolEntry is one row in the tool-preferences UI: a single ungrouped tool, or a
// collapsed group listing its member names.
type ToolEntry struct {
	Key   string   `json:"key"`
	Group string   `json:"group,omitempty"`
	Tools []string `json:"tools"`
	Mode  ToolMode `json:"mode,omitempty"`
}

// ListToolEntries collapses grouped tools into one entry per group and leaves
// ungrouped tools individual, sorted by Key. prefs (may be nil) annotates Mode.
func ListToolEntries(infos []ToolInfo, prefs ToolPreferences) []ToolEntry {
	groups := map[string][]string{}
	var entries []ToolEntry
	for _, info := range infos {
		if g := info.Group; g != "" {
			groups[g] = append(groups[g], info.Name)
			continue
		}
		entry := ToolEntry{Key: info.Name, Tools: []string{info.Name}}
		if mode, ok := NormalizedPreference(prefs, info.Name); ok {
			entry.Mode = mode
		}
		entries = append(entries, entry)
	}
	for group, members := range groups {
		sort.Strings(members)
		entry := ToolEntry{Key: group, Group: group, Tools: members}
		if mode, ok := NormalizedPreference(prefs, group); ok {
			entry.Mode = mode
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

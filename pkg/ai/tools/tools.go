// Package tools is captain's chat tool registry runtime: the approval gate, the
// per-request preference resolution, and the definition resolver every provider
// runs so the API and agent runtimes cannot disagree about the visible tool set.
//
// The data model it operates on — ToolPolicy, ToolInfo, ToolDefinition and the
// catalog DTOs — is owned by pkg/api and aliased here. There is one permission
// vocabulary and one set of rules for it; this package holds none of them.
//
// It is genkit- and clicky-free: the genkit binding (registering these as model
// tools) and the clicky-RPC→tool mapping live in the consumer (clicky/aichat),
// which imports this package. Clicky-specific metadata (the originating
// verb/method/path) rides in the opaque Annotations map, not as typed fields.
package tools

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// The tool permission vocabulary is owned by pkg/api. These are aliases so a
// consumer already importing this package does not need both imports; there is
// exactly one vocabulary and one set of rules behind them.
type (
	ToolPolicy       = api.ToolPolicy
	ToolPreferences  = api.ToolPreferences
	ToolInfo         = api.ToolInfo
	ToolDefinition   = api.ToolDefinition
	ToolCatalog      = api.ToolCatalog
	ToolCatalogEntry = api.ToolCatalogEntry
	ToolMatch        = api.ToolMatch
	PermissionRule   = api.PermissionRule
	PermissionPolicy = api.PermissionPolicy
)

const (
	ToolPolicyAuto  = api.ToolPolicyAuto
	ToolPolicyAsk   = api.ToolPolicyAsk
	ToolPolicyAllow = api.ToolPolicyAllow
	ToolPolicyDeny  = api.ToolPolicyDeny
)

// Re-exported so catalog builders can stay on this package's import.
var (
	CustomCatalogEntry  = api.CustomCatalogEntry
	ApplyToolMetadata   = api.ApplyToolMetadata
	PreferenceKey       = api.PreferenceKey
	ObjectSchema        = api.ObjectSchema
	StringMetadata      = api.StringMetadata
	BoolMetadata        = api.BoolMetadata
	DefaultToolPolicy   = api.DefaultToolPolicy
	NormalizeToolPolicy = api.NormalizeToolPolicy
	ParseToolPolicy     = api.ParseToolPolicy
)

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
			if preferred, ok := EffectivePreference(cfg.preferences, tool); ok {
				if decision, handled := preferred.ApprovalDecision(); handled {
					return decision
				}
			}
			if decision, handled := tool.DefaultPermission.ApprovalDecision(); handled {
				return decision
			}
			if cfg.defaultApproval != nil {
				return cfg.defaultApproval(tool, input)
			}
		}
	}
	if decision, handled := tool.DefaultPermission.ApprovalDecision(); handled {
		return decision
	}
	if fallback == nil {
		return false
	}
	return fallback(tool, input)
}

// EffectivePreference resolves the policy for a tool: an exact tool-name
// preference wins, else the tool's group preference; ungrouped tools resolve by
// their own name.
func EffectivePreference(prefs ToolPreferences, info ToolInfo) (ToolPolicy, bool) {
	if policy, ok := NormalizedPreference(prefs, info.Name); ok {
		return policy, true
	}
	if info.Group != "" {
		return NormalizedPreference(prefs, info.Group)
	}
	return "", false
}

// NormalizedPreference looks up and normalizes a preference by key.
func NormalizedPreference(prefs ToolPreferences, name string) (ToolPolicy, bool) {
	if len(prefs) == 0 {
		return "", false
	}
	policy, ok := prefs[name]
	if !ok {
		return "", false
	}
	return NormalizeToolPolicy(string(policy))
}

// ResolveOptions carries the two shapes a caller may express tool authority in.
//
// Both are accepted and evaluated through ONE ordered list, so a spec that sets
// each cannot get two different answers for the same tool. Preferences are
// lowered first and the policy appended after, which makes an explicit rule beat
// an inherited preference — the layering the whole design turns on.
type ResolveOptions struct {
	// Preferences is the legacy flat tool→policy map, keyed by tool name or
	// group. Lowered through api.FromPreferences rather than matched separately.
	Preferences ToolPreferences
	// Policy is the ordered, last-match-wins rule list.
	Policy PermissionPolicy
}

// EffectivePolicy is the single ordered list these options resolve through.
func (o ResolveOptions) EffectivePolicy() PermissionPolicy {
	return api.FromPreferences(o.Preferences).Append(o.Policy)
}

// toolInfo projects a definition onto the subject a rule matches against. It
// carries the full identity — parent, hints and the clicky annotations — because
// a rule may select on any of them; passing only name and group is what limited
// matching to exact strings before.
func toolInfo(definition api.ToolDefinition) ToolInfo {
	return ToolInfo{
		Name:              definition.Name,
		Group:             definition.Group,
		Parent:            definition.Parent,
		Icon:              definition.Icon,
		DefaultPermission: definition.DefaultPermission,
		Strict:            definition.Strict,
		ReadOnlyHint:      definition.ReadOnlyHint,
		DestructiveHint:   definition.DestructiveHint,
		IdempotentHint:    definition.IdempotentHint,
		Annotations:       definition.Annotations,
	}
}

// ResolveDefinitions validates caller tools, resolves each against the ordered
// permission policy, omits denied tools, and writes the effective permission onto
// a copy of each selected definition. Every provider uses this function so the
// API and agent runtimes cannot disagree about the visible tool set.
//
// A denied tool is dropped rather than marked: for a caller tool captain owns the
// MCP server, so omission IS the enforcement — which is why a deny is honoured
// even on backends whose own CLI has no tool filter.
func ResolveDefinitions(definitions []api.ToolDefinition, opts ResolveOptions) ([]api.ToolDefinition, error) {
	if err := opts.Preferences.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Policy.Validate(); err != nil {
		return nil, err
	}
	effective := opts.EffectivePolicy()
	selected := make([]api.ToolDefinition, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			return nil, fmt.Errorf("caller tool name cannot be empty")
		}
		if !validCallerToolName(definition.Name) {
			return nil, fmt.Errorf("caller tool name %q contains unsupported characters", definition.Name)
		}
		if _, ok := seen[definition.Name]; ok {
			return nil, fmt.Errorf("duplicate caller tool %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		if definition.Handler == nil {
			return nil, fmt.Errorf("caller tool %q has no handler", definition.Name)
		}
		policy := ToolPolicyAuto
		if definition.DefaultPermission != "" {
			var ok bool
			policy, ok = NormalizeToolPolicy(string(definition.DefaultPermission))
			if !ok {
				return nil, fmt.Errorf("tool %q has invalid default permission %q", definition.Name, definition.DefaultPermission)
			}
		}
		if resolved, matched := effective.Resolve(toolInfo(definition)); matched && resolved != ToolPolicyAuto {
			policy = resolved
		}
		if policy == ToolPolicyDeny {
			continue
		}
		if policy == ToolPolicyAuto {
			if definition.ReadOnlyHint != nil && *definition.ReadOnlyHint &&
				definition.DestructiveHint != nil && !*definition.DestructiveHint {
				policy = ToolPolicyAllow
			} else {
				policy = ToolPolicyAsk
			}
		}
		definition.DefaultPermission = policy
		selected = append(selected, definition)
	}
	return selected, nil
}

func validCallerToolName(name string) bool {
	for _, value := range name {
		if value >= 'a' && value <= 'z' ||
			value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' ||
			value == '-' ||
			value == '_' {
			continue
		}
		return false
	}
	return true
}

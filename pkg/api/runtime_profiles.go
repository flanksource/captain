package api

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// RuntimePresetSpec is the complete reusable task specification. The named
// type is retained while profile APIs are deprecated so existing callers keep
// compiling until the final removal release.
type RuntimePresetSpec Spec

var ErrRuntimePresetNestingUnsupported = errors.New("nested runtime presets are not supported yet")

const RuntimeProfileDeprecationWarning = "runtimeProfile is deprecated and ignored; use presets"

type RuntimePreset struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Scope       SpecLayerScope    `json:"scope" yaml:"scope"`
	Spec        RuntimePresetSpec `json:"spec" yaml:"spec"`
	Presets     []string          `json:"presets,omitempty" yaml:"presets,omitempty"`
}

type RuntimeProfile struct {
	ID          string   `json:"id" yaml:"id"`
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Spec        Spec     `json:"spec" yaml:"spec"`
	Presets     []string `json:"presets,omitempty" yaml:"presets,omitempty"`
}

type RuntimeProfileResolveRequest struct {
	Profile RuntimeProfile  `json:"profile"`
	Presets []RuntimePreset `json:"presets"`
}

type RuntimePresetResolveRequest struct {
	Selected []string        `json:"selected"`
	Presets  []RuntimePreset `json:"presets"`
}

type RuntimeProfileResolveResponse struct {
	Resolved          ResolvedSpec          `json:"resolved"`
	Tools             []ToolCatalogEntry    `json:"tools"`
	Permissions       map[string]ToolPolicy `json:"permissions"`
	PermissionSupport map[string]Support    `json:"permissionSupport"`
	EffectivePolicy   PermissionPolicy      `json:"effectivePolicy"`
}

type RuntimePresetResolveResponse = RuntimeProfileResolveResponse

// RuntimePresetLayers materializes a flat ordered preset selection. Nested
// references are persisted by the API but deliberately fail until the
// deferred resolver tracked by Gavel TODO 3178c771 is implemented.
func RuntimePresetLayers(request RuntimePresetResolveRequest) ([]SpecLayer, error) {
	index, err := indexRuntimePresets(request.Presets)
	if err != nil {
		return nil, err
	}
	layers := make([]SpecLayer, 0, len(request.Selected))
	selected := make(map[string]struct{}, len(request.Selected))
	for _, ref := range request.Selected {
		preset, err := index.lookup("runtime preset selection", ref)
		if err != nil {
			return nil, err
		}
		if len(preset.Presets) > 0 {
			return nil, fmt.Errorf("runtime preset %q declares nested presets, which are not supported yet: %w", preset.Name, ErrRuntimePresetNestingUnsupported)
		}
		if _, repeated := selected[preset.ID]; repeated {
			return nil, fmt.Errorf("runtime preset selection repeats preset %q", ref)
		}
		selected[preset.ID] = struct{}{}
		layers = append(layers, SpecLayer{
			ID: preset.ID, Source: SpecLayerSourcePreset,
			Name: preset.Name, Scope: preset.Scope, Spec: preset.Spec.ToSpec(),
		})
	}
	if err := ValidateSpecLayers(layers...); err != nil {
		return nil, err
	}
	return layers, nil
}

func ResolveRuntimePresets(request RuntimePresetResolveRequest) (ResolvedSpec, error) {
	layers, err := RuntimePresetLayers(request)
	if err != nil {
		return ResolvedSpec{}, err
	}
	resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: layers})
	if err != nil {
		return ResolvedSpec{}, fmt.Errorf("resolve runtime presets: %w", err)
	}
	return resolved, nil
}

// RuntimeProfileLayers materializes selected presets and the profile spec in
// reference order. Runtime resolution waits until the host adds its other layers.
func RuntimeProfileLayers(request RuntimeProfileResolveRequest) ([]SpecLayer, error) {
	if err := validateRuntimeProfile(request.Profile); err != nil {
		return nil, err
	}
	layers, err := RuntimePresetLayers(RuntimePresetResolveRequest{Selected: request.Profile.Presets, Presets: request.Presets})
	if err != nil {
		return nil, fmt.Errorf("runtime profile %q: %w", request.Profile.Name, err)
	}
	layers = append(layers, SpecLayer{
		ID: request.Profile.ID + ":spec", Source: SpecLayerSourceProfile,
		Name: request.Profile.Name + " run spec", Scope: SpecLayerSurface, Spec: request.Profile.Spec,
	})
	if err := ValidateSpecLayers(layers...); err != nil {
		return nil, err
	}
	return layers, nil
}

// ResolveRuntimeProfile resolves and validates a profile in isolation for preview.
// Hosts composing a run use RuntimeProfileLayers before adding their other layers.
func ResolveRuntimeProfile(request RuntimeProfileResolveRequest) (ResolvedSpec, error) {
	layers, err := RuntimeProfileLayers(request)
	if err != nil {
		return ResolvedSpec{}, err
	}
	resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: layers})
	if err != nil {
		return ResolvedSpec{}, fmt.Errorf("resolve runtime profile %q: %w", request.Profile.Name, err)
	}
	return resolved, nil
}

type runtimePresetIndex struct {
	byID   map[string]RuntimePreset
	byName map[string][]RuntimePreset
}

func indexRuntimePresets(presets []RuntimePreset) (runtimePresetIndex, error) {
	index := runtimePresetIndex{
		byID:   make(map[string]RuntimePreset, len(presets)),
		byName: make(map[string][]RuntimePreset, len(presets)),
	}
	for _, preset := range presets {
		if err := validateRuntimePreset(preset); err != nil {
			return runtimePresetIndex{}, err
		}
		if _, exists := index.byID[preset.ID]; exists {
			return runtimePresetIndex{}, fmt.Errorf("runtime preset id %q is duplicated", preset.ID)
		}
		index.byID[preset.ID] = preset
		name := strings.ToLower(strings.TrimSpace(preset.Name))
		index.byName[name] = append(index.byName[name], preset)
	}
	return index, nil
}

func (i runtimePresetIndex) lookup(profile, ref string) (RuntimePreset, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RuntimePreset{}, fmt.Errorf("runtime profile %q contains an empty preset reference", profile)
	}
	if preset, ok := i.byID[ref]; ok {
		return preset, nil
	}
	switch named := i.byName[strings.ToLower(ref)]; len(named) {
	case 0:
		return RuntimePreset{}, fmt.Errorf("runtime profile %q references missing preset %q", profile, ref)
	case 1:
		return named[0], nil
	default:
		return RuntimePreset{}, fmt.Errorf("runtime profile %q references preset %q by name, which matches %d presets", profile, ref, len(named))
	}
}

func (s RuntimePresetSpec) ToSpec() Spec {
	spec := Spec(s)
	spec.Explicit = spec.Explicit.Clone()
	return spec
}

func validateRuntimeProfile(profile RuntimeProfile) error {
	if strings.TrimSpace(profile.ID) == "" {
		return fmt.Errorf("runtime profile id is required")
	}
	if strings.TrimSpace(profile.Name) == "" {
		return fmt.Errorf("runtime profile name is required")
	}
	return nil
}

// ValidateRuntimePreset checks a preset's identity, scope, and every reusable
// spec fragment it carries, so stores reject an unusable preset at write time
// rather than at the first resolution that references it.
func ValidateRuntimePreset(preset RuntimePreset) error { return validateRuntimePreset(preset) }

func validateRuntimePreset(preset RuntimePreset) error {
	preset.ID = strings.TrimSpace(preset.ID)
	if preset.ID == "" {
		return fmt.Errorf("runtime preset id is required")
	}
	if strings.TrimSpace(preset.Name) == "" {
		return fmt.Errorf("runtime preset %q name is required", preset.ID)
	}
	for index, ref := range preset.Presets {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("runtime preset %q preset reference %d is blank", preset.Name, index)
		}
	}
	return ValidateSpecLayers(SpecLayer{ID: preset.ID, Name: preset.Name, Scope: preset.Scope,
		Source: SpecLayerSourcePreset, Spec: preset.Spec.ToSpec()})
}

// UnsupportedPermissions reports settings the selected runtime cannot honour.
// Callers choose whether these capability diagnostics refuse a run or warn.
func UnsupportedPermissions(spec Spec) []string {
	if !hasResolvedPermissionSettings(spec) {
		return nil
	}
	provider, mode, err := spec.Runtime()
	if err != nil {
		return []string{fmt.Sprintf("permission settings require a resolved runtime: %v", err)}
	}
	runtime := RuntimeOf(provider, mode)
	caps := PermissionCapabilitiesFor(runtime)
	var warnings []string
	add := func(err error) {
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	if posture := spec.Permissions.Mode; posture != "" && !caps.ModeSupport(posture).Honoured() {
		warnings = append(warnings, fmt.Sprintf("permissions.mode %q is not available for %s", posture, runtime))
	}
	// The same translation RequireToolPolicySupport checks: the runtime's own
	// tool names, with alias-only allows already inert on an unfiltered runtime.
	tools, ignored := spec.Permissions.Tools.ForRuntime(provider, mode)
	for _, name := range sortedKeys(tools) {
		add(requireResolvedToolPolicy(caps, runtime, ProvenanceAgent, tools[name]))
	}
	warnings = append(warnings, ignored...)
	for _, name := range sortedKeys(spec.ToolPreferences) {
		add(requireResolvedToolPolicy(caps, runtime, ProvenanceCaller, spec.ToolPreferences[name]))
	}
	for _, rule := range spec.ToolPolicy {
		add(requireResolvedToolPolicy(caps, runtime, ProvenanceCaller, rule.Policy))
	}
	if spec.Permissions.MCP.Disabled {
		add(requireResolvedResource(caps, runtime, ResourceKindMCP, ResourceDisabled))
	}
	if len(spec.Permissions.MCP.Servers) > 0 {
		add(requireResolvedResource(caps, runtime, ResourceKindMCP, ResourceEnabled))
	}
	for _, name := range sortedKeys(spec.Permissions.MCP.Modes) {
		add(requireResolvedResource(caps, runtime, ResourceKindMCP, spec.Permissions.MCP.Modes[name]))
	}
	for _, name := range sortedKeys(spec.Permissions.Skills) {
		add(requireResolvedResource(caps, runtime, ResourceKindSkills, spec.Permissions.Skills[name]))
		if spec.Permissions.Skills[name] == ResourceDisabled && slices.Contains(spec.Memory.Skills, name) &&
			caps.ResourceSupport(ResourceKindSkills, ResourceEnabled).Honoured() {
			warnings = append(warnings, fmt.Sprintf("permissions.skills %q is disabled but memory.skills still loads it for %s", name, runtime))
		}
	}
	for _, name := range sortedKeys(spec.Permissions.Plugins) {
		add(requireResolvedResource(caps, runtime, ResourceKindPlugins, spec.Permissions.Plugins[name]))
	}
	return warnings
}

func hasResolvedPermissionSettings(spec Spec) bool {
	permissions := spec.Permissions
	return permissions.Mode != "" || len(permissions.Tools) > 0 || permissions.MCP.Disabled ||
		len(permissions.MCP.Servers) > 0 || len(permissions.MCP.Modes) > 0 ||
		len(permissions.Skills) > 0 || len(permissions.Plugins) > 0 ||
		len(spec.ToolPreferences) > 0 || len(spec.ToolPolicy) > 0
}

// ValidateResolvedSandbox refuses sandbox isolation unsupported by the runtime.
func ValidateResolvedSandbox(spec Spec) error {
	if spec.Sandbox == nil {
		return nil
	}
	if err := spec.Sandbox.Validate(); err != nil {
		return err
	}
	provider, mode, err := spec.Runtime()
	if err != nil {
		return fmt.Errorf("sandbox settings require a resolved runtime: %w", err)
	}
	capabilities := RuntimeSandboxCapabilitiesFor(provider, mode)
	if !containsSandboxMode(capabilities.Modes, spec.Sandbox.Mode) {
		return fmt.Errorf("sandbox mode %q is not available for %s", spec.Sandbox.Mode, RuntimeOf(provider, mode))
	}
	if mode != ModeAPI {
		switch provider {
		case Anthropic:
			_, err = TranslateClaudeSandbox(RuntimeOf(provider, mode), *spec.Sandbox)
		case OpenAI:
			_, err = TranslateCodexSandbox(RuntimeOf(provider, mode), spec.Sandbox, spec.Permissions.Mode)
		}
	}
	if err != nil {
		return err
	}
	return nil
}

func requireResolvedToolPolicy(
	caps PermissionCapabilities,
	runtime Runtime,
	provenance ToolProvenance,
	policy ToolPolicy,
) error {
	if policy == ToolPolicyAuto || caps.ToolPolicySupport(provenance, policy).Kind != SupportUnsupported {
		return nil
	}
	return fmt.Errorf("%s-tool policy %q is not available for %s", provenance, policy, runtime)
}

func requireResolvedResource(
	caps PermissionCapabilities,
	runtime Runtime,
	kind ResourceKind,
	mode ResourceMode,
) error {
	if caps.ResourceSupport(kind, mode).Honoured() {
		return nil
	}
	return fmt.Errorf("resource policy %s=%s is not available for %s", kind, mode, runtime)
}

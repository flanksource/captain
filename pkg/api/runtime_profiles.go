package api

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
)

// RuntimePresetSpec is the reusable subset of Spec. Prompt bodies, messages,
// verification, session identity, CLI flags, and checkout locations remain
// task-specific profile fields and cannot be represented here.
type RuntimePresetSpec struct {
	Model           `json:",inline" yaml:",inline"`
	Budget          Budget              `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory          Memory              `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions     Permissions         `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	ToolPreferences ToolPreferences     `json:"toolPreferences,omitempty" yaml:"toolPreferences,omitempty"`
	ToolPolicy      PermissionPolicy    `json:"toolPolicy,omitempty" yaml:"toolPolicy,omitempty"`
	Setup           *RuntimePresetSetup `json:"setup,omitempty" yaml:"setup,omitempty"`
	Sandbox         *SandboxRef         `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
}

// RuntimePresetSetup contains reusable environment and checkout behaviour.
// It deliberately has no cwd, base directory, repository, ref, or worktree path.
type RuntimePresetSetup struct {
	EnvVars  []types.EnvVar         `json:"envVars,omitempty" yaml:"envVars,omitempty"`
	Checkout *RuntimePresetCheckout `json:"checkout,omitempty" yaml:"checkout,omitempty"`
}

type RuntimePresetCheckout struct {
	Mode     shell.CheckoutMode     `json:"mode,omitempty" yaml:"mode,omitempty"`
	Depth    *int                   `json:"depth,omitempty" yaml:"depth,omitempty"`
	Worktree *RuntimePresetWorktree `json:"worktree,omitempty" yaml:"worktree,omitempty"`
}

type RuntimePresetWorktree struct {
	Mode        shell.WorktreeMode `json:"mode,omitempty" yaml:"mode,omitempty"`
	Keep        bool               `json:"keep,omitempty" yaml:"keep,omitempty"`
	Uncommitted shell.CloneMode    `json:"uncommitted,omitempty" yaml:"uncommitted,omitempty"`
	Ignored     shell.CloneMode    `json:"ignored,omitempty" yaml:"ignored,omitempty"`
}

type RuntimePreset struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Scope       SpecLayerScope    `json:"scope" yaml:"scope"`
	Spec        RuntimePresetSpec `json:"spec" yaml:"spec"`
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

type RuntimeProfileResolveResponse struct {
	Resolved          ResolvedSpec          `json:"resolved"`
	Tools             []ToolCatalogEntry    `json:"tools"`
	Permissions       map[string]ToolPolicy `json:"permissions"`
	PermissionSupport map[string]Support    `json:"permissionSupport"`
	EffectivePolicy   PermissionPolicy      `json:"effectivePolicy"`
}

// RuntimeProfileLayers materializes selected presets and the profile spec in
// reference order. Runtime resolution waits until the host adds its other layers.
func RuntimeProfileLayers(request RuntimeProfileResolveRequest) ([]SpecLayer, error) {
	if err := validateRuntimeProfile(request.Profile); err != nil {
		return nil, err
	}
	index, err := indexRuntimePresets(request.Presets)
	if err != nil {
		return nil, err
	}

	layers := make([]SpecLayer, 0, len(request.Profile.Presets)+1)
	selected := make(map[string]struct{}, len(request.Profile.Presets))
	for _, ref := range request.Profile.Presets {
		preset, err := index.lookup(request.Profile.Name, ref)
		if err != nil {
			return nil, err
		}
		if _, repeated := selected[preset.ID]; repeated {
			return nil, fmt.Errorf("runtime profile %q repeats preset %q", request.Profile.Name, ref)
		}
		selected[preset.ID] = struct{}{}
		layers = append(layers, SpecLayer{
			ID: preset.ID, Source: SpecLayerSourcePreset,
			Name: preset.Name, Scope: preset.Scope, Spec: preset.Spec.ToSpec(),
		})
	}
	return append(layers, SpecLayer{
		ID: request.Profile.ID + ":spec", Source: SpecLayerSourceProfile,
		Name: request.Profile.Name + " run spec", Scope: SpecLayerSurface, Spec: request.Profile.Spec,
	}), nil
}

// ResolveRuntimeProfile resolves and validates a profile in isolation for preview.
// Hosts composing a run use RuntimeProfileLayers before adding their other layers.
func ResolveRuntimeProfile(request RuntimeProfileResolveRequest) (ResolvedSpec, error) {
	layers, err := RuntimeProfileLayers(request)
	if err != nil {
		return ResolvedSpec{}, err
	}
	resolved, err := ResolveSpecLayers(layers...)
	if err != nil {
		return ResolvedSpec{}, fmt.Errorf("resolve runtime profile %q: %w", request.Profile.Name, err)
	}
	// The layers carry an authored model (name plus mode); the validators below
	// need the resolved adapter, which no longer survives serialization. Derive it
	// here rather than letting them reject their own input.
	if strings.TrimSpace(resolved.Spec.Name) != "" {
		model, modelErr := ResolveModel(resolved.Spec.Model)
		if modelErr != nil {
			return ResolvedSpec{}, fmt.Errorf("resolve runtime profile %q model: %w", request.Profile.Name, modelErr)
		}
		resolved.Spec.Model = model
	}
	if err := validateResolvedSandbox(resolved.Spec); err != nil {
		return ResolvedSpec{}, err
	}
	if err := validateResolvedPermissions(resolved.Spec); err != nil {
		return ResolvedSpec{}, err
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
	return Spec{
		Model: s.Model, Budget: s.Budget, Memory: s.Memory,
		Permissions: s.Permissions, ToolPreferences: s.ToolPreferences,
		ToolPolicy: s.ToolPolicy, Setup: s.Setup.toSetup(), Sandbox: s.Sandbox,
	}
}

func (s *RuntimePresetSetup) toSetup() *shell.Setup {
	if s == nil {
		return nil
	}
	setup := &shell.Setup{EnvVars: append([]types.EnvVar(nil), s.EnvVars...)}
	if s.Checkout != nil {
		setup.Checkout = &shell.Checkout{Mode: s.Checkout.Mode, Depth: s.Checkout.Depth}
		if s.Checkout.Worktree != nil {
			setup.Checkout.Worktree = &shell.Worktree{
				Mode: s.Checkout.Worktree.Mode, Keep: s.Checkout.Worktree.Keep,
				Uncommitted: s.Checkout.Worktree.Uncommitted, Ignored: s.Checkout.Worktree.Ignored,
			}
		}
	}
	return setup
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
	if scopeRank(preset.Scope) < 0 {
		return fmt.Errorf("runtime preset %q has invalid scope %q", preset.ID, preset.Scope)
	}
	spec := preset.Spec.ToSpec()
	if !IsEmpty(spec.Model) {
		if err := spec.Model.Validate(); err != nil {
			return fmt.Errorf("runtime preset %q model: %w", preset.ID, err)
		}
	}
	if err := spec.Budget.Validate(); err != nil {
		return fmt.Errorf("runtime preset %q budget: %w", preset.ID, err)
	}
	if err := spec.Permissions.Validate(); err != nil {
		return fmt.Errorf("runtime preset %q permissions: %w", preset.ID, err)
	}
	if err := spec.ToolPreferences.Validate(); err != nil {
		return fmt.Errorf("runtime preset %q: %w", preset.ID, err)
	}
	if err := spec.ToolPolicy.Validate(); err != nil {
		return fmt.Errorf("runtime preset %q: %w", preset.ID, err)
	}
	if spec.Sandbox != nil {
		if err := spec.Sandbox.Validate(); err != nil {
			return fmt.Errorf("runtime preset %q sandbox: %w", preset.ID, err)
		}
	}
	return nil
}

func validateResolvedPermissions(spec Spec) error {
	if !hasResolvedPermissionSettings(spec) {
		return nil
	}
	provider, mode, err := spec.Runtime()
	if err != nil {
		return fmt.Errorf("permission settings require a resolved runtime: %w", err)
	}
	runtime := RuntimeOf(provider, mode)
	caps := PermissionCapabilitiesFor(runtime)
	if posture := spec.Permissions.Mode; posture != "" && !caps.ModeSupport(posture).Honoured() {
		return fmt.Errorf("permissions.mode %q is not available for %s", posture, runtime)
	}
	for _, policy := range spec.Permissions.Tools {
		if err := requireResolvedToolPolicy(caps, runtime, ProvenanceAgent, policy); err != nil {
			return err
		}
	}
	for _, policy := range spec.ToolPreferences {
		if err := requireResolvedToolPolicy(caps, runtime, ProvenanceCaller, policy); err != nil {
			return err
		}
	}
	for _, rule := range spec.ToolPolicy {
		if err := requireResolvedToolPolicy(caps, runtime, ProvenanceCaller, rule.Policy); err != nil {
			return err
		}
	}
	if spec.Permissions.MCP.Disabled {
		if err := requireResolvedResource(caps, runtime, ResourceKindMCP, ResourceDisabled); err != nil {
			return err
		}
	}
	if len(spec.Permissions.MCP.Servers) > 0 {
		if err := requireResolvedResource(caps, runtime, ResourceKindMCP, ResourceEnabled); err != nil {
			return err
		}
	}
	for _, mode := range spec.Permissions.MCP.Modes {
		if err := requireResolvedResource(caps, runtime, ResourceKindMCP, mode); err != nil {
			return err
		}
	}
	for _, mode := range spec.Permissions.Skills {
		if err := requireResolvedResource(caps, runtime, ResourceKindSkills, mode); err != nil {
			return err
		}
	}
	for _, mode := range spec.Permissions.Plugins {
		if err := requireResolvedResource(caps, runtime, ResourceKindPlugins, mode); err != nil {
			return err
		}
	}
	return nil
}

func hasResolvedPermissionSettings(spec Spec) bool {
	permissions := spec.Permissions
	return permissions.Mode != "" || len(permissions.Tools) > 0 || permissions.MCP.Disabled ||
		len(permissions.MCP.Servers) > 0 || len(permissions.MCP.Modes) > 0 ||
		len(permissions.Skills) > 0 || len(permissions.Plugins) > 0 ||
		len(spec.ToolPreferences) > 0 || len(spec.ToolPolicy) > 0
}

func validateResolvedSandbox(spec Spec) error {
	if spec.Sandbox == nil {
		return nil
	}
	provider, mode, err := spec.Runtime()
	if err != nil {
		return fmt.Errorf("sandbox settings require a resolved runtime: %w", err)
	}
	capabilities := RuntimeSandboxCapabilitiesFor(provider, mode)
	if !containsSandboxMode(capabilities.Modes, spec.Sandbox.Mode) {
		return fmt.Errorf("sandbox mode %q is not available for %s", spec.Sandbox.Mode, RuntimeOf(provider, mode))
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

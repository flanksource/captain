package api

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// SavedDefaultsError identifies malformed injected configuration independently
// from authored request errors, so hosts can attribute it to their config owner.
type SavedDefaultsError struct {
	Source string
	Err    error
}

func (e *SavedDefaultsError) Error() string {
	return fmt.Sprintf("saved defaults %s: %v", e.Source, e.Err)
}

func (e *SavedDefaultsError) Unwrap() error { return e.Err }

func (composed *ComposedSpec) applyDefaults(options ResolveSpecOptions) error {
	if options.Saved == nil && !options.RequireModel {
		normalized, err := composed.normalize(options.Normalize)
		composed.recordContext(normalized)
		return err
	}
	saved := captainconfig.AIDefaults{}
	if options.Saved != nil {
		saved = *options.Saved
	}
	model := composed.Spec.modelWithPresence()
	defaults := aiflags.DefaultOptions{Model: model, Saved: saved, CatalogDefaults: options.Saved != nil, AllowUnknownModel: true}
	var normalized *SpecNormalization
	if options.Normalize != nil {
		defaults.Normalize = func(model Model) (Model, error) {
			composed.Spec.Model = model
			var err error
			normalized, err = composed.normalize(options.Normalize)
			return composed.Spec.modelWithPresence(), err
		}
	}
	defaulted, err := aiflags.ApplyDefaults(defaults)
	if err != nil {
		if invalid := saved.Validate(); invalid != nil {
			return &SavedDefaultsError{Source: "~/.captain.yaml ai", Err: invalid}
		}
		return err
	}
	composed.Spec.Model = defaulted.Model
	composed.recordDefaults(defaulted.Sources, normalized)
	for _, missing := range defaulted.Unconfigured {
		diagnostic := &aiflags.UnconfiguredError{Field: "mode", Model: missing.Model, Provider: missing.Provider}
		composed.Warnings = append(composed.Warnings, fmt.Sprintf("%s: %s; using the registry runtime default during the compatibility window", missing.Path, diagnostic.Error()))
	}
	composed.fillSavedSpec(saved)
	if options.Saved != nil && !fieldCovered(composed.Spec.Fields(), "/budget/maxTokens") {
		composed.Spec.Budget.MaxTokens = 4096
		composed.Provenance["/budget/maxTokens"] = FieldProvenance{Source: FieldSource{Kind: FieldSourceCatalog, Name: "Captain defaults", Key: "captain.defaults.maxTokens"}}
	}
	if options.RequireModel && strings.TrimSpace(composed.Spec.Name) == "" {
		return &aiflags.UnconfiguredError{Field: "model", Model: composed.Spec.Name, Provider: composed.Spec.Provider}
	}
	return nil
}

func (composed *ComposedSpec) recordDefaults(sources map[string]string, normalized *SpecNormalization) {
	for path, key := range sources {
		if strings.HasPrefix(key, "primary.") {
			continue
		}
		kind, name := FieldSourceSaved, "~/.captain.yaml"
		if strings.HasPrefix(key, "registry.") {
			kind, name = FieldSourceCatalog, "model registry"
		}
		composed.Provenance[path] = FieldProvenance{Source: FieldSource{Kind: kind, Name: name, Key: key}}
	}
	composed.recordContext(normalized)
	for path, key := range sources {
		if primary, inherited := strings.CutPrefix(key, "primary."); inherited {
			composed.Provenance[path] = composed.Provenance["/"+primary]
		}
	}
}

func (s Spec) modelWithPresence() Model {
	return applyModelPresence(s.Model, s.explicitFields(), "")
}

func applyModelPresence(model Model, fields FieldPresence, prefix string) Model {
	for path := range fields {
		local, belongs := strings.CutPrefix(path, prefix)
		if belongs && strings.Count(local, "/") == 1 && serializedField(reflect.ValueOf(model), []string{local[1:]}).IsValid() {
			model = model.WithExplicit(local)
		}
	}
	for i, fallback := range model.Fallbacks {
		model.Fallbacks[i] = applyModelPresence(fallback, fields, fmtFieldIndex(prefix+"/fallbacks", i))
	}
	return model
}

func (composed *ComposedSpec) fillSavedSpec(saved captainconfig.AIDefaults) {
	present := composed.Spec.Fields()
	fields := []struct {
		Path  string
		Key   string
		Value any
	}{
		{"/budget/cost", "budgetUSD", saved.BudgetUSD},
		{"/budget/maxTokens", "maxTokens", saved.MaxTokens},
		{"/budget/timeout", "timeout", saved.Timeout},
		{"/permissions/mcp/disabled", "noMCP", saved.NoMCP},
		{"/memory/skipHooks", "noHooks", saved.NoHooks},
		{"/memory/skipSkills", "noSkills", saved.NoSkills},
		{"/memory/skipUser", "noUser", saved.NoUser},
		{"/memory/skipProject", "noProject", saved.NoProject},
		{"/memory/skipMemory", "noMemory", saved.NoMemory},
	}
	for _, field := range fields {
		if !saved.Fields().Has("/"+field.Key) || fieldCovered(present, field.Path) || hasDescendant(present, field.Path) {
			continue
		}
		target := serializedField(reflect.ValueOf(&composed.Spec).Elem(), strings.Split(field.Path[1:], "/"))
		target.Set(reflect.ValueOf(field.Value))
		composed.Spec = composed.Spec.WithExplicit(field.Path)
		composed.Provenance[field.Path] = FieldProvenance{Source: FieldSource{Kind: FieldSourceSaved, Name: "~/.captain.yaml", Key: "ai." + field.Key}}
	}
}

func hasDescendant(fields FieldPresence, path string) bool {
	for field, present := range fields {
		if present && strings.HasPrefix(field, path+"/") {
			return true
		}
	}
	return false
}

func fieldCovered(fields FieldPresence, path string) bool {
	for path != "" {
		if fields.Has(path) {
			return true
		}
		index := strings.LastIndex(path, "/")
		if index < 0 {
			return false
		}
		path = path[:index]
	}
	return false
}

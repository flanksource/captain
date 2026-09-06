package api

import (
	"fmt"
	"reflect"
	"strings"
)

func (composed *ComposedSpec) normalize(normalize func(Spec) (SpecNormalization, error)) (*SpecNormalization, error) {
	if normalize == nil {
		return nil, nil
	}
	normalized, err := normalize(Spec{}.Merge(composed.Spec))
	if err != nil {
		return nil, err
	}
	if err := normalized.Spec.ValidateStructure(); err != nil {
		return nil, fmt.Errorf("normalized spec: %w", err)
	}
	for path, present := range normalized.Fields {
		if !present {
			continue
		}
		if !serializedField(reflect.ValueOf(normalized.Spec), strings.Split(strings.TrimPrefix(path, "/"), "/")).IsValid() {
			return nil, fmt.Errorf("normalization declares unknown field %q", path)
		}
		normalized.Spec = normalized.Spec.WithExplicit(path)
	}
	composed.Spec = normalized.Spec
	return &normalized, nil
}

func (composed *ComposedSpec) recordContext(normalized *SpecNormalization) {
	if normalized == nil {
		return
	}
	for path, present := range normalized.Fields {
		if !present {
			continue
		}
		provenance, exists := composed.Provenance[path]
		source := normalized.Source
		if source.Key == "" {
			source.Key = path
		}
		if exists {
			provenance.NormalizedBy = &source
		} else {
			provenance.Source = source
		}
		composed.Provenance[path] = provenance
	}
}

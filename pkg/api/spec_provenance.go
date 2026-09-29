package api

import (
	"reflect"
	"strconv"
	"strings"
)

// recordLayer attributes every field layer names to it, given merged — the
// composed spec with layer applied. Paths inside a key-merged list's elements
// are recorded at the element's merged position, and the attributions earlier
// layers made there move with their elements, so a stanza keeps its sources
// wherever the merge places it. Key names the path as the layer authored it.
func (composed *ComposedSpec) recordLayer(layer SpecLayer, merged Spec) {
	if composed.fieldLayers == nil {
		composed.fieldLayers = map[string]int{}
	}
	fields := sortedKeys(layer.Spec.Fields())
	explicit := layer.Spec.explicitFields()
	for _, path := range fields {
		tokens := splitPath(path)
		value := serializedField(reflect.ValueOf(layer.Spec), tokens)
		if keyed, replaces := keyedListReplaces(tokens, value, explicit[path]); !replacesField(value) || keyed && !replaces {
			continue
		}
		for previous := range composed.Provenance {
			if strings.HasPrefix(previous, path+"/") {
				delete(composed.Provenance, previous)
			}
		}
		for previous := range composed.fieldLayers {
			if strings.HasPrefix(previous, path+"/") {
				delete(composed.fieldLayers, previous)
			}
		}
	}
	composed.Provenance = rekeyed(composed.Provenance, composed.Spec, merged)
	composed.fieldLayers = rekeyed(composed.fieldLayers, composed.Spec, merged)
	authored := layer.Spec.withCommitPhases()
	for _, path := range fields {
		at := path
		if inKeyedElement(splitPath(path)) {
			at = keyedPath(path, authored, merged)
		}
		composed.fieldLayers[at] = len(composed.Trace) + 1
		composed.Provenance[at] = FieldProvenance{Source: FieldSource{Kind: FieldSourceLayer, Name: layer.Name, Key: path, LayerID: layer.ID}}
	}
}

func (composed *ComposedSpec) expandProvenance() error {
	raw := composed.Spec.Model
	nameSource := composed.Provenance["/model"]
	parts := strings.Split(raw.Name, ",")
	if count := len(parts) - 1; count > 0 {
		shifted := map[string]FieldProvenance{}
		for path, source := range composed.Provenance {
			if suffix, ok := strings.CutPrefix(path, "/fallbacks/"); ok {
				index, rest, _ := strings.Cut(suffix, "/")
				if i, err := strconv.Atoi(index); err == nil {
					shifted[fmtFieldIndex("/fallbacks", i+count)+"/"+rest] = source
					delete(composed.Provenance, path)
				}
			}
		}
		for path, source := range shifted {
			composed.Provenance[path] = source
		}
		for i, part := range parts[1:] {
			model, err := (Model{Name: strings.TrimSpace(part)}).Expand()
			if err != nil {
				return err
			}
			for path := range model.Fields() {
				composed.Provenance[fmtFieldIndex("/fallbacks", i)+path] = nameSource
			}
		}
		if _, found := composed.Provenance["/fallbacks"]; !found {
			composed.Provenance["/fallbacks"] = nameSource
		}
	}
	primary, err := (Model{Name: parts[0]}).Expand()
	if err != nil {
		return err
	}
	for _, field := range []string{"/mode", "/effort"} {
		if field == "/effort" && composed.fieldLayers[field] > composed.fieldLayers["/model"] {
			continue
		}
		if primary.Fields().Has(field) {
			composed.Provenance[field] = nameSource
		}
	}
	for i, fallback := range raw.Fallbacks {
		expanded, err := (Model{Name: fallback.Name}).Expand()
		if err != nil {
			return err
		}
		prefix := fmtFieldIndex("/fallbacks", i+len(parts)-1)
		for _, field := range []string{"/mode", "/effort"} {
			if expanded.Fields().Has(field) {
				composed.Provenance[prefix+field] = composed.Provenance[prefix+"/model"]
			}
		}
	}
	return nil
}

func (composed *ComposedSpec) expandModel() error {
	if err := composed.expandProvenance(); err != nil {
		return err
	}
	raw := composed.Spec.modelWithPresence()
	expanded, err := raw.Expand()
	if err != nil {
		return err
	}
	if composed.fieldLayers["/effort"] > composed.fieldLayers["/model"] {
		expanded.Effort = raw.Effort
		expanded = expanded.WithExplicit("/effort")
	}
	for i, fallback := range expanded.Fallbacks {
		expanded.Fallbacks[i], err = fallback.Expand()
		if err != nil {
			return err
		}
	}
	composed.Spec.Model = expanded
	for path := range composed.Spec.Explicit {
		if strings.HasPrefix(path, "/fallbacks/") {
			delete(composed.Spec.Explicit, path)
		}
	}
	if len(composed.Spec.Explicit) == 0 {
		composed.Spec.Explicit = nil
	}
	return nil
}

func (resolved *ResolvedSpec) recordNormalization(before Model) {
	for path := range (Spec{Model: resolved.Spec.Model}).Fields() {
		tokens := strings.Split(strings.TrimPrefix(path, "/"), "/")
		previous := serializedField(reflect.ValueOf(before), tokens)
		value := serializedField(reflect.ValueOf(resolved.Spec.Model), tokens)
		source, exists := resolved.Provenance[path]
		catalog := FieldSource{Kind: FieldSourceCatalog, Name: "model registry", Key: "registry.ResolveModel" + path}
		if !exists {
			source.Source = catalog
		} else if !previous.IsValid() || !value.IsValid() || !reflect.DeepEqual(previous.Interface(), value.Interface()) {
			source.NormalizedBy = &catalog
		}
		resolved.Provenance[path] = source
	}
}

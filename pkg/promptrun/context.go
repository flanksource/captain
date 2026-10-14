package promptrun

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptcontext"
)

// applyContext folds the caller's prompt.context hooks into the resolved spec
// before anything reads it, so the admitted spec, HookContext.Original and every
// verify retry carry the enriched prompt. Each applied hook is attributed on
// /prompt/user and announced on OnEvent. No sources leaves the spec untouched.
func applyContext(ctx context.Context, in *Input) error {
	if len(in.ContextSources) == 0 {
		return nil
	}
	hooks, err := promptcontext.Discover(in.ContextSources...)
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	applied, err := promptcontext.Apply(ctx, &in.Resolved.Spec, hooks)
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	if len(applied) == 0 {
		return nil
	}
	in.Resolved.Provenance = contextProvenance(in.Resolved.Provenance, applied)
	if in.OnEvent == nil {
		return nil
	}
	for _, hook := range applied {
		in.OnEvent(0, ai.Event{Kind: ai.EventSystem, Text: fmt.Sprintf("context hook %s (%s) applied", hook.Name, hook.Source)})
	}
	return nil
}

// contextProvenance attributes /prompt/user to the applied hooks: as its source
// when nothing else claimed the field, otherwise as what rewrote the authored
// value.
func contextProvenance(provenance map[string]api.FieldProvenance, applied promptcontext.Applied) map[string]api.FieldProvenance {
	sources := make([]string, 0, len(applied))
	for _, hook := range applied {
		sources = append(sources, hook.Source)
	}
	source := api.FieldSource{
		Kind: api.FieldSourceContext,
		Name: "context hook " + strings.Join(applied.Names(), ", "),
		Key:  strings.Join(sources, ", "),
	}
	out := maps.Clone(provenance)
	if out == nil {
		out = map[string]api.FieldProvenance{}
	}
	field, authored := out["/prompt/user"]
	if authored {
		field.NormalizedBy = &source
	} else {
		field.Source = source
	}
	out["/prompt/user"] = field
	return out
}

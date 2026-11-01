package hookaction_test

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/hookaction"
)

// fakeRunner records each call and answers from a table keyed by tool or path.
type fakeRunner struct {
	answers map[string]any
	calls   []call
}

type call struct {
	Name  string
	Input map[string]any
}

func (f *fakeRunner) RunTool(_ context.Context, name string, input map[string]any) (any, error) {
	f.calls = append(f.calls, call{Name: name, Input: input})
	answer, ok := f.answers[name]
	if !ok {
		return nil, fmt.Errorf("no answer for %s", name)
	}
	return answer, nil
}

func (f *fakeRunner) RunPrompt(ctx context.Context, path string, vars map[string]any, _ string) (any, error) {
	return f.RunTool(ctx, path, vars)
}

var _ = Describe("RenderWith", func() {
	vars := map[string]any{
		"output": map[string]any{"value": map[string]any{"product": "Annuity", "score": 0.9, "tags": []any{"a", "b"}}},
		"vars":   map[string]any{"file": "/issues/KEY-1.md"},
		"steps":  []any{map[string]any{"ok": true}},
	}

	It("binds a lone {{path}} to the native value and renders everything else as text", func() {
		rendered, err := hookaction.RenderWith(map[string]any{
			"values":  "{{output.value}}",
			"score":   "{{ output.value.score }}",
			"first":   "{{steps.0.ok}}",
			"file":    "{{vars.file}}",
			"label":   "product={{output.value.product}}",
			"encoded": "{{json output.value.tags}}",
			"nested":  map[string]any{"list": []any{"{{output.value.tags}}", 3}},
		}, vars)

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered).To(Equal(map[string]any{
			"values":  map[string]any{"product": "Annuity", "score": 0.9, "tags": []any{"a", "b"}},
			"score":   0.9,
			"first":   true,
			"file":    "/issues/KEY-1.md",
			"label":   "product=Annuity",
			"encoded": `["a","b"]`,
			"nested":  map[string]any{"list": []any{[]any{"a", "b"}, 3}},
		}))
	})

	It("fails on a lone path that names nothing, rather than binding null", func() {
		_, err := hookaction.RenderWith(map[string]any{"product": "{{output.value.missing}}"}, vars)
		Expect(err).To(MatchError(ContainSubstring(`with.product: {{output.value.missing}}: "missing" not found`)))
	})
})

var _ = Describe("ApplySchemaHooks", func() {
	questions := `{"product":{"type":"choice","instructions":"Pick the product.","criteria":{"unknown":"No evidence."}}}`
	emitted := map[string]any{"product": map[string]any{"type": "choice", "instructions": "Pick the product.", "criteria": map[string]any{
		"Annuity": "The annuity product.", "unknown": "No evidence.",
	}}}
	spec := func(hooks ...api.HookAction) *api.Spec {
		return &api.Spec{
			Model:  api.Model{Name: "jev-latest"},
			Labels: map[string]string{"team": "ops"},
			Prompt: api.Prompt{
				User: "classify", OutputSchemaType: api.SchemaTypeTypeSafe, TypeSafeQuestions: json.RawMessage(questions),
				Variables: map[string]any{"product": "Annuity"}, Metadata: map[string]string{"source": "jira"},
				SchemaHooks: hooks,
			},
		}
	}

	It("replaces the TypeSafe questions with what the tool emits, given the schema, vars and with", func() {
		runner := &fakeRunner{answers: map[string]any{"choices": emitted}}
		s := spec(api.HookAction{Tool: "choices", With: map[string]any{"questions": "{{vars.product}}"}})

		Expect(hookaction.ApplySchemaHooks(context.Background(), s, runner, nil)).To(Succeed())

		Expect(s.Prompt.TypeSafeQuestions).To(MatchJSON(mustJSON(emitted)))
		Expect(runner.calls).To(HaveLen(1))
		Expect(mustJSON(runner.calls[0].Input)).To(MatchJSON(`{
			"schema": ` + questions + `, "schemaType": "typesafe-schema",
			"vars": {"product": "Annuity"}, "metadata": {"source": "jira"}, "questions": "Annuity"}`))
	})

	It("chains hooks in order, each seeing the schema the previous one emitted", func() {
		runner := &fakeRunner{answers: map[string]any{"first": emitted, "second.prompt": emitted}}
		s := spec(api.HookAction{Tool: "first"}, api.HookAction{Prompt: "second.prompt"})

		Expect(hookaction.ApplySchemaHooks(context.Background(), s, runner, nil)).To(Succeed())

		Expect(runner.calls[1].Input["schema"]).To(Equal(emitted))
	})

	It("skips a hook whose if is false", func() {
		runner := &fakeRunner{}
		s := spec(api.HookAction{Tool: "choices", If: `labels.team == "dev"`})

		Expect(hookaction.ApplySchemaHooks(context.Background(), s, runner, nil)).To(Succeed())

		Expect(runner.calls).To(BeEmpty())
		Expect(s.Prompt.TypeSafeQuestions).To(MatchJSON(questions))
	})

	It("fails before any model call on an emission that is not valid TypeSafe questions, naming the hook", func() {
		runner := &fakeRunner{answers: map[string]any{"choices": map[string]any{"product": map[string]any{"type": "choice"}}}}
		s := spec(api.HookAction{Tool: "choices"})

		err := hookaction.ApplySchemaHooks(context.Background(), s, runner, nil)

		Expect(err).To(MatchError(ContainSubstring(`output.hooks[0] (tool choices): emitted schema: TypeSafe question "product"`)))
		Expect(s.Prompt.TypeSafeQuestions).To(MatchJSON(questions))
	})

	It("replaces a JSON Schema and refuses one that does not compile", func() {
		s := spec(api.HookAction{Tool: "schema"})
		s.Prompt.OutputSchemaType, s.Prompt.TypeSafeQuestions = api.SchemaTypeJSONSchema, nil
		s.Prompt.SchemaJSON = json.RawMessage(`{"type":"object"}`)
		good := map[string]any{"type": "object", "required": []any{"product"}}
		Expect(hookaction.ApplySchemaHooks(context.Background(), s, &fakeRunner{answers: map[string]any{"schema": good}}, nil)).To(Succeed())
		Expect(s.Prompt.SchemaJSON).To(MatchJSON(mustJSON(good)))

		bad := map[string]any{"type": 7}
		err := hookaction.ApplySchemaHooks(context.Background(), s, &fakeRunner{answers: map[string]any{"schema": bad}}, nil)
		Expect(err).To(MatchError(ContainSubstring("output.hooks[0] (tool schema): emitted schema")))
	})
})

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return string(raw)
}

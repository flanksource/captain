package promptrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

const (
	hookTestTimeout  = 2 * time.Minute
	staticQuestions  = `{"product":{"type":"choice","instructions":"Which product?","criteria":{"unknown":"No evidence."}}}`
	emittedQuestions = `{"product":{"type":"choice","instructions":"Which product?","criteria":{"Annuity":"Annuity.","unknown":"No evidence."}}}`
)

var _ = g.Describe("Hook actions", func() {
	var dir string

	g.BeforeEach(func() { dir = g.GinkgoT().TempDir() })

	// hooked binds the spec's inline tools and returns its hook list with a
	// context to dispatch it on, as Run would.
	hooked := func(spec api.Spec, cfg ...ai.Config) ([]any, *agent.HookContext) {
		in := Input{Resolved: api.ResolvedSpec{Spec: spec}, Timeout: hookTestTimeout}
		if len(cfg) > 0 {
			in.Config = cfg[0]
		}
		Expect(bindInlineTools(&in)).To(Succeed())
		hooks, err := Hooks(context.Background(), in, &bufferedOnlyProvider{})
		Expect(err).NotTo(HaveOccurred())
		request := in.Resolved.Spec
		hc := &agent.HookContext{Context: context.Background(), Request: &request, Response: &ai.Response{}}
		return hooks, hc
	}
	preRun := func(hooks []any, hc *agent.HookContext) error {
		for _, hook := range hooks {
			if pr, ok := hook.(agent.PreRun); ok {
				if err := pr.PreRun(hc); err != nil {
					return err
				}
			}
		}
		return nil
	}
	postRun := func(hooks []any, hc *agent.HookContext) error {
		for _, hook := range hooks {
			if post, ok := hook.(agent.Post); ok {
				if err := post.Post(hc, agent.PhaseRun); err != nil {
					return err
				}
			}
		}
		return nil
	}
	schemaSpec := func(emit string) api.Spec {
		return api.Spec{
			Tools: api.InlineTools{"choices": {Exec: "cat > " + filepath.Join(dir, "stdin.json") + "; printf '%s' '" + emit + "'", ReadOnly: true}},
			Prompt: api.Prompt{
				User: "classify", OutputSchemaType: api.SchemaTypeTypeSafe, TypeSafeQuestions: json.RawMessage(staticQuestions),
				Variables:   map[string]any{"product": "Annuity"},
				SchemaHooks: []api.HookAction{{Tool: "choices", With: map[string]any{"product": "{{vars.product}}"}}},
			},
		}
	}

	g.It("replaces the TypeSafe questions before the first turn with what the schema tool prints", func() {
		hooks, hc := hooked(schemaSpec(emittedQuestions))
		g.DeferCleanup(func() { _ = postRun(hooks, hc) })

		Expect(preRun(hooks, hc)).To(Succeed())

		Expect(hc.Request.Prompt.TypeSafeQuestions).To(MatchJSON(emittedQuestions))
		stdin, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(stdin).To(MatchJSON(`{"schema": ` + staticQuestions + `, "schemaType": "typesafe-schema",
			"vars": {"product": "Annuity"}, "metadata": {}, "product": "Annuity"}`))
	})

	g.It("fails pre-run, naming the hook, when the emitted schema is not valid", func() {
		hooks, hc := hooked(schemaSpec(`{"product":{"type":"choice"}}`))
		g.DeferCleanup(func() { _ = postRun(hooks, hc) })

		Expect(preRun(hooks, hc)).To(MatchError(ContainSubstring(`output.hooks[0] (tool choices): emitted schema: TypeSafe question "product"`)))
	})

	g.It("runs an after tool step through the inline tool's runner with the native with as stdin", func() {
		spec := api.Spec{
			Tools:  api.InlineTools{"record": {Exec: "cat > " + filepath.Join(dir, "record.json"), Permission: api.ToolPolicyAllow}},
			Prompt: api.Prompt{User: "classify", Variables: map[string]any{"file": "/issues/KEY-1.md"}},
			Workflow: &api.Workflow{After: []api.AfterStep{{When: api.CommitWhenSuccess, HookAction: api.HookAction{
				Tool: "record", With: map[string]any{"file": "{{vars.file}}", "values": "{{output.value}}"},
			}}}},
		}
		hooks, hc := hooked(spec)
		Expect(preRun(hooks, hc)).To(Succeed())
		hc.Response.StructuredData = map[string]any{"value": map[string]any{"product": "Annuity"}}

		Expect(postRun(hooks, hc)).To(Succeed())

		recorded, err := os.ReadFile(filepath.Join(dir, "record.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(MatchJSON(`{"file": "/issues/KEY-1.md", "values": {"product": "Annuity"}}`))
	})

	g.It("runs an after prompt step as a child run whose vars are the rendered with, failing the parent when it fails", func() {
		child := filepath.Join(dir, "stage2.prompt")
		Expect(os.WriteFile(child, []byte("---\nmodel: deepseek-reasoner\n---\nPick a transaction for {{product}}."), 0o644)).To(Succeed())
		provider := &bufferedOnlyProvider{}
		actions := newHookActions(Input{Provider: provider, Timeout: hookTestTimeout})

		out, err := actions.RunPrompt(context.Background(), child, map[string]any{"product": "Annuity"}, dir)

		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(map[string]any{"status": "ok"}))
		Expect(provider.executeCalls).To(Equal(1))

		_, err = actions.RunPrompt(context.Background(), filepath.Join(dir, "missing.prompt"), nil, dir)
		Expect(err).To(MatchError(ContainSubstring("missing.prompt")))
	})

	g.Context("input hooks", func() {
		inputHooked := func(emit string) *promptlib.Template {
			return promptlib.LoadNamed("classify.prompt", `---
model: deepseek-reasoner
tools:
  load:
    description: Load the issue
    readonly: true
    exec: cat > `+filepath.Join(dir, "stdin.json")+`; printf '%s' '`+emit+`'
input:
  hooks:
    - tool: load
      with: {path: "{{vars.file}}"}
---
Classify {{issue}} from {{file}}.`)
		}

		g.It("renders the body with the vars the hook returns, after handing it the original vars", func() {
			req, _, skip, err := RenderTemplate(context.Background(), inputHooked(`{"vars":{"file":"KEY-1.md","issue":"the loaded issue"}}`),
				promptlib.RenderOptions{Data: map[string]any{"file": "KEY-1.md"}}, ai.Config{})

			Expect(err).NotTo(HaveOccurred())
			Expect(skip).To(BeNil())
			Expect(req.Prompt.User).To(Equal("Classify the loaded issue from KEY-1.md."))
			Expect(req.Prompt.Variables).To(Equal(map[string]any{"file": "KEY-1.md", "issue": "the loaded issue"}))
			stdin, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(stdin).To(MatchJSON(`{"vars": {"file": "KEY-1.md"}, "metadata": {}, "path": "KEY-1.md"}`))
		})

		g.It("returns the skip a hook asks for instead of rendering with new vars", func() {
			_, _, skip, err := RenderTemplate(context.Background(), inputHooked(`{"skip":"already classified"}`),
				promptlib.RenderOptions{Data: map[string]any{"file": "KEY-1.md"}}, ai.Config{})

			Expect(err).NotTo(HaveOccurred())
			Expect(skip).To(Equal(&Skip{Hook: "input.hooks[0] (tool load)", Reason: "already classified"}))
		})

		g.It("reports a child prompt its input hooks skip as a Skip error, without running it", func() {
			child := filepath.Join(dir, "stage2.prompt")
			Expect(os.WriteFile(child, []byte(`---
model: deepseek-reasoner
tools:
  done: {description: Already done, readonly: true, exec: "cat >/dev/null; printf '%s' '{\"skip\":\"already done\"}'"}
input:
  hooks: [{tool: done}]
---
Pick a transaction for {{product}}.`), 0o644)).To(Succeed())
			provider := &bufferedOnlyProvider{}
			actions := newHookActions(Input{Provider: provider, Timeout: hookTestTimeout})

			_, err := actions.RunPrompt(context.Background(), child, map[string]any{"product": "Annuity"}, dir)

			var skipped *Skip
			Expect(errors.As(err, &skipped)).To(BeTrue(), "got %v", err)
			Expect(skipped.Reason).To(Equal("already done"))
			Expect(provider.executeCalls).To(BeZero())
		})
	})

	// A Jev run ignores tools, so admission never resolved their permissions:
	// the hook must enforce them itself before a destructive tool runs.
	g.DescribeTable("gates a Jev run's destructive after tool on its permission",
		func(permission api.ToolPolicy, approve api.ApprovalFunc, wantErr string) {
			marker := filepath.Join(dir, "landed")
			spec := api.Spec{
				Model:    api.Model{Name: "jev-latest", Mode: api.ModeAPI},
				Tools:    api.InlineTools{"write": {Exec: "touch " + marker, Destructive: true, Permission: permission}},
				Prompt:   api.Prompt{User: "classify"},
				Workflow: &api.Workflow{After: []api.AfterStep{{When: api.CommitWhenSuccess, HookAction: api.HookAction{Tool: "write"}}}},
			}
			hooks, hc := hooked(spec, ai.Config{OnApproval: approve})
			Expect(preRun(hooks, hc)).To(Succeed())

			err := postRun(hooks, hc)

			_, statErr := os.Stat(marker)
			if wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
				Expect(statErr).NotTo(HaveOccurred(), "the tool ran")
				return
			}
			Expect(err).To(MatchError(ContainSubstring(wantErr)))
			Expect(os.IsNotExist(statErr)).To(BeTrue(), "the tool must not run unapproved")
		},
		g.Entry("unset permission, no broker: refused", api.ToolPolicy(""), nil, `tool "write" needs approval, but the run attaches no approval broker`),
		g.Entry("unset permission, broker denies: refused", api.ToolPolicy(""), decide(false), `tool "write" was not approved`),
		g.Entry("unset permission, broker approves: runs", api.ToolPolicy(""), decide(true), ""),
		g.Entry("permission allow: runs", api.ToolPolicyAllow, nil, ""),
		g.Entry("permission deny: refused", api.ToolPolicyDeny, decide(true), `tool "write" is denied`),
	)
})

func decide(allow bool) api.ApprovalFunc {
	return func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{Allow: allow, Message: "reviewer said so"}, nil
	}
}

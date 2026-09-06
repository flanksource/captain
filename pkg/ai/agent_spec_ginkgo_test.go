package ai_test

import (
	"context"
	"encoding/json"
	"sync"

	captainai "github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type namedSpecProvider struct {
	mu       sync.Mutex
	requests []api.Spec
}

func (p *namedSpecProvider) Execute(_ context.Context, spec api.Spec) (*api.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, spec)
	return &api.Response{Text: spec.Prompt.User, Model: spec.Model.Name, CostUSD: 0.02}, nil
}

func (p *namedSpecProvider) GetModel() string { return "provider-model" }
func (p *namedSpecProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.OpenAI, api.ModeAPI)
}

var _ = Describe("Named prompt Spec transport", func() {
	It("forwards the complete declared spec without borrowing provider config", func() {
		temperature := 0.0
		spec := api.Spec{
			Explicit: api.FieldPresence{"/noCache": true, "/permissions": true},
			Model:    api.Model{Name: "gpt-5.4", Mode: api.ModeAPI, Temperature: &temperature},
			Prompt: api.Prompt{
				User: "Review the change", System: "Explain findings", Source: "review.prompt",
				SchemaJSON: json.RawMessage(`{"type":"object"}`), SchemaStrictness: "error",
			},
			Budget:      api.Budget{MaxTokens: 512, MaxTurns: 3, Timeout: "1m"},
			Memory:      api.Memory{Skills: []string{"review"}, SkipUser: true},
			Permissions: api.Permissions{Mode: "plan"},
			Setup:       &shell.Setup{Cwd: "/workspace/review", Env: []string{"REVIEW_MODE=focused"}},
			Sandbox:     &api.SandboxRef{Mode: "off"},
			SessionID:   "review-session",
			CLIArgs:     map[string]any{"resume": true},
			Workflow:    &api.Workflow{Verify: &api.Verify{Commands: []string{"make test"}}},
		}
		provider := &namedSpecProvider{}
		agent := captainai.NewAgentWithProvider(provider, captainai.Config{
			Model: api.Model{Name: "config-model"}, Budget: api.Budget{MaxTokens: 2048},
		})
		request := captainai.PromptRequest{Name: "review", Spec: spec}

		response, err := agent.ExecutePrompt(context.Background(), request)

		Expect(err).NotTo(HaveOccurred())
		Expect(provider.requests).To(Equal([]api.Spec{spec}))
		Expect(response.Request).To(Equal(request))
		Expect(response.Result).To(Equal("Review the change"))
		Expect(agent.TotalCost()).To(BeNumerically("~", 0.02))
	})

	It("keeps the native structured-output target attached to the prompt", func() {
		target := &struct{ Summary string }{}
		spec := api.Spec{Prompt: api.Prompt{User: "Summarize", Schema: target}}
		provider := &namedSpecProvider{}
		agent := captainai.NewAgentWithProvider(provider, captainai.Config{})

		_, err := agent.ExecutePrompt(context.Background(), captainai.PromptRequest{Name: "summary", Spec: spec})

		Expect(err).NotTo(HaveOccurred())
		Expect(provider.requests).To(Equal([]api.Spec{spec}))
		Expect(provider.requests[0].Prompt.Schema).To(BeIdenticalTo(target))
	})

	It("keeps each batch item's spec and response name independent", func() {
		requests := []captainai.PromptRequest{
			{Name: "first", Spec: api.Spec{Prompt: api.Prompt{User: "First"}, SessionID: "first-session"}},
			{Name: "second", Spec: api.Spec{Prompt: api.Prompt{User: "Second"}, Memory: api.Memory{Skills: []string{"second"}}}},
		}
		provider := &namedSpecProvider{}
		agent := captainai.NewAgentWithProvider(provider, captainai.Config{MaxConcurrent: 2})

		responses, err := agent.ExecuteBatch(context.Background(), requests)

		Expect(err).NotTo(HaveOccurred())
		Expect(provider.requests).To(ConsistOf(requests[0].Spec, requests[1].Spec))
		Expect(responses["first"].Request).To(Equal(requests[0]))
		Expect(responses["second"].Request).To(Equal(requests[1]))
		Expect(responses["first"].Result).To(Equal("First"))
		Expect(responses["second"].Result).To(Equal("Second"))
		Expect(agent.GetCosts()).To(HaveLen(2))
	})
})

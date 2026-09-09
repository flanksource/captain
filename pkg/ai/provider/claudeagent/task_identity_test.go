package claudeagent

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Supervised agent task identity", func() {
	newProvider := func() *Provider {
		provider, err := New(ai.Config{Model: api.Model{Name: "claude-opus-5"}})
		Expect(err).ToNot(HaveOccurred())
		return provider
	}

	It("names the task after the host's own title and carries its labels through", func() {
		provider := newProvider()

		options := provider.taskIdentity(ai.Request{
			Labels:    map[string]string{"title": "Stack Trace Viewer improvements", "todo": "5d9f1d2a", "href": "/todos/5d9f1d2a"},
			SessionID: "7c16765b",
			Budget:    api.Budget{Cost: 50},
		})

		Expect(options.Name).To(Equal("Stack Trace Viewer improvements"))
		Expect(options.Kind).To(Equal("agent"))
		Expect(options.Href).To(Equal("/todos/5d9f1d2a"))
		Expect(options.Background).To(BeTrue())
		Expect(options.Labels).To(Equal(map[string]string{
			"title":      "Stack Trace Viewer improvements",
			"todo":       "5d9f1d2a",
			"href":       "/todos/5d9f1d2a",
			"model":      "claude-opus-5",
			"provider":   "anthropic",
			"mode":       "agent",
			"runSession": "7c16765b",
			"budget":     "50",
		}))
	})

	It("falls back to a model-qualified name when the host supplies no title", func() {
		options := newProvider().taskIdentity(ai.Request{})

		Expect(options.Name).To(Equal("claude-agent (claude-opus-5)"))
		Expect(options.Href).To(BeEmpty())
		Expect(options.Labels).To(Equal(map[string]string{
			"model":    "claude-opus-5",
			"provider": "anthropic",
			"mode":     "agent",
		}))
	})

	It("drops empty host labels rather than emitting blank chips", func() {
		options := newProvider().taskIdentity(ai.Request{
			Labels: map[string]string{"todo": "", "phase": "run"},
		})

		Expect(options.Labels).ToNot(HaveKey("todo"))
		Expect(options.Labels).To(HaveKeyWithValue("phase", "run"))
	})

	It("reports the turn state and fills the session in once the handshake lands", func() {
		provider := newProvider()

		Expect(provider.taskMetadata()).To(Equal(ai.AgentMetadata{State: ai.AgentIdle}))

		provider.setActive(&turnState{planMode: true, pending: 2})
		provider.rememberSession("df7aa36e")

		Expect(provider.taskMetadata()).To(Equal(ai.AgentMetadata{
			State:   ai.AgentRunning,
			Session: "df7aa36e",
			Turn:    &ai.AgentTurn{PlanMode: true, Pending: 2},
		}))

		provider.clearActive()
		Expect(provider.taskMetadata()).To(Equal(ai.AgentMetadata{
			State:   ai.AgentIdle,
			Session: "df7aa36e",
		}))
	})
})

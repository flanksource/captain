package promptrun_test

import (
	"context"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptcontext"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
)

const (
	contextPrompt = "fix the build for PR"
	contextBlock  = "<!-- captain:context:pr-status -->\nPR status: run gavel pr status\n<!-- /captain:context:pr-status -->"
)

// requestCapture streams an answered turn and keeps every request it was sent.
type requestCapture struct {
	eventsProvider
	requests []ai.Request
}

func (p *requestCapture) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return p.eventsProvider.ExecuteStream(ctx, req)
}

func prStatusSource() []promptcontext.Source {
	return []promptcontext.Source{{Label: "host", FS: fstest.MapFS{"pr-status.prompt": {Data: []byte(
		"---\nhook: {on: prompt.context, mode: append, match: '\\bPR\\b'}\n---\nPR status: run gavel pr status\n")}}}}
}

var _ = Describe("prompt context hooks", func() {
	var (
		provider *requestCapture
		resolved api.ResolvedSpec
		events   []ai.Event
	)

	BeforeEach(func() {
		provider = &requestCapture{eventsProvider: eventsProvider{events: answeredTurn(uuid.NewString())}}
		spec := api.Spec{
			Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent},
			Prompt: api.Prompt{User: contextPrompt},
		}
		spec.SetCwd(GinkgoT().TempDir())
		resolved = api.ResolvedSpec{Spec: spec}
		events = nil
	})

	input := func(sources []promptcontext.Source) promptrun.Input {
		return promptrun.Input{
			Resolved: resolved, Provider: provider, Timeout: testTimeout, ContextSources: sources,
			OnEvent: func(_ int, ev ai.Event) { events = append(events, ev) },
		}
	}

	It("sends the runner the enriched prompt, records it in metadata and announces the hook", func(ctx SpecContext) {
		_, err := promptrun.Run(ctx, input(prStatusSource()))
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.requests).NotTo(BeEmpty())
		Expect(provider.requests[0].Prompt.User).To(Equal(contextPrompt + "\n\n" + contextBlock))
		Expect(provider.requests[0].Prompt.Metadata).To(Equal(map[string]string{promptcontext.MetadataKey: "pr-status"}))
		Expect(events[0]).To(Equal(ai.Event{Kind: ai.EventSystem, Text: "context hook pr-status (host:pr-status.prompt) applied"}))
	})

	It("leaves the prompt byte-identical when no context sources are configured", func(ctx SpecContext) {
		_, err := promptrun.Run(ctx, input(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.requests[0].Prompt).To(Equal(api.Prompt{User: contextPrompt}))
	})

	It("admits the enriched spec with the hook's provenance on /prompt/user", func(ctx SpecContext) {
		db := openRecordDB()
		var admitted map[string]any
		in := input(prStatusSource())
		in.Record = &promptrun.Recording{
			DB: db, Placement: promptrun.Placement{Sessions: hostTree()}, Origin: "test", AdmissionKey: "admission-" + uuid.NewString(),
			Link: func(_ context.Context, _ *database.DB, run *database.PromptRun) error {
				admitted = map[string]any{"spec": run.RenderedSpec, "provenance": run.Metadata["specProvenance"]}
				return nil
			},
		}
		_, err := promptrun.Run(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		Expect(admitted["spec"]).To(HaveKeyWithValue("prompt", map[string]any{
			"user": contextPrompt + "\n\n" + contextBlock, "metadata": map[string]any{promptcontext.MetadataKey: "pr-status"},
		}))
		context := map[string]any{"kind": "context", "name": "context hook pr-status", "key": "host:pr-status.prompt"}
		Expect(admitted["provenance"]).To(HaveKeyWithValue("/prompt/user", map[string]any{"source": context}))
	})
})

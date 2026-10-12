package cli

import (
	"context"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type interruptProviderStub struct {
	interrupted bool
}

func (p *interruptProviderStub) Execute(context.Context, api.Spec) (*api.Response, error) {
	return &api.Response{}, nil
}
func (p *interruptProviderStub) GetModel() string { return "test-model" }
func (p *interruptProviderStub) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.OpenAI, api.ModeAgent)
}
func (p *interruptProviderStub) Interrupt(context.Context) error {
	p.interrupted = true
	return nil
}

var _ = Describe("prompt chat lifecycle", func() {
	It("queues an idle follow-up and publishes its exact message id", func() {
		stream := newRunStream()
		chat := newChatSession("run-1", PromptRenderResult{Provider: api.OpenAI.Name, Mode: string(api.ModeAgent)}, 0, stream, nil)
		chat.state.Status = "idle"

		response, err := chat.send(context.Background(), ChatMessageRequest{Text: "next", MessageID: "message-1"})

		Expect(err).NotTo(HaveOccurred())
		Expect(response.Status).To(Equal("started"))
		Expect(response.MessageID).To(Equal("message-1"))
		entries, _, _, _ := stream.snapshot()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].ID).To(Equal("message-1"))
		Expect(chat.queue).To(Equal([]ChatQueuedMessage{{MessageID: "message-1", Text: "next"}}))
	})

	It("interrupts the active turn and discards queued messages", func() {
		stream := newRunStream()
		provider := &interruptProviderStub{}
		chat := newChatSession("run-1", PromptRenderResult{Provider: api.OpenAI.Name, Mode: string(api.ModeAgent)}, 0, stream, nil)
		chat.provider = provider
		chat.state.Status = "running"
		chat.state.Turn = 2
		chat.turnDone = make(chan struct{})
		chat.queue = []ChatQueuedMessage{{MessageID: "queued-1", Text: "later"}}

		response, err := chat.interrupt(context.Background())
		close(chat.turnDone)

		Expect(err).NotTo(HaveOccurred())
		Expect(provider.interrupted).To(BeTrue())
		Expect(response.DiscardedMessageIDs).To(Equal([]string{"queued-1"}))
		Expect(chat.state.Status).To(Equal("interrupting"))
		Expect(chat.state.Queued).To(BeEmpty())
		Expect(chat.state.DiscardedMessageIDs).To(Equal([]string{"queued-1"}))
	})

	It("runs as a background task so a host's drain never waits on an idle chat", func() {
		cwd := GinkgoT().TempDir()
		rendered := claudeAgentRender("")
		rendered.Input.SetCwd(cwd)
		// A missing attachment ends the run before any provider is built.
		rendered.Input.Prompt.Attachments = []api.AttachmentRef{{Path: filepath.Join(cwd, "missing.png")}}
		chat := newChatSession("background-run", rendered, 0, newRunStream(), nil)

		handle := task.StartTask("chat run", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return chat.run(t)
		})
		handle.WaitFor()
		_, err := handle.GetResult()

		Expect(err).To(HaveOccurred())
		Expect(handle.IsBackground()).To(BeTrue())
	})

	It("latches stop requests made before a cancel function is attached", func() {
		stream := newRunStream()
		Expect(stream.requestStop()).To(BeTrue())
		cancelled := false
		stream.setCancel(func() { cancelled = true })
		Expect(cancelled).To(BeTrue())
	})

	It("publishes every prompt chat control route in OpenAPI", func() {
		spec := &rpc.OpenAPISpec{}
		addCaptainPromptRunPaths(spec)

		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}"))
		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}/stream"))
		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}/message"))
		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}/interrupt"))
		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}/stop"))
		Expect(spec.Paths).To(HaveKey("/api/captain/prompt/runs/{runId}/permission-mode"))
		Expect(spec.Paths).To(HaveKeyWithValue("/api/captain/sessions/{id}/message", HaveKey("post")))
		Expect(spec.Paths).To(HaveKeyWithValue("/api/captain/sessions/{id}/chat", HaveKey("get")))
		Expect(spec.Paths).To(HaveKeyWithValue("/api/captain/sessions/{id}/interrupt", HaveKey("post")))
		Expect(spec.Paths).To(HaveKeyWithValue("/api/captain/sessions/{id}/permission-mode", HaveKey("post")))
	})

	It("collapses duplicate records for the same provider session when resuming", func() {
		rich := SessionGetItem{
			CaptainID: "captain-rich", ProviderSessionID: "provider-1",
			Summary: SessionRecord{Model: "gpt-5.6-sol", Provider: "openai", ModelMode: "agent", CWD: "/repo"},
		}
		selected, err := selectResumeSession([]SessionGetItem{
			{CaptainID: "captain-sparse", ProviderSessionID: "provider-1"}, rich,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal(rich))
	})

	It("rejects resume prefixes that identify different provider sessions", func() {
		_, err := selectResumeSession([]SessionGetItem{
			{ProviderSessionID: "provider-1"}, {ProviderSessionID: "provider-2"},
		})

		Expect(err).To(MatchError("session id is ambiguous"))
	})
})

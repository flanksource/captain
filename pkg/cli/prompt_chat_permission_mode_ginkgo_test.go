package cli

import (
	"context"
	"errors"
	"net/http"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type permissionSwitchProviderStub struct {
	runtime api.Runtime
	modes   []api.PermissionMode
}

func (p *permissionSwitchProviderStub) Execute(context.Context, api.Spec) (*api.Response, error) {
	return &api.Response{}, nil
}
func (p *permissionSwitchProviderStub) GetModel() string        { return "test-model" }
func (p *permissionSwitchProviderStub) GetRuntime() api.Runtime { return p.runtime }
func (p *permissionSwitchProviderStub) SetPermissionMode(_ context.Context, mode api.PermissionMode) error {
	p.modes = append(p.modes, mode)
	return nil
}

// claudeAgentModes is the canonical-order posture list the claude agent runtime
// honours; every claude transport maps all six postures natively.
var claudeAgentModes = []api.PermissionMode{
	api.PermissionAcceptEdits, api.PermissionAuto, api.PermissionBypass,
	api.PermissionDefault, api.PermissionDontAsk, api.PermissionPlan,
}

func claudeAgentRender(mode api.PermissionMode) PromptRenderResult {
	rendered := PromptRenderResult{Provider: api.Anthropic.Name, Mode: string(api.ModeAgent)}
	rendered.Input.Permissions.Mode = mode
	return rendered
}

func chatErrorStatus(err error) int {
	var target chatHTTPError
	if !errors.As(err, &target) {
		return 0
	}
	return target.status
}

var _ = Describe("prompt chat permission mode", func() {
	It("reports the rendered posture, the runtime's postures, and the switch capability", func() {
		chat := newChatSession("run-1", claudeAgentRender(api.PermissionAuto), 0, newRunStream(), nil)

		Expect(chat.state.PermissionMode).To(Equal(api.PermissionAuto))
		Expect(chat.state.PermissionModes).To(Equal(claudeAgentModes))
		Expect(chat.state.Capabilities.SetPermissionMode).To(BeTrue())
	})

	It("lets a live codex agent run switch into auto review", func() {
		provider := &permissionSwitchProviderStub{runtime: api.RuntimeOf(api.OpenAI, api.ModeAgent)}
		rendered := PromptRenderResult{Provider: api.OpenAI.Name, Mode: string(api.ModeAgent)}
		chat := newChatSession("run-1", rendered, 0, newRunStream(), nil)
		chat.provider = provider
		chat.state.Status = "idle"

		_, err := chat.setPermissionMode(context.Background(), api.PermissionAuto)

		Expect(err).NotTo(HaveOccurred())
		Expect(chat.state.Capabilities.SetPermissionMode).To(BeTrue())
		Expect(provider.modes).To(Equal([]api.PermissionMode{api.PermissionAuto}))
		Expect(chat.followUpRequest(chat.rendered.Input, "next").Permissions.Mode).To(Equal(api.PermissionAuto))
	})

	It("reports default when the run declared no posture", func() {
		chat := newChatSession("run-1", claudeAgentRender(""), 0, newRunStream(), nil)

		Expect(chat.state.PermissionMode).To(Equal(api.PermissionDefault))
	})

	It("switches a live run and carries the posture into follow-up turns", func() {
		stream := newRunStream()
		provider := &permissionSwitchProviderStub{runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent)}
		chat := newChatSession("run-1", claudeAgentRender(api.PermissionDefault), 0, stream, nil)
		chat.provider = provider
		chat.state.Status = "running"

		response, err := chat.setPermissionMode(context.Background(), api.PermissionAuto)

		Expect(err).NotTo(HaveOccurred())
		Expect(response).To(Equal(ChatPermissionModeResponse{RunID: "run-1", PermissionMode: api.PermissionAuto}))
		Expect(provider.modes).To(Equal([]api.PermissionMode{api.PermissionAuto}))
		Expect(chat.state.PermissionMode).To(Equal(api.PermissionAuto))
		Expect(chat.followUpRequest(chat.rendered.Input, "next").Permissions.Mode).To(Equal(api.PermissionAuto))
		snapshot, events := stream.subscribeEvents()
		stream.unsubscribeEvents(events)
		Expect(snapshot.State.PermissionMode).To(Equal(api.PermissionAuto))
	})

	It("switches a live run before sending a message that names a different posture", func() {
		provider := &permissionSwitchProviderStub{runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent)}
		chat := newChatSession("run-1", claudeAgentRender(api.PermissionDefault), 0, newRunStream(), nil)
		chat.provider = provider
		chat.state.Status = "idle"

		_, err := chat.send(context.Background(), ChatMessageRequest{Text: "next", PermissionMode: "auto"})

		Expect(err).NotTo(HaveOccurred())
		Expect(provider.modes).To(Equal([]api.PermissionMode{api.PermissionAuto}))
		Expect(chat.queue).To(HaveLen(1))
	})

	DescribeTable("refuses a switch the run cannot perform",
		func(rendered PromptRenderResult, status string, mode api.PermissionMode, wantStatus int) {
			chat := newChatSession("run-1", rendered, 0, newRunStream(), nil)
			chat.provider = &permissionSwitchProviderStub{runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent)}
			chat.state.Status = status

			_, err := chat.setPermissionMode(context.Background(), mode)

			Expect(chatErrorStatus(err)).To(Equal(wantStatus))
		},
		Entry("an unrecognised posture", claudeAgentRender(""), "idle", api.PermissionMode("yolo"), http.StatusBadRequest),
		Entry("a runtime that declares no live switch",
			PromptRenderResult{Provider: api.Google.Name, Mode: string(api.ModeCLI)}, "idle", api.PermissionAuto, http.StatusConflict),
		Entry("a run that has not started its provider", claudeAgentRender(""), "starting", api.PermissionAuto, http.StatusConflict),
	)

	It("refuses a switch on a terminal run", func() {
		chat := newChatSession("run-1", claudeAgentRender(""), 0, newRunStream(), nil)
		chat.terminal = true

		_, err := chat.setPermissionMode(context.Background(), api.PermissionAuto)

		Expect(chatErrorStatus(err)).To(Equal(http.StatusConflict))
	})

	Describe("resuming a saved session", func() {
		var item SessionGetItem

		BeforeEach(func() {
			item = SessionGetItem{
				CaptainID: "captain-1", ProviderSessionID: "provider-1",
				Summary: SessionRecord{Source: "claude", Model: "claude-sonnet-5", CWD: GinkgoT().TempDir()},
			}
		})

		It("starts the resumed run in the requested posture", func() {
			rendered, err := resumeRenderResult(item, ChatMessageRequest{Text: "continue", PermissionMode: "auto"})

			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.Input.Permissions.Mode).To(Equal(api.PermissionAuto))
			Expect(rendered.Input.SessionID).To(Equal("provider-1"))
		})

		It("refuses a posture the resume runtime does not honour", func() {
			item.Summary.Source = "codex"
			item.Summary.Model = "gpt-5.6-sol"

			_, err := resumeRenderResult(item, ChatMessageRequest{Text: "continue", PermissionMode: "dontAsk"})

			Expect(chatErrorStatus(err)).To(Equal(http.StatusUnprocessableEntity))
		})

		It("lists the postures a resumed session may start with", func() {
			Expect(resumePermissionModes("claude")).To(Equal(claudeAgentModes))
			Expect(resumePermissionModes("unknown")).To(BeEmpty())
		})

		It("resumes a launcher-owned session through the transcript row that executed it", func() {
			worktree := GinkgoT().TempDir()
			item.Summary = SessionRecord{Source: "gavel", CWD: "/repository/root/that/does/not/exist"}
			item.Execution = &SessionExecution{
				CaptainID: "transcript-1", Source: "claude", CWD: worktree, Model: "claude-opus-5",
			}

			rendered, err := resumeRenderResult(item, ChatMessageRequest{Text: "Answers: implement the plan"})

			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.Provider).To(Equal("anthropic"))
			Expect(rendered.Model).To(Equal("claude-opus-5"))
			Expect(rendered.Input.Cwd()).To(Equal(worktree))
			Expect(rendered.Input.SessionID).To(Equal("provider-1"))
		})
	})
})

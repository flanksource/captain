package api_test

import (
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical messages", func() {
	validMessages := func() []api.Message {
		return []api.Message{
			{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: "Be precise."}}},
			{Role: api.RoleUser, Parts: []api.Part{
				{Type: api.PartText, Text: "Inspect this image."},
				{Type: api.PartAttachment, Attachment: &api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), MediaType: "image/png"}},
			}},
			{Role: api.RoleAssistant, Parts: []api.Part{
				{Type: api.PartReasoning, Text: "I should inspect its dimensions."},
				{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "call-1", Name: "image_dimensions", Input: json.RawMessage(`{"attachment":"a"}`)}},
			}},
			{Role: api.RoleTool, Parts: []api.Part{
				{Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "call-1", Output: json.RawMessage(`{"width":640,"height":480}`)}},
			}},
			{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: "It is 640 by 480."}}},
		}
	}

	It("validates a multimodal tool transcript", func() {
		Expect(api.ValidateMessages(validMessages())).To(Succeed())
	})

	DescribeTable("rejects parts on incompatible roles",
		func(role api.MessageRole, part api.Part, want string) {
			err := api.ValidateMessages([]api.Message{{Role: role, Parts: []api.Part{part}}})
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("system reasoning", api.RoleSystem, api.Part{Type: api.PartReasoning, Text: "thought"}, "system message cannot contain reasoning"),
		Entry("user reasoning", api.RoleUser, api.Part{Type: api.PartReasoning, Text: "thought"}, "user message cannot contain reasoning"),
		Entry("assistant attachment", api.RoleAssistant, api.Part{Type: api.PartAttachment, Attachment: &api.AttachmentRef{Path: "image.png"}}, "assistant message cannot contain attachment"),
		Entry("tool text", api.RoleTool, api.Part{Type: api.PartText, Text: "result"}, "tool message cannot contain text"),
	)

	It("requires a tool result to immediately follow and match an assistant request", func() {
		messages := validMessages()
		messages[3].Parts[0].ToolResult.ToolCallID = "unknown"
		Expect(api.ValidateMessages(messages)).To(MatchError(ContainSubstring(`tool call "unknown" was not requested by the preceding assistant message`)))

		messages = validMessages()
		messages = append(messages[:3], api.Message{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "continue"}}}, messages[3])
		Expect(api.ValidateMessages(messages)).To(MatchError(ContainSubstring("tool message must immediately follow an assistant message")))
	})

	It("requires stable unique tool call IDs", func() {
		messages := validMessages()
		messages[2].Parts[1].ToolRequest.ToolCallID = ""
		Expect(api.ValidateMessages(messages)).To(MatchError(ContainSubstring("tool request call ID is required")))

		messages = validMessages()
		messages[2].Parts = append(messages[2].Parts, api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
			ToolCallID: "call-1", Name: "other", Input: json.RawMessage(`{}`),
		}})
		Expect(api.ValidateMessages(messages)).To(MatchError(ContainSubstring(`duplicate tool request call ID "call-1"`)))
	})

	It("keeps conversation and single-turn prompt as mutually exclusive request modes", func() {
		spec := api.Spec{Model: api.Model{Name: "gpt-5", Backend: api.BackendOpenAI}, Messages: validMessages()}
		Expect(spec.Validate()).To(Succeed())
		Expect(spec.IsVerifyOnly()).To(BeFalse())

		spec.Prompt.User = "silent fallback"
		Expect(spec.Validate()).To(MatchError(ContainSubstring("prompt body and messages are mutually exclusive")))
	})
})

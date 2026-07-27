package genkit

import (
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("canonical message projection", func() {
	It("projects multimodal history without flattening typed parts", func() {
		attachment := api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("image")})
		messages, err := conversationMessages([]api.Message{
			{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: "Be precise."}}},
			{Role: api.RoleUser, Parts: []api.Part{
				{Type: api.PartText, Text: "Inspect this."},
				{Type: api.PartAttachment, Attachment: &attachment},
			}},
			{Role: api.RoleAssistant, Parts: []api.Part{
				{Type: api.PartReasoning, Text: "I should inspect it."},
				{Type: api.PartText, Text: "Done."},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(HaveLen(3))
		Expect(messages[0].Role).To(Equal(gkai.RoleSystem))
		Expect(messages[1].Role).To(Equal(gkai.RoleUser))
		Expect(messages[1].Content).To(HaveLen(2))
		Expect(messages[1].Content[1]).To(SatisfyAll(
			HaveField("Kind", gkai.PartMedia),
			HaveField("ContentType", "image/png"),
			HaveField("Text", "data:image/png;base64,aW1hZ2U="),
		))
		Expect(messages[2].Role).To(Equal(gkai.RoleModel))
		Expect(messages[2].Content[0].Kind).To(Equal(gkai.PartReasoning))
	})

	It("correlates canonical tool requests and results", func() {
		messages, err := conversationMessages([]api.Message{
			{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "List stacks."}}},
			{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
				ToolCallID: "call-1", Name: "stack_list", Input: json.RawMessage(`{"limit":2}`),
			}}}},
			{Role: api.RoleTool, Parts: []api.Part{{Type: api.PartToolResult, ToolResult: &api.ToolResult{
				ToolCallID: "call-1", Output: json.RawMessage(`{"items":["a","b"]}`),
			}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(HaveLen(3))
		request := messages[1].Content[0].ToolRequest
		Expect(request).To(Equal(&gkai.ToolRequest{Ref: "call-1", Name: "stack_list", Input: map[string]any{"limit": float64(2)}}))
		result := messages[2].Content[0].ToolResponse
		Expect(result).To(Equal(&gkai.ToolResponse{Ref: "call-1", Name: "stack_list", Output: map[string]any{"items": []any{"a", "b"}}}))
	})

	It("does not fall back to the single-turn prompt for an invalid conversation", func() {
		_, err := conversationMessages([]api.Message{{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartReasoning, Text: "invalid"}}}})
		Expect(err).To(MatchError(ContainSubstring("user message cannot contain reasoning")))
	})
})

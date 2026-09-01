package aichat_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("AI SDK v6 wire types", func() {
	It("decodes the DefaultChatTransport request without losing UI parts", func() {
		const body = `{
			"id":"chat-1",
			"trigger":"regenerate-message",
			"messageId":"message-1",
			"messages":[{"id":"message-1","role":"assistant","parts":[
				{"type":"reasoning","text":"checking"},
				{"type":"dynamic-tool","toolName":"invoice_get","toolCallId":"call-1","state":"approval-responded","input":{"id":"inv-1"},"approval":{"id":"approval-1","approved":true}},
				{"type":"file","mediaType":"application/pdf","url":"https://example.com/invoice.pdf","filename":"invoice.pdf","attachmentId":"att_sha256_abc"}
			]}],
			"model":"anthropic/claude-sonnet",
			"reasoningEffort":"high",
			"temperature":0,
			"budget":{"cost":1.5,"maxTokens":2048,"maxTurns":4},
			"toolPreferences":{"billing":"ask","invoice_get":"on"},
			"context":"invoice editor",
			"contextItems":[{"id":"inv-1","type":"invoice","label":"Invoice 1","fields":{"status":"draft"},"payload":{"total":25}}],
			"threadId":"thread-1",
			"providerSessionId":"session-1",
			"permissionMode":"acceptEdits"
		}`

		var request aichat.ChatRequest
		Expect(json.Unmarshal([]byte(body), &request)).To(Succeed())
		Expect(request.ID).To(Equal("chat-1"))
		Expect(request.Trigger).To(Equal("regenerate-message"))
		Expect(request.MessageID).To(Equal("message-1"))
		Expect(request.Model).To(Equal("anthropic/claude-sonnet"))
		Expect(request.ReasoningEffort).To(Equal(api.EffortHigh))
		Expect(request.Temperature).NotTo(BeNil())
		Expect(*request.Temperature).To(Equal(0.0))
		Expect(request.Budget).To(Equal(api.Budget{Cost: 1.5, MaxTokens: 2048, MaxTurns: 4}))
		Expect(request.ToolPreferences).To(Equal(api.ToolPreferences{
			"billing":     api.ToolPolicyAsk,
			"invoice_get": api.ToolPolicyAllow,
		}))
		Expect(request.PermissionMode).To(Equal(api.PermissionAcceptEdits))
		Expect(request.ToolApproval).To(BeNil())
		Expect(request.ThreadID).To(Equal("thread-1"))
		Expect(request.ProviderSessionID).To(Equal("session-1"))
		Expect(request.ContextItems).To(HaveLen(1))
		Expect(request.ContextItems[0].Payload).To(MatchJSON(`{"total":25}`))

		Expect(request.Messages).To(HaveLen(1))
		Expect(request.Messages[0].Parts).To(HaveLen(3))
		tool := request.Messages[0].Parts[1]
		Expect(tool.IsTool()).To(BeTrue())
		Expect(tool.EffectiveToolName()).To(Equal("invoice_get"))
		Expect(tool.Approval).NotTo(BeNil())
		Expect(tool.Approval.Approved).NotTo(BeNil())
		Expect(*tool.Approval.Approved).To(BeTrue())
		Expect(aichat.UIPart{Type: "tool-static_lookup"}.EffectiveToolName()).To(Equal("static_lookup"))
		Expect(aichat.UIPart{Type: "text"}.EffectiveToolName()).To(BeEmpty())
	})

	// An authored runtime names its model and its mode; the provider follows from
	// the model name, server-side. The wire carried a `backend` key that meant the
	// resolved adapter outbound and the mode inbound — a client read one and posted
	// back the other, which is the bug this vocabulary removes.
	It("decodes an exact structured runtime", func() {
		var request aichat.ChatRequest
		Expect(json.Unmarshal([]byte(`{
			"runtime":{"model":"sonnet","mode":"agent","effort":"high"},
			"messages":[{"role":"user","parts":[{"type":"text","text":"hello"}]}]
		}`), &request)).To(Succeed())

		Expect(request.Runtime).NotTo(BeNil())
		Expect(*request.Runtime).To(Equal(api.Model{
			Name: "sonnet", Mode: api.ModeAgent, Effort: api.EffortHigh,
		}))
		resolved, err := api.ResolveModel(*request.Runtime)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Provider).To(Equal(api.Anthropic))
		Expect(resolved.Mode).To(Equal(api.ModeAgent))
	})

	It("rejects client-owned tool approval state", func() {
		var request aichat.ChatRequest
		Expect(json.Unmarshal([]byte(`{"messages":[],"toolApproval":{"state":{}}}`), &request)).To(
			MatchError(ContainSubstring("toolApproval is server-owned")),
		)
	})

	It("publishes stable model and tool catalog response shapes", func() {
		strict := true
		models := aichat.ModelCatalogResponse{{
			ID: "openai/gpt", Provider: "openai", Label: "GPT", Reasoning: true,
			Temperature: true, Configured: true, Availability: api.Available(), ContextWindow: 128000,
			InputMediaTypes: []string{"image/*"},
			Runtime:         api.Model{Name: "gpt-5.5", Mode: api.ModeAPI},
		}}
		tools := aichat.ToolCatalogResponse{Tools: []aichat.ToolCatalogEntry{{
			Name: "invoice_get", Source: "custom", Group: "billing",
			PreferenceKey: "billing", DefaultPermission: api.ToolPolicyAsk,
			Strict: &strict, Method: "GET", Path: "/invoices/{id}",
			OperationName: "invoice get", InputSchema: map[string]any{"type": "object"},
		}}}

		modelJSON, err := json.Marshal(models)
		Expect(err).NotTo(HaveOccurred())
		Expect(modelJSON).To(MatchJSON(`[{"id":"openai/gpt","provider":"openai","label":"GPT","runtime":{"model":"gpt-5.5","mode":"api"},"reasoning":true,"temperature":true,"configured":true,"availability":{"state":"available"},"contextWindow":128000,"inputMediaTypes":["image/*"]}]`))
		toolJSON, err := json.Marshal(tools)
		Expect(err).NotTo(HaveOccurred())
		Expect(toolJSON).To(MatchJSON(`{"tools":[{"name":"invoice_get","source":"custom","group":"billing","preferenceKey":"billing","defaultPermission":"ask","strict":true,"method":"GET","path":"/invoices/{id}","operationName":"invoice get","inputSchema":{"type":"object"}}]}`))
	})
})

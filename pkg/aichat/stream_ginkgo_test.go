package aichat_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *flushRecorder) Flush() { r.flushes++ }

type nonFlushWriter struct {
	header http.Header
}

func (w *nonFlushWriter) Header() http.Header        { return w.header }
func (w *nonFlushWriter) Write([]byte) (int, error)  { return 0, nil }
func (w *nonFlushWriter) WriteHeader(statusCode int) {}

func recordEvents(events ...api.Event) (*flushRecorder, error) {
	return recordEventsWithOptions(aichat.EventStreamOptions{}, events...)
}

func recordEventsWithOptions(options aichat.EventStreamOptions, events ...api.Event) (*flushRecorder, error) {
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer, err := aichat.NewSSEWriter(recorder)
	if err != nil {
		return recorder, err
	}
	channel := make(chan api.Event, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return recorder, aichat.WriteEventStream(writer, channel, options)
}

func decodedDataLines(body string) []map[string]any {
	parts := []map[string]any{}
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var part map[string]any
		Expect(json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &part)).To(Succeed())
		parts = append(parts, part)
	}
	return parts
}

func partTypes(parts []map[string]any) []string {
	types := make([]string, len(parts))
	for i, part := range parts {
		types[i], _ = part["type"].(string)
	}
	return types
}

func pendingApprovalState(calls ...api.ToolApprovalRequest) *api.ToolApprovalState {
	parts := make([]api.Part, len(calls))
	stateCalls := make([]api.ToolApprovalCall, len(calls))
	for i, call := range calls {
		parts[i] = api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
			ToolCallID: call.ToolCallID, Name: call.Tool, Input: call.Input,
		}}
		stateCalls[i] = api.ToolApprovalCall{Request: call}
	}
	return &api.ToolApprovalState{
		Messages: []api.Message{
			{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "update"}}},
			{Role: api.RoleAssistant, Parts: parts},
		},
		Calls: stateCalls,
	}
}

func approvalEvent(callID, tool string) api.Event {
	return api.Event{
		Kind: api.EventPermission, ToolCallID: callID, Tool: tool, ApprovalID: "approval-" + callID,
	}
}

var _ = Describe("AI SDK v6 event stream", func() {
	It("rejects a response writer that cannot stream", func() {
		_, err := aichat.NewSSEWriter(&nonFlushWriter{header: http.Header{}})
		Expect(err).To(MatchError("response writer does not support flushing"))
	})

	It("sets the protocol headers and flushes every frame", func() {
		recorder, err := recordEvents(api.Event{Kind: api.EventResult, Success: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(recorder.Header().Get("Content-Type")).To(Equal("text/event-stream"))
		Expect(recorder.Header().Get("x-vercel-ai-ui-message-stream")).To(Equal("v1"))
		Expect(recorder.Header().Get("Cache-Control")).To(Equal("no-cache"))
		Expect(recorder.Header().Get("x-accel-buffering")).To(Equal("no"))
		Expect(recorder.flushes).To(Equal(6))
		Expect(recorder.Body.String()).To(HaveSuffix("data: [DONE]\n\n"))
	})

	It("keeps every text, reasoning, tool, approval, result, and finish part ordered", func() {
		usage := &api.Usage{InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 5}
		recorder, err := recordEvents(
			api.Event{Kind: api.EventSystem, SessionID: "session-1", Model: "claude-sonnet"},
			api.Event{Kind: api.EventThinking, Text: "check"},
			api.Event{Kind: api.EventThinking, Text: "ing"},
			api.Event{Kind: api.EventText, Text: "I will inspect."},
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_get", Input: map[string]any{"id": "inv-1"}},
			approvalEvent("call-1", "invoice_get"),
			api.Event{Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "invoice_get", Text: `{"status":"draft"}`, Success: true},
			api.Event{Kind: api.EventText, Text: "It is a draft."},
			api.Event{Kind: api.EventResult, SessionID: "session-1", Model: "claude-sonnet", Usage: usage, CostUSD: 0.0125, Success: true, StructuredData: json.RawMessage(`{"invoiceId":"inv-1"}`)},
		)
		Expect(err).NotTo(HaveOccurred())
		parts := decodedDataLines(recorder.Body.String())
		Expect(partTypes(parts)).To(Equal([]string{
			"start", "start-step",
			"reasoning-start", "reasoning-delta", "reasoning-delta", "reasoning-end",
			"text-start", "text-delta", "text-end",
			"tool-input-available", "tool-approval-request", "tool-output-available",
			"text-start", "text-delta", "text-end",
			"data-result", "finish-step", "finish",
		}))
		Expect(parts[2]).To(HaveKeyWithValue("id", "reasoning-0"))
		Expect(parts[6]).To(HaveKeyWithValue("id", "text-1"))
		Expect(parts[9]).To(SatisfyAll(
			HaveKeyWithValue("toolCallId", "call-1"),
			HaveKeyWithValue("toolName", "invoice_get"),
			HaveKeyWithValue("dynamic", true),
		))
		Expect(parts[10]).To(SatisfyAll(
			HaveKeyWithValue("approvalId", "approval-call-1"),
			HaveKeyWithValue("toolCallId", "call-1"),
		))
		Expect(parts[11]["output"]).To(Equal(map[string]any{"output": `{"status":"draft"}`}))
		Expect(parts[15]["data"]).To(Equal(map[string]any{"invoiceId": "inv-1"}))
		Expect(parts[17]["messageMetadata"]).To(Equal(map[string]any{
			"providerSessionId": "session-1",
			"model":             "claude-sonnet",
			"usage": map[string]any{
				"inputTokens": 100.0, "outputTokens": 40.0, "reasoningTokens": 10.0,
				"cacheReadTokens": 5.0, "cacheWriteTokens": 0.0, "totalTokens": 155.0,
			},
			"cost": 0.0125,
			// Context occupancy is the whole prompt: input plus the cached
			// prefix, not input alone.
			"contextTokens": 105.0,
			"success":       true,
		}))
	})

	It("rides the priced breakdown and the thread's cumulative cost on the finish part", func() {
		// Without these the UI has nothing to render: every per-bucket Cost row
		// falls back to "-", and "Thread total" silently degrades to this one
		// turn's cost, which understated a real 9-call thread by 10x.
		costs := &aichat.TurnCosts{
			Breakdown: &aichat.CostBreakdownMetadata{
				Model: "claude-sonnet", InputUSD: 0.0003, OutputUSD: 0.006,
				ReasoningUSD: 0.0015, CacheReadUSD: 0.0000015, TotalUSD: 0.0125,
			},
			ThreadCostUSD: 7.358646,
		}
		recorder, err := recordEventsWithOptions(
			aichat.EventStreamOptions{Costs: costs},
			api.Event{Kind: api.EventText, Text: "done"},
			api.Event{
				Kind: api.EventResult, SessionID: "session-1", Model: "claude-sonnet",
				Usage: &api.Usage{InputTokens: 100, OutputTokens: 40}, CostUSD: 0.0125, Success: true,
			},
		)
		Expect(err).NotTo(HaveOccurred())

		parts := decodedDataLines(recorder.Body.String())
		metadata, ok := parts[len(parts)-1]["messageMetadata"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(metadata).To(HaveKeyWithValue("threadCostUsd", 7.358646))
		Expect(metadata).To(HaveKeyWithValue("cost", 0.0125),
			"the per-turn cost stays alongside the cumulative total, not replaced by it")
		// cacheWriteUsd is emitted as an explicit 0 rather than omitted: on the
		// API backends genkit carries no cache-write tokens at all, and a
		// missing key would render as "-" (unknown) instead of "$0".
		Expect(metadata["costBreakdown"]).To(Equal(map[string]any{
			"model": "claude-sonnet", "inputUsd": 0.0003, "outputUsd": 0.006,
			"reasoningUsd": 0.0015, "cacheReadUsd": 0.0000015, "cacheWriteUsd": 0.0,
			"totalUsd": 0.0125,
		}))
	})

	It("turns a Captain error event into a closed, valid UI stream", func() {
		recorder, err := recordEvents(
			api.Event{Kind: api.EventText, Text: "partial"},
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "accounts_edit"},
			api.Event{Kind: api.EventError, Error: "provider disconnected"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(partTypes(decodedDataLines(recorder.Body.String()))).To(Equal([]string{
			"start", "start-step", "text-start", "text-delta", "text-end",
			"tool-input-available", "tool-output-error", "error", "finish-step", "finish",
		}))
		parts := decodedDataLines(recorder.Body.String())
		Expect(parts[6]).To(SatisfyAll(
			HaveKeyWithValue("toolCallId", "call-1"),
			HaveKeyWithValue("errorText", "provider disconnected"),
		))
		Expect(parts[7]).To(HaveKeyWithValue("errorText", "provider disconnected"))
		Expect(recorder.Body.String()).To(HaveSuffix("data: [DONE]\n\n"))
	})

	It("finishes an interrupted turn without rendering a provider error", func() {
		recorder, err := recordEvents(
			api.Event{Kind: api.EventText, Text: "partial"},
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "accounts_edit"},
			api.Event{Kind: api.EventInterrupted, Reason: "user"},
		)
		Expect(err).NotTo(HaveOccurred())
		parts := decodedDataLines(recorder.Body.String())
		Expect(partTypes(parts)).To(Equal([]string{
			"start", "start-step", "text-start", "text-delta", "text-end",
			"tool-input-available", "tool-output-error", "data-result", "finish-step", "finish",
		}))
		Expect(parts[6]).To(HaveKeyWithValue("errorText", "user"))
		Expect(parts[7]["data"]).To(Equal(map[string]any{"success": false, "interrupted": true}))
		Expect(parts[9]["messageMetadata"]).To(HaveKeyWithValue("interrupted", true))
		Expect(recorder.Body.String()).NotTo(ContainSubstring(`"type":"error"`))
	})

	It("finishes a suspended turn with its approval card still pending", func() {
		recorder, err := recordEvents(
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update", Input: map[string]any{"id": "inv-1"}},
			approvalEvent("call-1", "invoice_update"),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(partTypes(decodedDataLines(recorder.Body.String()))).To(Equal([]string{
			"start", "start-step", "tool-input-available", "tool-approval-request", "finish-step", "finish",
		}))
	})

	It("carries durable Captain approval state across the AI SDK boundary", func() {
		approval := pendingApprovalState(api.ToolApprovalRequest{
			ToolCallID: "call-1", Tool: "invoice_update", Input: json.RawMessage(`{"id":"inv-1"}`),
		})
		recorder, err := recordEvents(
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update", Input: map[string]any{"id": "inv-1"}},
			approvalEvent("call-1", "invoice_update"),
			api.Event{Kind: api.EventResult, Success: true, ToolApproval: approval},
		)
		Expect(err).NotTo(HaveOccurred())
		parts := decodedDataLines(recorder.Body.String())
		Expect(partTypes(parts)).To(Equal([]string{
			"start", "start-step", "tool-input-available", "tool-approval-request",
			"data-result", "finish-step", "finish",
		}))
		Expect(parts[4]["data"]).To(Equal(map[string]any{"success": true, "waitingApproval": true}))
	})

	DescribeTable("rejects approval state that does not match the streamed pending tools",
		func(state *api.ToolApprovalState, message string) {
			recorder, err := recordEvents(
				api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update", Input: map[string]any{"id": "inv-1"}},
				approvalEvent("call-1", "invoice_update"),
				api.Event{Kind: api.EventResult, Success: true, ToolApproval: state},
			)
			Expect(err).To(MatchError(message))
			Expect(partTypes(decodedDataLines(recorder.Body.String()))).To(ContainElement("error"))
		},
		Entry("different call ID", pendingApprovalState(api.ToolApprovalRequest{
			ToolCallID: "call-2", Tool: "invoice_update", Input: json.RawMessage(`{"id":"inv-1"}`),
		}), `approval state call "call-2" has no matching streamed approval request`),
		Entry("different tool name", pendingApprovalState(api.ToolApprovalRequest{
			ToolCallID: "call-1", Tool: "invoice_delete", Input: json.RawMessage(`{"id":"inv-1"}`),
		}), `approval state call "call-1" names "invoice_delete", want "invoice_update"`),
		Entry("different tool input", pendingApprovalState(api.ToolApprovalRequest{
			ToolCallID: "call-1", Tool: "invoice_update", Input: json.RawMessage(`{"id":"inv-2"}`),
		}), `approval state call "call-1" input does not match the streamed tool request`),
		Entry("extra pending call", pendingApprovalState(
			api.ToolApprovalRequest{ToolCallID: "call-1", Tool: "invoice_update", Input: json.RawMessage(`{"id":"inv-1"}`)},
			api.ToolApprovalRequest{ToolCallID: "call-2", Tool: "invoice_delete", Input: json.RawMessage(`{"id":"inv-2"}`)},
		), `approval state call "call-2" has no matching streamed approval request`),
	)

	It("rejects an approval state missing a streamed pending tool", func() {
		state := pendingApprovalState(api.ToolApprovalRequest{
			ToolCallID: "call-1", Tool: "invoice_update", Input: json.RawMessage(`{"id":"inv-1"}`),
		})
		_, err := recordEvents(
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update", Input: map[string]any{"id": "inv-1"}},
			approvalEvent("call-1", "invoice_update"),
			api.Event{Kind: api.EventToolUse, ToolCallID: "call-2", Tool: "invoice_delete", Input: map[string]any{"id": "inv-2"}},
			approvalEvent("call-2", "invoice_delete"),
			api.Event{Kind: api.EventResult, Success: true, ToolApproval: state},
		)
		Expect(err).To(MatchError(`streamed approval request "call-2" is absent from the approval state`))
	})

	DescribeTable("fails loud and emits an error part for malformed correlation",
		func(events []api.Event, message string) {
			recorder, err := recordEvents(events...)
			Expect(err).To(MatchError(message))
			parts := decodedDataLines(recorder.Body.String())
			Expect(partTypes(parts)).To(ContainElement("error"))
			Expect(parts[len(parts)-1]).To(HaveKeyWithValue("type", "finish"))
			Expect(recorder.Body.String()).To(HaveSuffix("data: [DONE]\n\n"))
		},
		Entry("missing tool call id", []api.Event{{Kind: api.EventToolUse, Tool: "invoice_get"}}, `tool use "invoice_get" has no tool call id`),
		Entry("duplicate tool call", []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_get"},
			{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_get"},
		}, `duplicate tool call id "call-1"`),
		Entry("orphan permission", []api.Event{approvalEvent("call-1", "invoice_get")}, `permission for tool call "call-1" has no matching tool use`),
		Entry("orphan result", []api.Event{{Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "invoice_get"}}, `result for tool call "call-1" has no matching tool use`),
		Entry("duplicate permission", []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update"},
			approvalEvent("call-1", "invoice_update"),
			approvalEvent("call-1", "invoice_update"),
		}, `duplicate permission for tool call "call-1"`),
		Entry("mismatched tool name", []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_get"},
			{Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "invoice_delete"},
		}, `result for tool call "call-1" names "invoice_delete", want "invoice_get"`),
		Entry("terminal result while approval is unresolved", []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_update"},
			approvalEvent("call-1", "invoice_update"),
			{Kind: api.EventResult, Success: true},
		}, `tool call "call-1" ended without a result`),
		Entry("dangling tool", []api.Event{{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "invoice_get"}}, `tool call "call-1" ended without a result or approval request`),
	)
})

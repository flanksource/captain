package api_test

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resumable tool approval", func() {
	validState := func() api.ToolApprovalState {
		return api.ToolApprovalState{
			Messages: []api.Message{
				{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Update and inspect the invoice."}}},
				{Role: api.RoleAssistant, Parts: []api.Part{
					{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "call-update", Name: "invoice_update", Input: json.RawMessage(`{"amount":10}`)}},
					{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "call-read", Name: "invoice_get", Input: json.RawMessage(`{"id":"inv-1"}`)}},
				}},
			},
			Calls: []api.ToolApprovalCall{
				{Request: api.ToolApprovalRequest{ToolCallID: "call-update", Tool: "invoice_update", Input: json.RawMessage(`{"amount":10}`)}},
				{
					Request: api.ToolApprovalRequest{ToolCallID: "call-read", Tool: "invoice_get", Input: json.RawMessage(`{"id":"inv-1"}`)},
					Result:  &api.ToolResult{ToolCallID: "call-read", Output: json.RawMessage(`{"amount":10}`)},
				},
			},
		}
	}

	It("round-trips pending and already-completed calls", func() {
		state := validState()
		data, err := json.Marshal(state)
		Expect(err).NotTo(HaveOccurred())

		var decoded api.ToolApprovalState
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())
		Expect(decoded.Calls).To(Equal(state.Calls))
		Expect(decoded.Pending()).To(Equal([]api.ToolApprovalRequest{state.Calls[0].Request}))
	})

	It("accepts one approve decision with edited input for each pending call", func() {
		resume := api.ToolApprovalResume{
			State: validState(),
			Decisions: []api.ToolApprovalDecision{{
				ToolCallID: "call-update",
				Tool:       "invoice_update",
				Action:     api.ToolApprovalApprove,
				Input:      json.RawMessage(`{"amount":12}`),
			}},
		}
		Expect(resume.Validate()).To(Succeed())

		spec := api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAPI}, ToolApproval: &resume}
		Expect(spec.Validate()).To(Succeed())
	})

	It("compares tool inputs structurally regardless of object key order", func() {
		state := validState()
		state.Messages[1].Parts[0].ToolRequest.Input = json.RawMessage(`{"currency":"USD","amount":10}`)
		state.Calls[0].Request.Input = json.RawMessage(`{"amount":10,"currency":"USD"}`)
		Expect(state.Validate()).To(Succeed())

		state.Calls[0].Request.Input = json.RawMessage(`{"amount":11,"currency":"USD"}`)
		Expect(state.Validate()).To(MatchError(ContainSubstring("input does not match")))
	})

	DescribeTable("rejects invalid continuation decisions",
		func(mutate func(*api.ToolApprovalResume), want string) {
			resume := api.ToolApprovalResume{
				State: validState(),
				Decisions: []api.ToolApprovalDecision{{
					ToolCallID: "call-update", Tool: "invoice_update", Action: api.ToolApprovalApprove,
				}},
			}
			mutate(&resume)
			Expect(resume.Validate()).To(MatchError(ContainSubstring(want)))
		},
		Entry("missing", func(resume *api.ToolApprovalResume) { resume.Decisions = nil }, `missing decision for pending tool call "call-update"`),
		Entry("duplicate", func(resume *api.ToolApprovalResume) { resume.Decisions = append(resume.Decisions, resume.Decisions[0]) }, `duplicate decision for tool call "call-update"`),
		Entry("unknown call", func(resume *api.ToolApprovalResume) { resume.Decisions[0].ToolCallID = "call-other" }, `decision references unknown tool call "call-other"`),
		Entry("mismatched tool", func(resume *api.ToolApprovalResume) { resume.Decisions[0].Tool = "invoice_delete" }, `decision tool "invoice_delete" does not match pending tool "invoice_update"`),
		Entry("completed replay", func(resume *api.ToolApprovalResume) {
			resume.Decisions[0] = api.ToolApprovalDecision{ToolCallID: "call-read", Tool: "invoice_get", Action: api.ToolApprovalApprove}
		}, `tool call "call-read" is already resolved`),
	)

	It("requires deny and respond decisions to carry only their own payload", func() {
		state := validState()
		deny := api.ToolApprovalResume{State: state, Decisions: []api.ToolApprovalDecision{{
			ToolCallID: "call-update", Tool: "invoice_update", Action: api.ToolApprovalDeny, Message: "not now",
		}}}
		Expect(deny.Validate()).To(Succeed())

		respond := api.ToolApprovalResume{State: state, Decisions: []api.ToolApprovalDecision{{
			ToolCallID: "call-update", Tool: "invoice_update", Action: api.ToolApprovalRespond,
			Result: &api.ToolResult{ToolCallID: "call-update", Output: json.RawMessage(`{"queued":true}`)},
		}}}
		Expect(respond.Validate()).To(Succeed())

		respond.Decisions[0].Input = json.RawMessage(`{"amount":12}`)
		Expect(respond.Validate()).To(MatchError(ContainSubstring("respond decision cannot replace tool input")))

		approve := api.ToolApprovalResume{State: state, Decisions: []api.ToolApprovalDecision{{
			ToolCallID: "call-update", Tool: "invoice_update", Action: api.ToolApprovalApprove, Input: json.RawMessage(`[12]`),
		}}}
		Expect(approve.Validate()).To(MatchError(ContainSubstring("approve decision input: must be a JSON object")))
	})
})

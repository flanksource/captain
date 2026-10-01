package claudeagent

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// brokeredTurn is a provider with an active turn whose callback records every
// request it receives and answers with decision.
func brokeredTurn(decision ai.ApprovalDecision, calls *[]ai.ApprovalRequest) (*Provider, *turnState) {
	callback := func(_ context.Context, request ai.ApprovalRequest) (ai.ApprovalDecision, error) {
		*calls = append(*calls, request)
		return decision, nil
	}
	provider := &Provider{model: testModel, baseCtx: context.Background(), cfg: ai.Config{OnApproval: callback}}
	turn := &turnState{
		ctx:        context.Background(),
		inbox:      make(chan ai.Event, 4),
		term:       make(chan struct{}),
		quit:       make(chan struct{}),
		onApproval: callback,
	}
	provider.setActive(turn)
	return provider, turn
}

func canUseTool(provider *Provider, params string) canUseToolResult {
	raw, rpcErr := provider.handleCanUseTool(json.RawMessage(params))
	Expect(rpcErr).To(BeNil())
	result, ok := raw.(canUseToolResult)
	Expect(ok).To(BeTrue())
	return result
}

var _ = Describe("Claude Agent approval request kinds", func() {
	const toolUseID = "toolu-kind"

	DescribeTable("maps each canUseTool request onto one kind and payload",
		func(tool, input string, shape func(*ai.ApprovalRequest)) {
			var calls []ai.ApprovalRequest
			provider, _ := brokeredTurn(ai.ApprovalDecision{Allow: true}, &calls)

			canUseTool(provider, `{"tool":"`+tool+`","tool_use_id":"`+toolUseID+`","input":`+input+`}`)

			var decoded map[string]any
			Expect(json.Unmarshal([]byte(input), &decoded)).To(Succeed())
			expected := ai.ApprovalRequest{
				Tool: tool, Input: decoded, ToolUseID: toolUseID,
				Interruptible: true, SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest},
				Info: &api.ToolInfo{Name: tool}, LegacyContract: true,
			}
			shape(&expected)
			Expect(calls).To(Equal([]ai.ApprovalRequest{expected}))
			Expect(expected.Validate()).To(Succeed())
		},
		Entry("sandboxed Bash is a command", "Bash", `{"command":"ls -la"}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Command = api.ApprovalKindCommand, &api.CommandApproval{Command: "ls -la"}
		}),
		Entry("Bash escaping the sandbox is an escalating command", "Bash",
			`{"command":"gavel pr status 85 2>&1 | tail -60","dangerouslyDisableSandbox":true}`, func(r *ai.ApprovalRequest) {
				r.Kind, r.Escalates = api.ApprovalKindCommand, true
				r.Command = &api.CommandApproval{Command: "gavel pr status 85 2>&1 | tail -60", Unsandboxed: true}
			}),
		Entry("Edit is a filesystem edit", "Edit", `{"file_path":"pkg/api/a.go","old_string":"a","new_string":"b"}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Filesystem = api.ApprovalKindFilesystem, &api.FilesystemApproval{Operation: api.FilesystemEdit, Paths: []string{"pkg/api/a.go"}}
		}),
		Entry("MultiEdit is a filesystem edit", "MultiEdit", `{"file_path":"b.go","edits":[]}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Filesystem = api.ApprovalKindFilesystem, &api.FilesystemApproval{Operation: api.FilesystemEdit, Paths: []string{"b.go"}}
		}),
		Entry("Write is a filesystem write", "Write", `{"file_path":"c.go","content":"package c"}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Filesystem = api.ApprovalKindFilesystem, &api.FilesystemApproval{Operation: api.FilesystemWrite, Paths: []string{"c.go"}}
		}),
		Entry("NotebookEdit is a filesystem edit of the notebook", "NotebookEdit", `{"notebook_path":"n.ipynb","new_source":"x"}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Filesystem = api.ApprovalKindFilesystem, &api.FilesystemApproval{Operation: api.FilesystemEdit, Paths: []string{"n.ipynb"}}
		}),
		Entry("Read is a filesystem read", "Read", `{"file_path":"d.go"}`, func(r *ai.ApprovalRequest) {
			r.Kind, r.Filesystem = api.ApprovalKindFilesystem, &api.FilesystemApproval{Operation: api.FilesystemRead, Paths: []string{"d.go"}}
		}),
		Entry("WebFetch is network access to the URL's host", "WebFetch",
			`{"url":"https://code.claude.com/docs/en/plugins.md","prompt":"Plugin structure"}`, func(r *ai.ApprovalRequest) {
				r.Kind = api.ApprovalKindNetwork
				r.Network = &api.NetworkApproval{Host: "code.claude.com", Protocol: "https", URL: "https://code.claude.com/docs/en/plugins.md"}
			}),
		Entry("WebSearch names no host, so it stays a tool", "WebSearch", `{"query":"claude agent sdk"}`, func(r *ai.ApprovalRequest) {
			r.Kind = api.ApprovalKindTool
		}),
		Entry("Monitor runs a loop but is not Bash, so it stays a tool", "Monitor", `{"command":"while true; do :; done"}`, func(r *ai.ApprovalRequest) {
			r.Kind = api.ApprovalKindTool
		}),
		Entry("AskUserQuestion is a question", "AskUserQuestion",
			`{"questions":[{"header":"Scope","question":"How far?","multiSelect":false,"options":[{"label":"Phase 1","description":"Smallest cut"},{"label":"All"}]}]}`,
			func(r *ai.ApprovalRequest) {
				r.Kind = api.ApprovalKindQuestion
				r.Questions = []api.TerminalQuestion{{
					Text: "How far?", Context: "Scope", Options: []string{"Phase 1", "All"},
					OptionDescriptions: map[string]string{"Phase 1": "Smallest cut"},
				}}
			}),
		Entry("ExitPlanMode outside plan mode is a plan approval", "ExitPlanMode",
			`{"plan":"1. Add the plan kind\n2. Map ExitPlanMode","planFilePath":"/home/me/.claude/plans/unified.md"}`,
			func(r *ai.ApprovalRequest) {
				r.Kind = api.ApprovalKindPlan
				r.Plan = &api.TerminalPlan{Content: "1. Add the plan kind\n2. Map ExitPlanMode", Path: "/home/me/.claude/plans/unified.md"}
			}),
	)

	DescribeTable("refuses input its kind cannot describe, without asking the callback",
		func(tool, input, reason string) {
			var calls []ai.ApprovalRequest
			provider, _ := brokeredTurn(ai.ApprovalDecision{Allow: true}, &calls)

			result := canUseTool(provider, `{"tool":"`+tool+`","tool_use_id":"`+toolUseID+`","input":`+input+`}`)

			Expect(result).To(Equal(canUseToolResult{Allow: false, Message: result.Message}))
			Expect(result.Message).To(ContainSubstring(reason))
			Expect(calls).To(BeEmpty())
		},
		Entry("unparseable questions", "AskUserQuestion", `{"questions":"which?"}`, "questions must be an array"),
		Entry("a Bash call with no command", "Bash", `{"description":"nothing"}`, `Bash input has no "command"`),
		Entry("a file tool with no path", "Edit", `{"old_string":"a"}`, `Edit input has no "file_path"`),
		Entry("a WebFetch URL with no host", "WebFetch", `{"url":"not a url"}`, `WebFetch url "not a url" has no host`),
		Entry("an ExitPlanMode call with no plan", "ExitPlanMode", `{"planFilePath":"p.md"}`, "plan is required"),
	)
})

var _ = Describe("Claude Agent approval decisions", func() {
	bash := `{"tool":"Bash","tool_use_id":"toolu-decide","input":{"command":"curl example.com","dangerouslyDisableSandbox":true}}`

	It("carries a cancel to the bridge as a deny that interrupts the turn", func() {
		var calls []ai.ApprovalRequest
		provider, _ := brokeredTurn(ai.ApprovalDecision{Allow: false, Interrupt: true, Message: "no network from this run"}, &calls)

		result := canUseTool(provider, bash)

		Expect(result).To(Equal(canUseToolResult{Allow: false, Message: "no network from this run", Interrupt: true}))
		wire, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(wire)).To(MatchJSON(`{"allow":false,"message":"no network from this run","interrupt":true}`))
	})

	DescribeTable("denies with the validation error when the decision does not fit the request",
		func(decision ai.ApprovalDecision, reason string) {
			var calls []ai.ApprovalRequest
			provider, _ := brokeredTurn(decision, &calls)

			result := canUseTool(provider, bash)

			Expect(result).To(Equal(canUseToolResult{Allow: false, Message: result.Message}))
			Expect(result.Message).To(ContainSubstring(reason))
		},
		Entry("an approval that also interrupts", ai.ApprovalDecision{Allow: true, Interrupt: true}, "cannot interrupt the turn"),
		Entry("a scope Claude does not offer", ai.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}, `does not offer scope "session"`),
		Entry("grants on a command", ai.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{}}, "grants only answer an approved permissions request"),
	)

	It("leaves the permission event to the seam that binds the callback", func() {
		var calls []ai.ApprovalRequest
		provider, turn := brokeredTurn(ai.ApprovalDecision{Allow: true}, &calls)

		result := canUseTool(provider, bash)

		Expect(result.Allow).To(BeTrue())
		Expect(calls).To(HaveLen(1))
		Expect(turn.inbox).NotTo(Receive())
	})
})

var _ = Describe("Claude Agent approvals without an active turn", func() {
	params := `{"tool":"Bash","tool_use_id":"toolu-idle","input":{"command":"ls"}}`

	It("fails closed when a callback is attached", func() {
		provider := &Provider{model: testModel, baseCtx: context.Background(), cfg: ai.Config{
			OnApproval: func(context.Context, ai.ApprovalRequest) (ai.ApprovalDecision, error) {
				Fail("the callback has no turn to answer on")
				return ai.ApprovalDecision{}, nil
			},
		}}

		result := canUseTool(provider, params)

		Expect(result).To(Equal(canUseToolResult{Allow: false, Message: result.Message}))
		Expect(result.Message).To(ContainSubstring(`no active turn to answer the "Bash" approval`))
	})

	It("allows the tool when no callback brokers approvals", func() {
		provider := &Provider{model: testModel, baseCtx: context.Background()}

		result := canUseTool(provider, params)

		Expect(result).To(Equal(canUseToolResult{Allow: true, UpdatedInput: map[string]any{"command": "ls"}}))
	})
})

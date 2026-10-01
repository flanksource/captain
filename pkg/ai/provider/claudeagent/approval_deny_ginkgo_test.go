package claudeagent

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent approval deny floor", func() {
	handle := func(params string, calls *[]ai.ApprovalRequest) canUseToolResult {
		provider := &Provider{model: testModel, baseCtx: context.Background()}
		provider.setActive(&turnState{
			ctx:        context.Background(),
			inbox:      make(chan ai.Event, 4),
			term:       make(chan struct{}),
			quit:       make(chan struct{}),
			toolPolicy: aitools.ResolveOptions{Preferences: api.ToolPreferences{"Bash": api.ToolPolicyDeny}},
			onApproval: func(_ context.Context, request ai.ApprovalRequest) (ai.ApprovalDecision, error) {
				*calls = append(*calls, request)
				return ai.ApprovalDecision{Allow: true}, nil
			},
		})
		raw, rpcErr := provider.handleCanUseTool(json.RawMessage(params))
		Expect(rpcErr).To(BeNil())
		return raw.(canUseToolResult)
	}

	It("refuses a denied tool without asking the callback", func() {
		var calls []ai.ApprovalRequest
		result := handle(`{"tool":"Bash","tool_use_id":"toolu-1","input":{"command":"rm -rf build"}}`, &calls)
		Expect(result.Allow).To(BeFalse())
		Expect(result.Message).To(ContainSubstring(`tool "Bash" is denied`))
		Expect(calls).To(BeEmpty())
	})

	It("asks the callback about a tool the policy leaves open, as a legacy-contract request", func() {
		var calls []ai.ApprovalRequest
		result := handle(`{"tool":"Edit","tool_use_id":"toolu-2","input":{"file_path":"a.go"}}`, &calls)
		Expect(result.Allow).To(BeTrue())
		Expect(calls).To(ConsistOf(And(
			HaveField("Tool", "Edit"),
			HaveField("Kind", api.ApprovalKindFilesystem),
			HaveField("LegacyContract", true),
		)))
	})
})

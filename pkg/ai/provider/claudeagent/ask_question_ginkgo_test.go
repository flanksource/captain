package claudeagent

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent AskUserQuestion answers", func() {
	// The shape Claude actually sends: no question ids, so a dashboard can only
	// key its answers by position or by text.
	params := json.RawMessage(`{"tool":"AskUserQuestion","tool_use_id":"toolu-1","input":{"questions":[
		{"header":"Scope","question":"How far should this land?","multiSelect":false,"options":[{"label":"Phase 1","description":"Smallest cut"}]},
		{"header":"Areas","question":"Which areas?","multiSelect":true,"options":[{"label":"API","description":"Go"},{"label":"UI","description":"React"}]}
	]}}`)

	answer := func(decision ai.PermissionDecision, err error) (canUseToolResult, *string) {
		provider := &Provider{model: testModel, baseCtx: context.Background()}
		provider.setActive(&turnState{
			ctx:        context.Background(),
			inbox:      make(chan ai.Event, 4),
			term:       make(chan struct{}),
			quit:       make(chan struct{}),
			canUseTool: func(context.Context, ai.PermissionRequest) (ai.PermissionDecision, error) { return decision, err },
		})
		raw, rpcErr := provider.handleCanUseTool(params)
		Expect(rpcErr).To(BeNil())
		result, ok := raw.(canUseToolResult)
		Expect(ok).To(BeTrue())
		if result.Message == "" {
			return result, nil
		}
		return result, &result.Message
	}

	It("rebuilds the updated input with answers keyed by question text", func() {
		result, _ := answer(ai.PermissionDecision{Allow: true, UpdatedInput: map[string]any{
			"answers": map[string]any{"1": "Phase 1", "2": []any{"API", "UI"}},
			// The dashboard echoes the original questions back; the rebuild drops
			// its copy so the SDK never sees a changed or unknown field.
			"questions": []any{},
		}}, nil)

		Expect(result.Allow).To(BeTrue())
		Expect(result.UpdatedInput["questions"]).To(HaveLen(2))
		Expect(result.UpdatedInput["answers"]).To(Equal(map[string]any{
			"How far should this land?": "Phase 1",
			"Which areas?":              []string{"API", "UI"},
		}))
	})

	DescribeTable("accepts every identity a host can key an answer on",
		func(answers map[string]any) {
			result, _ := answer(ai.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"answers": answers}}, nil)
			Expect(result.Allow).To(BeTrue())
			Expect(result.UpdatedInput["answers"]).To(HaveKeyWithValue("How far should this land?", "Phase 1"))
		},
		Entry("by index", map[string]any{"1": "Phase 1", "2": []any{"API"}}),
		Entry("by text", map[string]any{"How far should this land?": "Phase 1", "Which areas?": []any{"API"}}),
	)

	It("denies rather than sending answers the SDK would drop", func() {
		result, message := answer(ai.PermissionDecision{Allow: true, UpdatedInput: map[string]any{
			"answers": map[string]any{"9": "Phase 1"},
		}}, nil)

		Expect(result.Allow).To(BeFalse())
		Expect(result.UpdatedInput).To(BeNil())
		Expect(message).NotTo(BeNil())
		Expect(*message).To(ContainSubstring(`question 1 "How far should this land?" has no answer`))
	})

	It("leaves a plain approval untouched", func() {
		result, _ := answer(ai.PermissionDecision{Allow: true}, nil)

		Expect(result.Allow).To(BeTrue())
		Expect(result.UpdatedInput).To(BeNil())
	})
})

package api_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	clickyentity "github.com/flanksource/clicky/entity"
)

func toolWithMethod(method string) api.ToolInfo {
	return api.ToolInfo{
		Name:      "tool_" + method,
		Operation: &clickyentity.RPCOperation{Name: "op", Method: method},
	}
}

func toolWithHints(readOnly, destructive *bool) api.ToolInfo {
	return api.ToolInfo{Name: "hinted", ReadOnlyHint: readOnly, DestructiveHint: destructive}
}

var _ = Describe("HTTPVerbStrategy", func() {
	DescribeTable("derives authority from the method",
		func(method string, want api.ToolPolicy, wantMatched bool) {
			policy, matched := api.HTTPVerbStrategy{}.Resolve(toolWithMethod(method))
			Expect(matched).To(Equal(wantMatched))
			Expect(policy).To(Equal(want))
		},
		Entry("GET reads", http.MethodGet, api.ToolPolicyAllow, true),
		Entry("HEAD reads", http.MethodHead, api.ToolPolicyAllow, true),
		Entry("OPTIONS reads", http.MethodOptions, api.ToolPolicyAllow, true),
		Entry("POST writes", http.MethodPost, api.ToolPolicyAsk, true),
		Entry("PUT writes", http.MethodPut, api.ToolPolicyAsk, true),
		Entry("PATCH writes", http.MethodPatch, api.ToolPolicyAsk, true),
		Entry("DELETE writes", http.MethodDelete, api.ToolPolicyAsk, true),
		Entry("an unrecognised method is not an opinion", "TRACE", api.ToolPolicyAuto, false),
	)

	It("has no opinion about a tool that projects no operation", func() {
		_, matched := api.HTTPVerbStrategy{}.Resolve(api.ToolInfo{Name: "mcp_tool"})
		Expect(matched).To(BeFalse())
	})

	It("reads a lower-case method, since the operation stores it verbatim", func() {
		policy, matched := api.HTTPVerbStrategy{}.Resolve(toolWithMethod("get"))
		Expect(matched).To(BeTrue())
		Expect(policy).To(Equal(api.ToolPolicyAllow))
	})
})

var _ = Describe("MCPHintStrategy", func() {
	// An undeclared hint is not a claim: a tool that never said whether it is
	// read-only must not inherit the answer given to one that said so.
	DescribeTable("derives authority from declared hints only",
		func(readOnly, destructive *bool, want api.ToolPolicy, wantMatched bool) {
			policy, matched := api.MCPHintStrategy{}.Resolve(toolWithHints(readOnly, destructive))
			Expect(matched).To(Equal(wantMatched))
			Expect(policy).To(Equal(want))
		},
		Entry("read-only and non-destructive auto-runs",
			boolPtr(true), boolPtr(false), api.ToolPolicyAllow, true),
		Entry("destructive asks",
			boolPtr(false), boolPtr(true), api.ToolPolicyAsk, true),
		Entry("read-only but destructive asks",
			boolPtr(true), boolPtr(true), api.ToolPolicyAsk, true),
		Entry("read-only alone is not enough to auto-run",
			boolPtr(true), nil, api.ToolPolicyAuto, false),
		Entry("a write that is not destructive is left to the next layer",
			boolPtr(false), boolPtr(false), api.ToolPolicyAuto, false),
		Entry("no hints at all is no opinion", nil, nil, api.ToolPolicyAuto, false),
	)
})

var _ = Describe("ResolveStrategies", func() {
	It("lets a later strategy override an earlier one", func() {
		// A GET the method would auto-run, which the operation itself says is
		// destructive. The hint is the later, better-informed answer.
		tool := api.ToolInfo{
			Name:            "dangerous_get",
			ReadOnlyHint:    boolPtr(false),
			DestructiveHint: boolPtr(true),
			Operation:       &clickyentity.RPCOperation{Name: "op", Method: http.MethodGet},
		}

		policy, matched := api.ResolveStrategies(api.DefaultStrategies(), tool)

		Expect(matched).To(BeTrue())
		Expect(policy).To(Equal(api.ToolPolicyAsk))
	})

	It("keeps an earlier answer when the later strategy has no opinion", func() {
		policy, matched := api.ResolveStrategies(api.DefaultStrategies(), toolWithMethod(http.MethodGet))

		Expect(matched).To(BeTrue())
		Expect(policy).To(Equal(api.ToolPolicyAllow))
	})

	It("reports no match when nothing in the chain has an opinion", func() {
		_, matched := api.ResolveStrategies(api.DefaultStrategies(), api.ToolInfo{Name: "opaque"})
		Expect(matched).To(BeFalse())
	})

	It("orders the default chain method-first so hints win", func() {
		Expect(api.DefaultStrategies()).To(HaveLen(2))
		Expect(api.DefaultStrategies()[0]).To(BeAssignableToTypeOf(api.HTTPVerbStrategy{}))
		Expect(api.DefaultStrategies()[1]).To(BeAssignableToTypeOf(api.MCPHintStrategy{}))
	})
})

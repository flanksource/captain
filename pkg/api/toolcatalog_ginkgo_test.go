package api_test

import (
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The publishers on the other side of the MCP _meta wire still speak the legacy
// vocabulary: clicky/entity.ToolPermission is on|off|ask|auto and
// clicky/mcp/registry copies it verbatim into _meta.defaultPermission. Reading
// those words as policies without the legacy mapping silently resolves them to
// auto, which turns "off" — a request to hide the tool — into a tool that is
// merely gated. These pin the mapping rather than the symptom.
var _ = Describe("ApplyToolMetadata", func() {
	entryWith := func(meta map[string]any) api.ToolCatalogEntry {
		entry := api.ToolCatalogEntry{Name: "invoice_delete", DefaultPermission: api.ToolPolicyAuto}
		api.ApplyToolMetadata(&entry, meta)
		return entry
	}

	DescribeTable("resolves the published permission",
		func(published string, want api.ToolPolicy) {
			Expect(entryWith(map[string]any{"defaultPermission": published}).DefaultPermission).To(Equal(want))
		},
		Entry("legacy off means deny, never auto", "off", api.ToolPolicyDeny),
		Entry("legacy on means allow, never auto", "on", api.ToolPolicyAllow),
		Entry("ask is spelled the same in both", "ask", api.ToolPolicyAsk),
		Entry("auto is spelled the same in both", "auto", api.ToolPolicyAuto),
		Entry("allow passes through", "allow", api.ToolPolicyAllow),
		Entry("deny passes through", "deny", api.ToolPolicyDeny),
		Entry("case and padding are folded", "  Off  ", api.ToolPolicyDeny),
		Entry("an unrecognised word falls back to auto", "sometimes", api.ToolPolicyAuto),
	)

	It("reads the nested clicky vendor block, where clicky actually publishes it", func() {
		entry := entryWith(map[string]any{
			"com.flanksource.clicky/tool": map[string]any{"defaultPermission": "off"},
		})
		Expect(entry.DefaultPermission).To(Equal(api.ToolPolicyDeny))
	})

	It("accepts the legacy defaultMode key too", func() {
		Expect(entryWith(map[string]any{"defaultMode": "on"}).DefaultPermission).To(Equal(api.ToolPolicyAllow))
	})
})

// A deny must remove the tool from the set the model is shown. This is the half
// of the contract ApplyToolMetadata's mapping exists to protect: if "off" ever
// resolves to auto again, the tool reappears here behind an approval prompt
// rather than being omitted, and nothing else in the stack would notice.
var _ = Describe("ToolPolicy.ApprovalDecision", func() {
	DescribeTable("maps a policy to an approval decision",
		func(policy api.ToolPolicy, wantRequire, wantHandled bool) {
			require, handled := policy.ApprovalDecision()
			Expect(handled).To(Equal(wantHandled))
			Expect(require).To(Equal(wantRequire))
		},
		Entry("ask requires approval", api.ToolPolicyAsk, true, true),
		Entry("allow runs unprompted", api.ToolPolicyAllow, false, true),
		Entry("deny is settled without an approval round-trip", api.ToolPolicyDeny, false, true),
		Entry("auto defers to the runtime gate", api.ToolPolicyAuto, false, false),
		Entry("an unset policy defers", api.ToolPolicy(""), false, false),
		Entry("a legacy spelling is not a policy and defers", api.ToolPolicy("on"), false, false),
	)
})

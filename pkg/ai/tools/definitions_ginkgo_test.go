package tools_test

import (
	"context"

	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Caller tool definitions", func() {
	noop := func(context.Context, map[string]any) (any, error) { return "ok", nil }

	It("resolves defaults and preferences through the one policy vocabulary", func() {
		readOnly, nonDestructive := true, false
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{
				Name: "invoice_get", ReadOnlyHint: &readOnly, DestructiveHint: &nonDestructive,
				DefaultPermission: api.ToolPolicyAuto, Handler: noop,
			},
			{Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk, Handler: noop},
			{Name: "invoice_delete", DefaultPermission: api.ToolPolicyDeny, Handler: noop},
		}, tools.ResolveOptions{Preferences: api.ToolPreferences{"invoice_update": api.ToolPolicyAllow}})

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(2))
		Expect(definitions[0].DefaultPermission).To(Equal(api.ToolPolicyAllow))
		Expect(definitions[1].DefaultPermission).To(Equal(api.ToolPolicyAllow))
	})

	It("resolves exact preferences before groups and omits disabled tools", func() {
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "invoice_list", Group: "billing", DefaultPermission: api.ToolPolicyAsk, Handler: noop},
			{Name: "invoice_delete", Group: "billing", DefaultPermission: api.ToolPolicyAllow, Handler: noop},
			{Name: "search", DefaultPermission: api.ToolPolicyDeny, Handler: noop},
		}, tools.ResolveOptions{Preferences: api.ToolPreferences{
			"billing":      api.ToolPolicyDeny,
			"invoice_list": api.ToolPolicyAllow,
			"search":       api.ToolPolicyAsk,
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(2))
		Expect(definitions[0].Name).To(Equal("invoice_list"))
		Expect(definitions[0].DefaultPermission).To(Equal(api.ToolPolicyAllow))
		Expect(definitions[1].Name).To(Equal("search"))
		Expect(definitions[1].DefaultPermission).To(Equal(api.ToolPolicyAsk))
	})

	It("validates definitions even when a preference disables them", func() {
		_, err := tools.ResolveDefinitions([]api.ToolDefinition{{
			Name: "search", DefaultPermission: "sometimes", Handler: noop,
		}}, tools.ResolveOptions{Preferences: api.ToolPreferences{"search": api.ToolPolicyDeny}})

		Expect(err).To(MatchError(ContainSubstring(`tool "search" has invalid default permission "sometimes"`)))
	})

	It("allows auto only for explicitly read-only non-destructive tools", func() {
		readOnly, nonDestructive := true, false
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{
				Name: "invoice_get", ReadOnlyHint: &readOnly, DestructiveHint: &nonDestructive,
				DefaultPermission: api.ToolPolicyAuto, Handler: noop,
			},
			{Name: "invoice_update", DefaultPermission: api.ToolPolicyAuto, Handler: noop},
		}, tools.ResolveOptions{})

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(2))
		Expect(definitions[0].DefaultPermission).To(Equal(api.ToolPolicyAllow))
		Expect(definitions[1].DefaultPermission).To(Equal(api.ToolPolicyAsk))
	})

	// The whole path an MCP server's declared permission travels: published in
	// _meta, folded onto the catalog entry, copied onto the definition, then
	// resolved into the set the model is shown. A tool published as "off" must
	// fall out of that set entirely — the projection sets no safety hints, so if
	// the legacy spelling ever resolves to auto again the tool comes back as an
	// ask rather than being omitted, and nothing downstream would catch it.
	DescribeTable("omits a tool an MCP server published as off",
		func(published string, wantNames []string) {
			entry := api.ToolCatalogEntry{Name: "invoice_delete", DefaultPermission: api.ToolPolicyAuto}
			api.ApplyToolMetadata(&entry, map[string]any{"defaultPermission": published})

			definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
				{Name: "invoice_get", DefaultPermission: api.ToolPolicyAsk, Handler: noop},
				{Name: entry.Name, DefaultPermission: entry.DefaultPermission, Handler: noop},
			}, tools.ResolveOptions{})

			Expect(err).NotTo(HaveOccurred())
			names := make([]string, 0, len(definitions))
			for _, definition := range definitions {
				names = append(names, definition.Name)
			}
			Expect(names).To(Equal(wantNames))
		},
		Entry("legacy off", "off", []string{"invoice_get"}),
		Entry("canonical deny", "deny", []string{"invoice_get"}),
		Entry("legacy on stays visible", "on", []string{"invoice_get", "invoice_delete"}),
		Entry("auto stays visible", "auto", []string{"invoice_get", "invoice_delete"}),
	)

	It("rejects duplicate and provider-unsafe tool names", func() {
		_, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "invoice_get", Handler: noop},
			{Name: "invoice_get", Handler: noop},
		}, tools.ResolveOptions{})
		Expect(err).To(MatchError(ContainSubstring(`duplicate caller tool "invoice_get"`)))

		_, err = tools.ResolveDefinitions([]api.ToolDefinition{{
			Name: "invoice/get", Handler: noop,
		}}, tools.ResolveOptions{})
		Expect(err).To(MatchError(ContainSubstring(`caller tool name "invoice/get"`)))
	})
})

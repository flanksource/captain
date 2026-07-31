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

	It("resolves exact preferences before groups and omits disabled tools", func() {
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "invoice_list", Group: "billing", DefaultPermission: api.ToolModeAsk, Handler: noop},
			{Name: "invoice_delete", Group: "billing", DefaultPermission: api.ToolModeOn, Handler: noop},
			{Name: "search", DefaultPermission: api.ToolModeOff, Handler: noop},
		}, api.ToolPreferences{
			"billing":      api.ToolModeOff,
			"invoice_list": api.ToolModeOn,
			"search":       api.ToolModeAsk,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(2))
		Expect(definitions[0].Name).To(Equal("invoice_list"))
		Expect(definitions[0].DefaultPermission).To(Equal(api.ToolModeOn))
		Expect(definitions[1].Name).To(Equal("search"))
		Expect(definitions[1].DefaultPermission).To(Equal(api.ToolModeAsk))
	})

	It("validates definitions even when a preference disables them", func() {
		_, err := tools.ResolveDefinitions([]api.ToolDefinition{{
			Name: "search", DefaultPermission: "sometimes", Handler: noop,
		}}, api.ToolPreferences{"search": api.ToolModeOff})

		Expect(err).To(MatchError(ContainSubstring(`tool "search" has invalid default permission "sometimes"`)))
	})

	It("allows auto only for explicitly read-only non-destructive tools", func() {
		readOnly, nonDestructive := true, false
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{
				Name: "invoice_get", ReadOnlyHint: &readOnly, DestructiveHint: &nonDestructive,
				DefaultPermission: api.ToolModeAuto, Handler: noop,
			},
			{Name: "invoice_update", DefaultPermission: api.ToolModeAuto, Handler: noop},
		}, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(2))
		Expect(definitions[0].DefaultPermission).To(Equal(api.ToolModeOn))
		Expect(definitions[1].DefaultPermission).To(Equal(api.ToolModeAsk))
	})

	It("rejects duplicate and provider-unsafe tool names", func() {
		_, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "invoice_get", Handler: noop},
			{Name: "invoice_get", Handler: noop},
		}, nil)
		Expect(err).To(MatchError(ContainSubstring(`duplicate caller tool "invoice_get"`)))

		_, err = tools.ResolveDefinitions([]api.ToolDefinition{{
			Name: "invoice/get", Handler: noop,
		}}, nil)
		Expect(err).To(MatchError(ContainSubstring(`caller tool name "invoice/get"`)))
	})
})

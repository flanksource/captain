package genkit

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	captools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Genkit tool policy", func() {
	noop := func(context.Context, map[string]any) (any, error) { return "ok", nil }

	It("resolves tool preferences before exposing tools", func() {
		defs := []api.ToolDefinition{
			{Name: "invoice_list", Group: "billing", DefaultPermission: api.ToolPolicyAsk, Handler: noop},
			{Name: "invoice_delete", Group: "billing", DefaultPermission: api.ToolPolicyAllow, Handler: noop},
			{Name: "search", DefaultPermission: api.ToolPolicyDeny, Handler: noop},
		}

		selected, err := resolveToolDefinitions(defs, captools.ResolveOptions{Preferences: api.ToolPreferences{
			"billing":      api.ToolPolicyDeny,
			"invoice_list": api.ToolPolicyAllow,
			"search":       api.ToolPolicyAsk,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(HaveLen(2))
		Expect(selected[0].Name).To(Equal("invoice_list"))
		Expect(selected[0].NeedsApproval()).To(BeFalse())
		Expect(selected[1].Name).To(Equal("search"))
		Expect(selected[1].NeedsApproval()).To(BeTrue())
	})

	It("lets tool-level ask override group-level on and invokes Captain approval", func() {
		approved := 0
		provider := newToolProvider(func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			approved++
			Expect(request.Tool).To(Equal("invoice_delete"))
			return api.PermissionDecision{Allow: true}, nil
		})
		def := api.ToolDefinition{
			Name: "invoice_delete", Group: "billing", DefaultPermission: api.ToolPolicyAllow, Handler: noop,
		}
		selected, err := resolveToolDefinitions([]api.ToolDefinition{def}, captools.ResolveOptions{Preferences: api.ToolPreferences{
			"billing":        api.ToolPolicyAllow,
			"invoice_delete": api.ToolPolicyAsk,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(HaveLen(1))

		_, err = runCorrelatedTool(provider, selected[0], map[string]any{}, func(ai.Event) {})
		Expect(err).NotTo(HaveOccurred())
		Expect(approved).To(Equal(1))
	})

	It("rejects invalid preferences instead of silently using defaults", func() {
		_, err := resolveToolDefinitions([]api.ToolDefinition{{Name: "search", Handler: noop}}, captools.ResolveOptions{Preferences: api.ToolPreferences{"search": "sometimes"}})
		Expect(err).To(MatchError(ContainSubstring(`invalid tool preference "sometimes" for "search"`)))
	})

	It("rejects an invalid tool default even when a preference disables the tool", func() {
		_, err := resolveToolDefinitions([]api.ToolDefinition{
			{Name: "search", DefaultPermission: "sometimes", Handler: noop},
		}, captools.ResolveOptions{Preferences: api.ToolPreferences{"search": api.ToolPolicyDeny}})
		Expect(err).To(MatchError(ContainSubstring(`tool "search" has invalid default permission "sometimes"`)))
	})

	It("caps Anthropic strict tools deterministically without dropping tools", func() {
		defs := make([]api.ToolDefinition, 0, anthropicMaxStrictTools+3)
		for i := 0; i < anthropicMaxStrictTools+2; i++ {
			defs = append(defs, api.ToolDefinition{
				Name: fmt.Sprintf("readonly_%02d", i), Strict: boolPointer(true), ReadOnlyHint: boolPointer(true), Handler: noop,
			})
		}
		defs = append(defs, api.ToolDefinition{
			Name: "destructive", Strict: boolPointer(true), DestructiveHint: boolPointer(true), Handler: noop,
		})

		shaped := anthropicStrictToolDefinitions(defs)
		Expect(shaped).To(HaveLen(len(defs)))
		strictNames := make([]string, 0, anthropicMaxStrictTools)
		for _, def := range shaped {
			Expect(def.Strict).NotTo(BeNil())
			if *def.Strict {
				strictNames = append(strictNames, def.Name)
			}
		}
		Expect(strictNames).To(HaveLen(anthropicMaxStrictTools))
		Expect(strictNames).To(ContainElement("destructive"))
		Expect(strictNames).NotTo(ContainElements("readonly_19", "readonly_20", "readonly_21"))
		Expect(*defs[0].Strict).To(BeTrue(), "shaping must not mutate Config.Tools")
	})

	It("writes explicit Anthropic strict metadata onto every Genkit tool", func() {
		provider := newToolProvider(nil)
		strict := api.ToolDefinition{Name: "strict", Strict: boolPointer(true), Handler: noop}
		loose := api.ToolDefinition{Name: "loose", Strict: boolPointer(false), Handler: noop}

		Expect(genkitStrictMetadata(provider.genkitTool(strict, nil, nil))).To(BeTrue())
		Expect(genkitStrictMetadata(provider.genkitTool(loose, nil, nil))).To(BeFalse())
	})
})

func genkitStrictMetadata(ref gkai.ToolRef) bool {
	tool, ok := ref.(gkai.Tool)
	Expect(ok).To(BeTrue())
	strict, ok := tool.Definition().Metadata["strict"].(bool)
	Expect(ok).To(BeTrue())
	return strict
}

func boolPointer(value bool) *bool { return &value }

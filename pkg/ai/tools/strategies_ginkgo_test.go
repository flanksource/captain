package tools_test

import (
	"context"
	"net/http"

	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	clickyentity "github.com/flanksource/clicky/entity"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The layering this suite pins is the whole contract, weakest to strongest:
// what the method implies, what the operation declares about itself, what its
// author registered, and finally what an operator's rules say.
var _ = Describe("Resolving a tool through the permission layers", func() {
	noop := func(context.Context, map[string]any) (any, error) { return "ok", nil }
	truth, falsehood := true, false

	// listInvoices is a GET with no hints, no registration and no rule: every
	// layer below the method is silent, so the method is the answer.
	listInvoices := func() api.ToolDefinition {
		return api.ToolDefinition{
			Name: "invoice_list", Handler: noop,
			Operation: &clickyentity.RPCOperation{
				Name: "invoice list", Method: http.MethodGet, Path: "/api/v1/invoice",
				Clicky: &clickyentity.ClickyOperationMeta{Entity: "invoice", Verb: "list", Scope: "collection"},
			},
		}
	}

	resolveOne := func(definition api.ToolDefinition, opts tools.ResolveOptions) (api.ToolPolicy, error) {
		resolved, err := tools.ResolveDefinitions([]api.ToolDefinition{definition}, opts)
		if err != nil {
			return "", err
		}
		if len(resolved) == 0 {
			return api.ToolPolicyDeny, nil
		}
		return resolved[0].DefaultPermission, nil
	}

	It("auto-runs a GET nothing else has an opinion about", func() {
		Expect(resolveOne(listInvoices(), tools.ResolveOptions{})).To(Equal(api.ToolPolicyAllow))
	})

	It("lets a destructive hint override the method", func() {
		definition := listInvoices()
		definition.ReadOnlyHint, definition.DestructiveHint = &falsehood, &truth

		Expect(resolveOne(definition, tools.ResolveOptions{})).To(Equal(api.ToolPolicyAsk))
	})

	It("lets the author's registration override what the facts imply", func() {
		definition := listInvoices()
		definition.DefaultPermission = api.ToolPolicyAsk

		Expect(resolveOne(definition, tools.ResolveOptions{})).To(Equal(api.ToolPolicyAsk))
	})

	It("lets a rule override the author's registration", func() {
		definition := listInvoices()
		definition.DefaultPermission = api.ToolPolicyAsk

		Expect(resolveOne(definition, tools.ResolveOptions{Policy: api.PermissionPolicy{
			{ToolMatch: api.ToolMatch{Verb: api.MatchPatterns{"list"}}, Policy: api.ToolPolicyAllow},
		}})).To(Equal(api.ToolPolicyAllow))
	})

	It("lets a later rule beat an earlier one", func() {
		Expect(resolveOne(listInvoices(), tools.ResolveOptions{Policy: api.PermissionPolicy{
			{ToolMatch: api.ToolMatch{Entity: api.MatchPatterns{"invoice"}}, Policy: api.ToolPolicyAsk},
			{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"invoice_list"}}, Policy: api.ToolPolicyAllow},
		}})).To(Equal(api.ToolPolicyAllow))
	})

	// The behaviour that was unreachable while clicky resolved a concrete answer
	// before captain saw the tool: `auto` is a refusal to decide, so the tool
	// falls back to what its own facts imply rather than to a blanket ask.
	It("hands a tool back to its facts when a rule says auto", func() {
		definition := listInvoices()
		definition.ReadOnlyHint, definition.DestructiveHint = &truth, &falsehood
		definition.DefaultPermission = api.ToolPolicyAsk

		Expect(resolveOne(definition, tools.ResolveOptions{Policy: api.PermissionPolicy{
			{ToolMatch: api.ToolMatch{Verb: api.MatchPatterns{"list"}}, Policy: api.ToolPolicyAuto},
		}})).To(Equal(api.ToolPolicyAsk))
	})

	It("omits a tool a rule denies", func() {
		resolved, err := tools.ResolveDefinitions([]api.ToolDefinition{listInvoices()}, tools.ResolveOptions{
			Policy: api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Scope: api.MatchPatterns{"collection"}}, Policy: api.ToolPolicyDeny},
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(BeEmpty())
	})

	It("asks about a tool no layer has an opinion on", func() {
		Expect(resolveOne(api.ToolDefinition{Name: "opaque", Handler: noop}, tools.ResolveOptions{})).
			To(Equal(api.ToolPolicyAsk))
	})

	It("does not select a tool that projects no operation with an operation rule", func() {
		Expect(resolveOne(api.ToolDefinition{Name: "mcp_tool", Handler: noop}, tools.ResolveOptions{
			Policy: api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Verb: api.MatchPatterns{"list"}}, Policy: api.ToolPolicyAllow},
			},
		})).To(Equal(api.ToolPolicyAsk))
	})

	// A deployment's own rules sit under the turn's. They must still beat the
	// tool's own facts — this is the defect the ordered list was built for: a
	// group baseline of "off" could never reach a live GET tool while the answer
	// was decided by whichever producer stamped the permission slot last.
	Describe("the deployment's base rules", func() {
		provider := func() api.ToolDefinition {
			definition := listInvoices()
			definition.Group = "provider.xero.read"
			return definition
		}
		baseline := api.PermissionPolicy{
			{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.xero.*"}}, Policy: api.ToolPolicyDeny},
		}

		It("hides a GET tool whose group baseline denies it", func() {
			resolved, err := tools.ResolveDefinitions([]api.ToolDefinition{provider()},
				tools.ResolveOptions{Base: baseline})

			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(BeEmpty())
		})

		It("still hides it when its author registered it as auto-run", func() {
			definition := provider()
			definition.DefaultPermission = api.ToolPolicyAllow

			Expect(resolveOne(definition, tools.ResolveOptions{Base: baseline})).To(Equal(api.ToolPolicyDeny))
		})

		It("yields to a per-turn preference that switches the group back on", func() {
			Expect(resolveOne(provider(), tools.ResolveOptions{
				Base:        baseline,
				Preferences: api.ToolPreferences{"provider.xero.read": api.ToolPolicyAsk},
			})).To(Equal(api.ToolPolicyAsk))
		})

		It("yields to a per-turn rule", func() {
			Expect(resolveOne(provider(), tools.ResolveOptions{
				Base: baseline,
				Policy: api.PermissionPolicy{
					{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.xero.read"}}, Policy: api.ToolPolicyAllow},
				},
			})).To(Equal(api.ToolPolicyAllow))
		})

		It("rejects a base rule that matches everything", func() {
			_, err := tools.ResolveDefinitions([]api.ToolDefinition{provider()}, tools.ResolveOptions{
				Base: api.PermissionPolicy{{Policy: api.ToolPolicyDeny}},
			})

			Expect(err).To(MatchError(ContainSubstring("at least one match facet")))
		})
	})

	It("honours a caller's own chain in place of the default", func() {
		// A deployment that refuses to read anything from the HTTP method: the
		// GET is no longer auto-run because nothing in this chain speaks for it.
		Expect(resolveOne(listInvoices(), tools.ResolveOptions{
			Strategies: []api.PermissionStrategy{api.MCPHintStrategy{}},
		})).To(Equal(api.ToolPolicyAsk))
	})
})

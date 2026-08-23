package api_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/flanksource/captain/pkg/api"
	clickyentity "github.com/flanksource/clicky/entity"
)

func boolPtr(v bool) *bool { return &v }

// xeroListTool is the shape the adversarial review's F1 defect turned on: a live
// list tool parented by a provider, projecting a clicky list operation.
func xeroListTool() api.ToolInfo {
	return api.ToolInfo{
		Name:         "xero_invoices_list",
		Group:        "provider.xero.read",
		Parent:       "xero",
		ReadOnlyHint: boolPtr(true),
		Operation: &clickyentity.RPCOperation{
			Name:   "invoices list",
			Method: http.MethodGet,
			Path:   "/api/v1/invoices",
			Clicky: &clickyentity.ClickyOperationMeta{
				Entity: "invoices", Verb: "list", Scope: "accounting",
			},
		},
	}
}

var _ = Describe("PermissionPolicy", func() {
	Describe("Resolve", func() {
		It("returns no match when no rule selects the tool", func() {
			policy := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"admin.*"}},
				Policy:    api.ToolPolicyDeny,
			}}

			_, matched := policy.Resolve(xeroListTool())

			Expect(matched).To(BeFalse())
		})

		// The layer order this list encodes runs weakest to strongest, so the last
		// matching rule is the intended answer. A first-match-wins reading would
		// invert the whole contract and make every user override unreachable.
		It("lets a later rule override an earlier one", func() {
			policy := api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.xero.*"}}, Policy: api.ToolPolicyDeny},
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"xero_invoices_list"}}, Policy: api.ToolPolicyAllow},
			}

			resolved, matched := policy.Resolve(xeroListTool())

			Expect(matched).To(BeTrue())
			Expect(resolved).To(Equal(api.ToolPolicyAllow))
		})

		// The F1 defect: a group baseline of `off` never reached the execution
		// path because a verb default was stamped into the same slot. Here the
		// group rule is the only rule, so it must decide.
		It("honours a group baseline that turns a whole provider off", func() {
			policy := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.xero.read"}},
				Policy:    api.ToolPolicyDeny,
			}}

			resolved, matched := policy.Resolve(xeroListTool())

			Expect(matched).To(BeTrue())
			Expect(resolved).To(Equal(api.ToolPolicyDeny))
		})

		It("requires every declared facet to match", func() {
			policy := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{
					Group:  api.MatchPatterns{"provider.xero.*"},
					Method: api.MatchPatterns{"POST"},
				},
				Policy: api.ToolPolicyDeny,
			}}

			_, matched := policy.Resolve(xeroListTool())

			Expect(matched).To(BeFalse())
		})
	})

	Describe("glob matching", func() {
		resolveWith := func(match api.ToolMatch) bool {
			_, matched := api.PermissionPolicy{{ToolMatch: match, Policy: api.ToolPolicyDeny}}.
				Resolve(xeroListTool())
			return matched
		}

		It("matches a suffix wildcard", func() {
			Expect(resolveWith(api.ToolMatch{Name: api.MatchPatterns{"xero_*"}})).To(BeTrue())
		})

		It("matches case-insensitively", func() {
			Expect(resolveWith(api.ToolMatch{Method: api.MatchPatterns{"get"}})).To(BeTrue())
		})

		It("accepts comma-separated alternatives in one pattern", func() {
			Expect(resolveWith(api.ToolMatch{Verb: api.MatchPatterns{"create,list,delete"}})).To(BeTrue())
		})

		// Negation takes precedence over any positive match, which is what lets a
		// rule say "this whole group except these".
		It("lets a negation veto a positive match", func() {
			Expect(resolveWith(api.ToolMatch{
				Group: api.MatchPatterns{"provider.xero.*", "!provider.xero.read"},
			})).To(BeFalse())
		})
	})

	Describe("hint facets", func() {
		It("matches a declared hint with the same value", func() {
			_, matched := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{ReadOnly: boolPtr(true)},
				Policy:    api.ToolPolicyAllow,
			}}.Resolve(xeroListTool())

			Expect(matched).To(BeTrue())
		})

		// "never said whether it is read-only" and "said it is not read-only" are
		// different claims. Treating the first as the second is how an unannotated
		// tool would inherit a permissive rule it was never meant to match.
		It("does not match a tool that never declared the hint", func() {
			tool := xeroListTool()
			tool.ReadOnlyHint = nil

			_, matched := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{ReadOnly: boolPtr(false)},
				Policy:    api.ToolPolicyAllow,
			}}.Resolve(tool)

			Expect(matched).To(BeFalse())
		})
	})

	Describe("Validate", func() {
		// A rule with no facet matches every tool and, being last-match-wins,
		// becomes the final word on all of them.
		It("rejects a rule that declares no match facet", func() {
			err := api.PermissionPolicy{{Policy: api.ToolPolicyDeny}}.Validate()

			Expect(err).To(MatchError(ContainSubstring("must declare at least one match facet")))
		})

		It("rejects an unrecognised policy", func() {
			err := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"Read"}},
				Policy:    api.ToolPolicy("sometimes"),
			}}.Validate()

			Expect(err).To(MatchError(ContainSubstring(`invalid policy "sometimes"`)))
		})

		It("reports the offending rule index", func() {
			err := api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"Read"}}, Policy: api.ToolPolicyAllow},
				{Policy: api.ToolPolicyDeny},
			}.Validate()

			Expect(err).To(MatchError(ContainSubstring("permission rule 1:")))
		})

		It("accepts a well-formed policy", func() {
			Expect(api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.*"}},
				Policy:    api.ToolPolicyAsk,
			}}.Validate()).To(Succeed())
		})
	})

	Describe("FromPreferences", func() {
		It("returns nothing for an empty map", func() {
			Expect(api.FromPreferences(nil)).To(BeEmpty())
		})

		// A preference key is ambiguous — PreferenceKey yields a group when the
		// tool has one and a name otherwise — so each key emits both forms, with
		// every group rule before every name rule. Sorting keeps the output
		// deterministic; map iteration order would make specs and tests flap.
		It("emits sorted group rules before sorted name rules", func() {
			policy := api.FromPreferences(api.ToolPreferences{
				"Write":    api.ToolPolicyDeny,
				"Bash":     api.ToolPolicyAsk,
				"provider": api.ToolPolicyAllow,
			})

			Expect(policy).To(Equal(api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"Bash"}}, Policy: api.ToolPolicyAsk},
				{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"Write"}}, Policy: api.ToolPolicyDeny},
				{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider"}}, Policy: api.ToolPolicyAllow},
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"Bash"}}, Policy: api.ToolPolicyAsk},
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"Write"}}, Policy: api.ToolPolicyDeny},
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"provider"}}, Policy: api.ToolPolicyAllow},
			}))
		})

		// The precedence the UI depends on: a per-tool toggle must beat the group
		// toggle above it, and the name-rules-last ordering is what delivers that.
		It("lets a name preference beat a group preference for the same tool", func() {
			policy := api.FromPreferences(api.ToolPreferences{
				"provider.xero.read": api.ToolPolicyDeny,
				"xero_invoices_list": api.ToolPolicyAllow,
			})

			resolved, matched := policy.Resolve(xeroListTool())

			Expect(matched).To(BeTrue())
			Expect(resolved).To(Equal(api.ToolPolicyAllow))
		})
	})

	Describe("Append", func() {
		It("layers a later policy over an earlier one without mutating either", func() {
			base := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.*"}},
				Policy:    api.ToolPolicyDeny,
			}}
			later := api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"xero_*"}},
				Policy:    api.ToolPolicyAllow,
			}}

			combined := base.Append(later)

			Expect(combined).To(HaveLen(2))
			Expect(base).To(HaveLen(1))
			resolved, matched := combined.Resolve(xeroListTool())
			Expect(matched).To(BeTrue())
			Expect(resolved).To(Equal(api.ToolPolicyAllow))
		})
	})

	Describe("decoding", func() {
		// `.prompt` frontmatter authors write a bare selector; requiring a list
		// there is ceremony that buys nothing.
		It("accepts a scalar or a list for a match facet", func() {
			var scalar api.PermissionRule
			Expect(yaml.Unmarshal([]byte("group: provider.xero.*\npolicy: deny\n"), &scalar)).To(Succeed())
			Expect(scalar.Group).To(Equal(api.MatchPatterns{"provider.xero.*"}))

			var list api.PermissionRule
			Expect(json.Unmarshal([]byte(`{"group":["a","b"],"policy":"deny"}`), &list)).To(Succeed())
			Expect(list.Group).To(Equal(api.MatchPatterns{"a", "b"}))
		})

		It("rejects a non-string match facet", func() {
			var rule api.PermissionRule
			Expect(json.Unmarshal([]byte(`{"group":{"x":1},"policy":"deny"}`), &rule)).
				To(MatchError(ContainSubstring("must be a string or a list of strings")))
		})
	})

	Describe("Spec round-trip", func() {
		spec := func() api.Spec {
			return api.Spec{ToolPolicy: api.PermissionPolicy{
				{ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"provider.*"}}, Policy: api.ToolPolicyDeny},
				{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{"Read"}, ReadOnly: boolPtr(true)}, Policy: api.ToolPolicyAllow},
			}}
		}

		It("survives a JSON round-trip through specMarshal", func() {
			encoded, err := json.Marshal(spec())
			Expect(err).NotTo(HaveOccurred())
			Expect(string(encoded)).To(ContainSubstring(`"toolPolicy"`))

			var decoded api.Spec
			Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
			Expect(decoded.ToolPolicy).To(Equal(spec().ToolPolicy))
		})

		It("survives a YAML round-trip through specMarshal", func() {
			encoded, err := yaml.Marshal(spec())
			Expect(err).NotTo(HaveOccurred())

			var decoded api.Spec
			Expect(yaml.Unmarshal(encoded, &decoded)).To(Succeed())
			Expect(decoded.ToolPolicy).To(Equal(spec().ToolPolicy))
		})

		It("omits an empty policy rather than emitting a null", func() {
			encoded, err := json.Marshal(api.Spec{})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(encoded)).NotTo(ContainSubstring("toolPolicy"))
		})

		It("rejects an invalid rule through Spec.Validate", func() {
			// Model and prompt are set so validation reaches the policy at all:
			// Spec.Validate checks both first and would otherwise fail for an
			// unrelated reason, and the test would pass without proving anything.
			invalid := api.Spec{
				Model:      api.Model{Name: "claude-sonnet-5"},
				Prompt:     api.Prompt{User: "summarize the ledger"},
				ToolPolicy: api.PermissionPolicy{{Policy: api.ToolPolicyDeny}},
			}

			Expect(invalid.Validate()).To(MatchError(ContainSubstring("must declare at least one match facet")))
		})
	})
})

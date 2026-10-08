package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A decoded override says two different things with an empty object: naming a
// section it supplies nothing inside ("permissions": {}) is not the same as
// deliberately emptying a collection within it ("tools": {}). Only the second
// clears — spec layers only ever supply values, so a request that mentions a
// section must not silently drop policy authored below it.
var _ = Describe("Decoded override presence", func() {
	base := func() Spec {
		return Spec{Permissions: Permissions{
			Mode:   PermissionPlan,
			Tools:  Tools{"Bash": ToolPolicyDeny},
			Skills: ResourcePolicies{"review": ResourceDisabled},
		}, Budget: Budget{Cost: 5, MaxTurns: 3}}
	}

	DescribeTable("decides what an empty object clears", func(encoded string, expect func(Spec)) {
		var override Spec
		Expect(json.Unmarshal([]byte(encoded), &override)).To(Succeed())

		expect(base().Merge(override))
	},
		Entry("a named section supplies nothing", `{"permissions":{}}`, func(merged Spec) {
			Expect(merged.Permissions).To(Equal(base().Permissions))
		}),
		Entry("an emptied collection clears only itself", `{"permissions":{"tools":{}}}`, func(merged Spec) {
			Expect(merged.Permissions.Tools).To(BeEmpty())
			Expect(merged.Permissions.Skills).To(Equal(ResourcePolicies{"review": ResourceDisabled}))
			Expect(merged.Permissions.Mode).To(Equal(PermissionPlan))
		}),
		Entry("a nulled collection clears only itself", `{"permissions":{"tools":null}}`, func(merged Spec) {
			Expect(merged.Permissions.Tools).To(BeEmpty())
			Expect(merged.Permissions.Mode).To(Equal(PermissionPlan))
		}),
		Entry("a named budget supplies nothing", `{"budget":{}}`, func(merged Spec) {
			Expect(merged.Budget).To(Equal(Budget{Cost: 5, MaxTurns: 3}))
		}),
		Entry("an explicit zero within a section still replaces", `{"budget":{"cost":0}}`, func(merged Spec) {
			Expect(merged.Budget.Cost).To(BeZero())
			Expect(merged.Budget.MaxTurns).To(Equal(3))
		}),
		Entry("a value within a named section still wins", `{"permissions":{"mode":"acceptEdits"}}`, func(merged Spec) {
			Expect(merged.Permissions.Mode).To(Equal(PermissionAcceptEdits))
			Expect(merged.Permissions.Tools).To(Equal(Tools{"Bash": ToolPolicyDeny}))
		}),
	)
})

package api

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runtime permission constraints", func() {
	DescribeTable("rejects a later permission widening with both layer names",
		func(constraints PermissionConstraints, request Spec, field string) {
			_, err := ComposeSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
				{Name: "project policy", Source: SpecLayerSourcePreset, Scope: SpecLayerGlobal, Constraints: RuntimeConstraints{Permissions: constraints}},
				RequestSpecLayer("request", request),
			}})
			Expect(err).To(MatchError(And(ContainSubstring(field), ContainSubstring("project policy"), ContainSubstring("request"))))
		},
		Entry("tool deny becomes allow",
			PermissionConstraints{Tools: Tools{"Bash": ToolPolicyDeny}},
			Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyAllow}}}, "permissions.tools.Bash"),
		Entry("plan becomes acceptEdits",
			PermissionConstraints{Mode: PermissionPlan},
			Spec{Permissions: Permissions{Mode: PermissionAcceptEdits}}, "permissions.mode"),
		Entry("plan becomes incomparable auto",
			PermissionConstraints{Mode: PermissionPlan},
			Spec{Permissions: Permissions{Mode: PermissionAuto}}, "permissions.mode"),
		Entry("dontAsk becomes incomparable default",
			PermissionConstraints{Mode: PermissionDontAsk},
			Spec{Permissions: Permissions{Mode: PermissionDefault}}, "permissions.mode"),
		Entry("disabled skill becomes enabled",
			PermissionConstraints{Skills: ResourcePolicies{"review": ResourceDisabled}},
			Spec{Permissions: Permissions{Skills: ResourcePolicies{"review": ResourceEnabled}}}, "permissions.skills.review"),
		Entry("sandbox leaves its allowlist",
			PermissionConstraints{SandboxModes: []SandboxKind{SandboxNative}},
			Spec{Sandbox: &SandboxRef{Mode: SandboxOff}}, "sandbox.mode"),
	)

	It("accepts permission and sandbox narrowing", func() {
		resolved, err := ComposeSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			{
				Name: "project policy", Scope: SpecLayerGlobal,
				Constraints: RuntimeConstraints{Permissions: PermissionConstraints{
					Mode: PermissionAcceptEdits, Tools: Tools{"Bash": ToolPolicyDeny},
					Skills: ResourcePolicies{"review": ResourceDisabled}, SandboxModes: []SandboxKind{SandboxNative, SandboxDocker},
				}},
			},
			RequestSpecLayer("request", Spec{
				Permissions: Permissions{Mode: PermissionPlan, Tools: Tools{"Bash": ToolPolicyDeny}, Skills: ResourcePolicies{"review": ResourceDisabled}},
				Sandbox:     &SandboxRef{Mode: SandboxDocker},
			}),
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Permissions.Mode).To(Equal(PermissionPlan))
		Expect(resolved.Spec.Sandbox.Mode).To(Equal(SandboxDocker))
	})

	It("intersects independently authored ceilings", func() {
		resolved, err := ComposeSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			{
				Name: "organization", Scope: SpecLayerGlobal,
				Constraints: RuntimeConstraints{Permissions: PermissionConstraints{
					Mode: PermissionAcceptEdits, Tools: Tools{"Bash": ToolPolicyDeny}, SandboxModes: []SandboxKind{SandboxNative, SandboxDocker},
				}},
			},
			{
				Name: "project", Scope: SpecLayerContext,
				Constraints: RuntimeConstraints{Permissions: PermissionConstraints{
					Mode: PermissionDefault, Tools: Tools{"Write": ToolPolicyDeny}, SandboxModes: []SandboxKind{SandboxDocker, SandboxGitAgent},
				}},
			},
			RequestSpecLayer("request", Spec{
				Permissions: Permissions{Mode: PermissionPlan, Tools: Tools{"Bash": ToolPolicyDeny, "Write": ToolPolicyDeny}},
				Sandbox:     &SandboxRef{Mode: SandboxDocker},
			}),
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Constraints.Permissions).To(Equal(PermissionConstraints{
			Mode: PermissionDefault, Tools: Tools{"Bash": ToolPolicyDeny, "Write": ToolPolicyDeny}, SandboxModes: []SandboxKind{SandboxDocker},
		}))
	})

	DescribeTable("rejects invalid permission constraints",
		func(constraints PermissionConstraints, message string) {
			_, err := ComposeSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{{
				Name: "project policy", Scope: SpecLayerGlobal, Constraints: RuntimeConstraints{Permissions: constraints},
			}}})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("tool constraint is not deny", PermissionConstraints{Tools: Tools{"Bash": ToolPolicyAllow}}, "must be deny"),
		Entry("tool constraint name is empty", PermissionConstraints{Tools: Tools{"": ToolPolicyDeny}}, "tool name is required"),
		Entry("skill constraint is not disabled", PermissionConstraints{Skills: ResourcePolicies{"review": ResourceEnabled}}, "must be disabled"),
		Entry("skill constraint name is empty", PermissionConstraints{Skills: ResourcePolicies{"": ResourceDisabled}}, "skill name is required"),
		Entry("sandbox mode is invalid", PermissionConstraints{SandboxModes: []SandboxKind{"invalid"}}, "sandbox mode"),
		Entry("mode is invalid", PermissionConstraints{Mode: "invalid"}, "permission mode"),
	)

	It("projects only restrictive fields from a spec", func() {
		constraints := PermissionConstraintsForSpec(Spec{
			Permissions: Permissions{
				Mode:   PermissionAcceptEdits,
				Tools:  Tools{"Bash": ToolPolicyDeny, "Read": ToolPolicyAllow},
				Skills: ResourcePolicies{"review": ResourceDisabled, "author": ResourceEnabled},
			},
			Sandbox: &SandboxRef{Mode: SandboxOff},
		})

		Expect(constraints).To(Equal(PermissionConstraints{
			Mode: PermissionAcceptEdits, Tools: Tools{"Bash": ToolPolicyDeny}, Skills: ResourcePolicies{"review": ResourceDisabled},
			SandboxModes: AllSandboxModes(),
		}))
	})

	It("adds authored restrictions to a layer without discarding its existing ceiling", func() {
		layer, err := ConstrainSpecLayerPermissions(SpecLayer{
			Name: "profile", Scope: SpecLayerSurface,
			Spec: Spec{Permissions: Permissions{Mode: PermissionPlan, Skills: ResourcePolicies{"review": ResourceDisabled}}},
			Constraints: RuntimeConstraints{Permissions: PermissionConstraints{
				Tools: Tools{"Bash": ToolPolicyDeny}, SandboxModes: []SandboxKind{SandboxNative},
			}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(layer.Constraints.Permissions).To(Equal(PermissionConstraints{
			Mode: PermissionPlan, Tools: Tools{"Bash": ToolPolicyDeny}, Skills: ResourcePolicies{"review": ResourceDisabled},
			SandboxModes: []SandboxKind{SandboxNative},
		}))
	})
})

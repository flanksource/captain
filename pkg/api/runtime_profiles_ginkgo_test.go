package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Runtime profiles", func() {
	It("resolves ordered preset layers before the task-specific profile spec", func() {
		request := api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID:   "review",
				Name: "Plan and review",
				Spec: api.Spec{
					Prompt: api.Prompt{User: "Review the final diff."},
					Budget: api.Budget{MaxTurns: 12},
				},
				Presets: []string{"personal", "organization", "plan"},
			},
			Presets: []api.RuntimePreset{
				{
					ID: "personal", Name: "Personal guardrails", Scope: api.SpecLayerUser,
					Spec: api.RuntimePresetSpec{ToolPolicy: api.PermissionPolicy{{
						ToolMatch: api.ToolMatch{Destructive: pointer(true)}, Policy: api.ToolPolicyAsk,
					}}},
				},
				{
					ID: "organization", Name: "Organization defaults", Scope: api.SpecLayerGlobal,
					Spec: api.RuntimePresetSpec{Model: api.Model{
						Name: "gpt-5.6-terra", Mode: api.ModeAgent,
					}, Budget: api.Budget{MaxTurns: 8}},
				},
				{
					ID: "plan", Name: "Plan mode", Scope: api.SpecLayerSurface,
					Spec: api.RuntimePresetSpec{
						Sandbox:     &api.SandboxRef{Mode: api.SandboxNative},
						Permissions: api.Permissions{Mode: api.PermissionPlan},
					},
				},
			},
		}

		resolved, err := api.ResolveRuntimeProfile(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Model.Name).To(Equal("gpt-5.6-terra"))
		Expect(resolved.Spec.Budget.MaxTurns).To(Equal(12))
		Expect(resolved.Spec.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(resolved.Spec.Prompt.User).To(Equal("Review the final diff."))
		Expect(resolved.Trace).To(HaveLen(4))
		Expect(resolved.Trace[0].ID).To(Equal("organization"))
		Expect(resolved.Trace[0].Source).To(Equal(api.SpecLayerSourcePreset))
		Expect(resolved.Trace[2].ID).To(Equal("review:spec"))
		Expect(resolved.Trace[2].Source).To(Equal(api.SpecLayerSourceProfile))
		Expect([]string{
			resolved.Trace[0].Name,
			resolved.Trace[1].Name,
			resolved.Trace[2].Name,
			resolved.Trace[3].Name,
		}).To(Equal([]string{
			"Organization defaults",
			"Plan mode",
			"Plan and review run spec",
			"Personal guardrails",
		}))
	})

	It("rejects a missing selected preset", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "review", Name: "Review", Presets: []string{"missing"},
			},
		})

		Expect(err).To(MatchError(ContainSubstring(`references missing preset "missing"`)))
	})

	It("resolves a preset referenced by name, matching case-insensitively", func() {
		resolved, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{ID: "review", Name: "Review", Presets: []string{"org defaults"}},
			Presets: []api.RuntimePreset{{
				ID: "preset-1", Name: "Org defaults", Scope: api.SpecLayerGlobal,
				Spec: api.RuntimePresetSpec{Budget: api.Budget{MaxTurns: 8}},
			}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Budget.MaxTurns).To(Equal(8))
		Expect(resolved.Trace[0].ID).To(Equal("preset-1"))
	})

	It("rejects a name reference that matches more than one preset", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{ID: "review", Name: "Review", Presets: []string{"Defaults"}},
			Presets: []api.RuntimePreset{
				{ID: "db-defaults", Name: "Defaults", Scope: api.SpecLayerGlobal},
				{ID: "file-defaults", Name: "defaults", Scope: api.SpecLayerUser},
			},
		})

		Expect(err).To(MatchError(ContainSubstring(`references preset "Defaults" by name, which matches 2 presets`)))
	})

	It("rejects a profile that selects the same preset by id and by name", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{ID: "review", Name: "Review", Presets: []string{"preset-1", "Org defaults"}},
			Presets: []api.RuntimePreset{{ID: "preset-1", Name: "Org defaults", Scope: api.SpecLayerGlobal}},
		})

		Expect(err).To(MatchError(ContainSubstring(`repeats preset "Org defaults"`)))
	})

	It("rejects a permission mode the resolved runtime cannot honour", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "codex", Name: "Codex", Spec: api.Spec{
					Model:       api.Model{Name: "gpt-5", Mode: api.ModeAgent},
					Permissions: api.Permissions{Mode: api.PermissionDontAsk},
				},
			},
		})

		Expect(err).To(MatchError(ContainSubstring("is not available for openai agent")))
	})

	// The posture is independent of isolation: a run with no sandbox at all must
	// still carry the mode it asked for, which folding it into SandboxRef broke.
	It("keeps a permission mode when no sandbox is configured", func() {
		resolved, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "plan", Name: "Plan", Spec: api.Spec{
					Model:       api.Model{Name: "claude", Mode: api.ModeAgent},
					Permissions: api.Permissions{Mode: api.PermissionPlan},
				},
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Sandbox).To(BeNil())
		Expect(resolved.Spec.Permissions.Mode).To(Equal(api.PermissionPlan))
	})

	It("rejects caller-tool policy on a runtime that cannot serve caller tools", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "cli", Name: "CLI", Spec: api.Spec{
					Model: api.Model{Name: "claude", Mode: api.ModeCLI},
					ToolPolicy: api.PermissionPolicy{{
						ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"billing.*"}},
						Policy:    api.ToolPolicyDeny,
					}},
				},
			},
		})

		Expect(err).To(MatchError(ContainSubstring(`caller-tool policy "deny" is not available for anthropic cli`)))
	})

	It("accepts brokered caller-tool policy when the backend supports caller tools", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "agent", Name: "Agent", Spec: api.Spec{
					Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent},
					ToolPolicy: api.PermissionPolicy{{
						ToolMatch: api.ToolMatch{Destructive: pointer(true)},
						Policy:    api.ToolPolicyAsk,
					}},
				},
			},
		})

		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects resource controls the resolved runtime drops", func() {
		_, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
			Profile: api.RuntimeProfile{
				ID: "codex", Name: "Codex", Spec: api.Spec{
					Model:       api.Model{Name: "codex", Mode: api.ModeCLI},
					Permissions: api.Permissions{MCP: api.MCP{Disabled: true}},
				},
			},
		})

		Expect(err).To(MatchError(ContainSubstring(`resource policy mcp=disabled is not available for openai cli`)))
	})
})

func pointer[T any](value T) *T { return &value }

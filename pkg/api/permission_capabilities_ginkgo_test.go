package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// codexRuntimes are the three transports that share api.CodexSafety.
var codexRuntimes = []api.Runtime{
	{Provider: "openai", Mode: api.ModeCLI},
	{Provider: "openai", Mode: api.ModeAgent},
	{Provider: "openai", Mode: api.ModeCmux},
}

// These three read (provider, mode) rather than a Runtime. Splitting the pair at
// the call site keeps the test iterating one canonical list — a second list keyed
// differently is exactly the drift this table exists to prevent.
func split(runtime api.Runtime) (*api.ModelProvider, api.RuntimeMode) {
	p, _ := runtime.ModelProvider()
	return p, runtime.Mode
}

func supportedModes(runtime api.Runtime) []api.PermissionMode {
	return api.SupportedPermissionModes(split(runtime))
}

func callerTools(runtime api.Runtime) bool { return api.SupportsCallerTools(split(runtime)) }

func provenances(runtime api.Runtime) []api.ToolProvenance {
	return api.ToolPolicyProvenances(split(runtime))
}

var _ = Describe("PermissionCapabilities", func() {
	Describe("completeness", func() {
		// The declaration is only useful if it is total: a missing cell reads as
		// "unsupported" through the accessors, which would quietly downgrade a
		// runtime rather than failing. Adding a provider×mode cell must force a decision here
		// the way tool_policy_support_test.go already forces one for ToolPolicy.
		It("declares a row for every runtime", func() {
			Expect(api.PermissionCapabilityRuntimes()).To(Equal(api.AllRuntimes()))
		})

		for _, runtime := range api.AllRuntimes() {
			Context(runtime.String(), func() {
				caps := api.PermissionCapabilitiesFor(runtime)

				It("declares every permission mode", func() {
					Expect(caps.Modes).To(HaveLen(len(api.AllPermissionModes())))
					for _, mode := range api.AllPermissionModes() {
						Expect(caps.Modes).To(HaveKey(mode))
						Expect(string(caps.Modes[mode].Kind)).ToNot(BeEmpty(),
							"mode %s has a zero Support, which is not a decision", mode)
					}
				})

				It("declares every policy value at every provenance", func() {
					for _, provenance := range api.AllToolProvenances() {
						Expect(caps.ToolPolicies).To(HaveKey(provenance))
						for _, policy := range api.AllToolPolicies() {
							Expect(string(caps.ToolPolicySupport(provenance, policy).Kind)).ToNot(BeEmpty(),
								"%s/%s has a zero Support", provenance, policy)
						}
					}
				})

				It("declares every resource kind in both directions", func() {
					for _, kind := range api.AllResourceKinds() {
						for _, mode := range api.AllResourceModes() {
							Expect(string(caps.ResourceSupport(kind, mode).Kind)).ToNot(BeEmpty(),
								"%s/%s has a zero Support", kind, mode)
						}
					}
				})

				It("explains every unsupported and approximated cell", func() {
					// An unexplained refusal is what the editor cannot render and a
					// reviewer cannot audit. `auto` is exempt: it constrains nothing,
					// so "unsupported" there is a vacuous cell, not a story.
					for mode, support := range caps.Modes {
						if support.Kind == api.SupportUnsupported || support.Kind == api.SupportApproximated {
							Expect(support.Effects.Note).ToNot(BeEmpty(),
								"mode %s is %s with no explanation", mode, support.Kind)
						}
					}
					for _, provenance := range api.AllToolProvenances() {
						for _, policy := range api.AllToolPolicies() {
							if policy == api.ToolPolicyAuto {
								continue
							}
							support := caps.ToolPolicySupport(provenance, policy)
							if support.Kind != api.SupportNative {
								Expect(support.Effects.Note).ToNot(BeEmpty(),
									"%s/%s is %s with no explanation", provenance, policy, support.Kind)
							}
						}
					}
				})
			})
		}

		It("fails closed for an unknown runtime", func() {
			caps := api.PermissionCapabilitiesFor(api.Runtime{Provider: "not-a-provider", Mode: "not-a-mode"})
			Expect(supportedModes(api.Runtime{Provider: "not-a-provider", Mode: "not-a-mode"})).To(BeEmpty())
			Expect(caps.ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportUnsupported))
			Expect(caps.ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind).
				To(Equal(api.SupportUnsupported))
		})
	})

	// The declaration claims to describe the mappers. These specs check it against
	// the one mapper that lives in this package; the claude and gemini halves are
	// pinned next to their own mappers, which is where they can be reached.
	Describe("agreement with the Codex sandbox translator", func() {
		for _, runtime := range codexRuntimes {
			Context(runtime.String(), func() {
				caps := api.PermissionCapabilitiesFor(runtime)

				for _, mode := range api.AllPermissionModes() {
					It("matches the declared approval for "+string(mode), func() {
						translation, err := api.TranslateCodexSandbox(runtime, &api.SandboxRef{
							Mode: api.SandboxNative,
						}, mode)
						support := caps.ModeSupport(mode)
						if support.Kind == api.SupportUnsupported {
							Expect(err).To(HaveOccurred())
							return
						}
						Expect(err).NotTo(HaveOccurred())
						Expect(translation.Sandbox).To(Equal(api.CodexSandboxReadOnly))
						Expect(support.Effects.Sandbox).To(BeEmpty())
						Expect(support.Effects.Approval).To(Equal(string(translation.Approval)))
					})
				}
			})
		}
	})

	Describe("supported vocabularies", func() {
		It("offers no posture on the API runtimes", func() {
			// The four API runtimes never read Permissions.Mode. Offering a posture
			// picker there is the F6 half of what this table exists to stop.
			for _, runtime := range []api.Runtime{
				{Provider: "anthropic", Mode: api.ModeAPI}, {Provider: "openai", Mode: api.ModeAPI},
				{Provider: "google", Mode: api.ModeAPI}, {Provider: "deepseek", Mode: api.ModeAPI},
			} {
				Expect(supportedModes(runtime)).To(BeEmpty(), runtime.String())
			}
		})

		It("offers every posture on every claude transport", func() {
			for _, runtime := range []api.Runtime{
				{Provider: "anthropic", Mode: api.ModeCLI}, {Provider: "anthropic", Mode: api.ModeAgent},
				{Provider: "anthropic", Mode: api.ModeCmux},
			} {
				Expect(supportedModes(runtime)).To(Equal(api.AllPermissionModes()), runtime.String())
			}
		})

		It("drops dontAsk on codex and keeps the rest", func() {
			for _, runtime := range codexRuntimes {
				Expect(supportedModes(runtime)).ToNot(ContainElement(api.PermissionDontAsk), runtime.String())
				Expect(supportedModes(runtime)).To(ContainElement(api.PermissionPlan), runtime.String())
			}
		})
	})

	// This is the finding the provenance dimension exists to express: a per-tool
	// deny is enforceable on codex-agent — by omission from the tool list captain
	// itself builds — even though codex has no --disallowedTools of its own.
	// A single runtime→bool cannot say that, and RequireToolPolicySupport refuses
	// the whole run today because of it.
	Describe("provenance", func() {
		It("enforces deny on a caller tool but not an agent built-in, on codex-agent", func() {
			caps := api.PermissionCapabilitiesFor(api.Runtime{Provider: "openai", Mode: api.ModeAgent})
			Expect(caps.ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportNative))
			Expect(caps.ToolPolicySupport(api.ProvenanceAgent, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportUnsupported))
		})

		It("enforces deny on an agent built-in but not a caller tool, on claude-cli", func() {
			caps := api.PermissionCapabilitiesFor(api.Runtime{Provider: "anthropic", Mode: api.ModeCLI})
			Expect(caps.ToolPolicySupport(api.ProvenanceAgent, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportNative))
			Expect(caps.ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportUnsupported))
		})

		It("declares caller-tool policy exactly where the registry declares caller tools", func() {
			// The caller row is not free-standing: it is enforceable precisely when
			// the adapter can carry caller-supplied tools at all. Deriving the
			// expectation from the registry keeps the two declarations from drifting
			// into disagreement.
			for _, runtime := range api.AllRuntimes() {
				enforced := api.PermissionCapabilitiesFor(runtime).
					ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind == api.SupportNative
				Expect(enforced).To(Equal(callerTools(runtime)), runtime.String())
			}
		})

		It("never claims per-tool policy over a third-party MCP server", func() {
			// Captain can stop a server loading; it cannot filter that server's tools
			// until a captain-owned gateway proxies them. Declaring it per provenance
			// means that arriving is a data change in this row.
			for _, runtime := range api.AllRuntimes() {
				Expect(provenances(runtime)).ToNot(ContainElement(api.ProvenanceMCP), runtime.String())
			}
		})

		It("reports ask as brokered wherever caller tools exist and unsupported elsewhere", func() {
			for _, runtime := range api.AllRuntimes() {
				want := api.SupportUnsupported
				if callerTools(runtime) {
					want = api.SupportRequiresBroker
				}
				Expect(api.PermissionCapabilitiesFor(runtime).
					ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyAsk).Kind).To(Equal(want), runtime.String())
			}
		})
	})

	// The resource axis is asymmetric today and the table has to say so, because
	// the opposite direction is accepted and then dropped in both cases.
	Describe("resources", func() {
		It("silences MCP only where a provider actually sends the empty server set", func() {
			for _, runtime := range api.AllRuntimes() {
				want := api.SupportUnsupported
				if runtime == (api.Runtime{Provider: "anthropic", Mode: api.ModeCLI}) || runtime == (api.Runtime{Provider: "openai", Mode: api.ModeAgent}) {
					want = api.SupportNative
				}
				Expect(api.PermissionCapabilitiesFor(runtime).
					ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind).To(Equal(want), runtime.String())
			}
		})

		It("loads skills only on claude-cli, and never unloads them anywhere", func() {
			for _, runtime := range api.AllRuntimes() {
				caps := api.PermissionCapabilitiesFor(runtime)
				want := api.SupportUnsupported
				if runtime == (api.Runtime{Provider: "anthropic", Mode: api.ModeCLI}) {
					want = api.SupportNative
				}
				Expect(caps.ResourceSupport(api.ResourceKindSkills, api.ResourceEnabled).Kind).
					To(Equal(want), runtime.String())
				Expect(caps.ResourceSupport(api.ResourceKindSkills, api.ResourceDisabled).Kind).
					To(Equal(api.SupportUnsupported), runtime.String())
			}
		})

		It("declares permissions.plugins inert on every runtime", func() {
			// The evidence for deleting the field in the next phase: it is declared
			// dead everywhere, and the matrix prints it.
			for _, runtime := range api.AllRuntimes() {
				caps := api.PermissionCapabilitiesFor(runtime)
				for _, mode := range api.AllResourceModes() {
					Expect(caps.ResourceSupport(api.ResourceKindPlugins, mode).Kind).
						To(Equal(api.SupportUnsupported), runtime.String())
				}
			}
		})
	})

	Describe("tool vocabulary", func() {
		It("gives each agent family its own built-in names", func() {
			// F15: the permission catalog served Claude's tool names for every
			// runtime. codex has never had a tool called Bash.
			Expect(api.PermissionCapabilitiesFor(api.Runtime{Provider: "anthropic", Mode: api.ModeCLI}).Tools).To(ContainElement("Bash"))
			Expect(api.PermissionCapabilitiesFor(api.Runtime{Provider: "openai", Mode: api.ModeCLI}).Tools).To(ContainElement("shell"))
			Expect(api.PermissionCapabilitiesFor(api.Runtime{Provider: "openai", Mode: api.ModeCLI}).Tools).ToNot(ContainElement("Bash"))
			Expect(api.PermissionCapabilitiesFor(api.Runtime{Provider: "google", Mode: api.ModeCLI}).Tools).To(ContainElement("run_shell_command"))
		})

		It("declares no built-in vocabulary for the API runtimes", func() {
			// An API runtime has no built-in tools at all: everything it can call is
			// caller-supplied. An empty list here is the honest answer, and it is why
			// the editor must not render an agent-tool tree there.
			for _, runtime := range []api.Runtime{
				{Provider: "anthropic", Mode: api.ModeAPI}, {Provider: "openai", Mode: api.ModeAPI},
				{Provider: "google", Mode: api.ModeAPI}, {Provider: "deepseek", Mode: api.ModeAPI},
			} {
				Expect(api.PermissionCapabilitiesFor(runtime).Tools).To(BeEmpty(), runtime.String())
			}
		})
	})
})

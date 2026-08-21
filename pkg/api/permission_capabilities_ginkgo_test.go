package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// codexBackends are the three transports that share api.CodexSafety.
var codexBackends = []api.Backend{api.BackendCodexCLI, api.BackendCodexAgent, api.BackendCodexCmux}

var _ = Describe("PermissionCapabilities", func() {
	Describe("completeness", func() {
		// The declaration is only useful if it is total: a missing cell reads as
		// "unsupported" through the accessors, which would quietly downgrade a
		// backend rather than failing. Adding a backend must force a decision here
		// the way tool_policy_support_test.go already forces one for ToolPolicy.
		It("declares a row for every backend", func() {
			Expect(api.PermissionCapabilityBackends()).To(Equal(api.AllBackends()))
		})

		for _, backend := range api.AllBackends() {
			Context(string(backend), func() {
				caps := api.PermissionCapabilitiesFor(backend)

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

		It("fails closed for an unknown backend", func() {
			caps := api.PermissionCapabilitiesFor(api.Backend("not-a-backend"))
			Expect(api.SupportedPermissionModes("not-a-backend")).To(BeEmpty())
			Expect(caps.ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportUnsupported))
			Expect(caps.ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind).
				To(Equal(api.SupportUnsupported))
		})
	})

	// The declaration claims to describe the mappers. These specs check it against
	// the one mapper that lives in this package; the claude and gemini halves are
	// pinned next to their own mappers, which is where they can be reached.
	Describe("agreement with CodexSafety", func() {
		for _, backend := range codexBackends {
			Context(string(backend), func() {
				caps := api.PermissionCapabilitiesFor(backend)

				for _, mode := range api.AllPermissionModes() {
					It("matches the declared posture for "+string(mode), func() {
						sandbox, approval := api.CodexSafety(api.Permissions{Mode: mode})
						support := caps.ModeSupport(mode)
						if support.Kind == api.SupportUnsupported {
							// dontAsk is declared unsupported precisely because it lands
							// on the read-only default rather than on anything resembling
							// "stop asking". Pinning that keeps the reason true.
							defSandbox, defApproval := api.CodexSafety(api.Permissions{})
							Expect(sandbox).To(Equal(defSandbox))
							Expect(approval).To(Equal(defApproval))
							return
						}
						Expect(support.Effects.Sandbox).To(Equal(string(sandbox)))
						Expect(support.Effects.Approval).To(Equal(string(approval)))
					})
				}
			})
		}
	})

	Describe("supported vocabularies", func() {
		It("offers no posture on the API backends", func() {
			// The four API backends never read Permissions.Mode. Offering a posture
			// picker there is the F6 half of what this table exists to stop.
			for _, backend := range []api.Backend{
				api.BackendAnthropic, api.BackendOpenAI, api.BackendGemini, api.BackendDeepSeek,
			} {
				Expect(api.SupportedPermissionModes(backend)).To(BeEmpty(), string(backend))
			}
		})

		It("offers every posture on every claude transport", func() {
			for _, backend := range []api.Backend{
				api.BackendClaudeCLI, api.BackendClaudeAgent, api.BackendClaudeCmux,
			} {
				Expect(api.SupportedPermissionModes(backend)).To(Equal(api.AllPermissionModes()), string(backend))
			}
		})

		It("drops dontAsk on codex and keeps the rest", func() {
			for _, backend := range codexBackends {
				Expect(api.SupportedPermissionModes(backend)).ToNot(ContainElement(api.PermissionDontAsk), string(backend))
				Expect(api.SupportedPermissionModes(backend)).To(ContainElement(api.PermissionPlan), string(backend))
			}
		})
	})

	// This is the finding the provenance dimension exists to express: a per-tool
	// deny is enforceable on codex-agent — by omission from the tool list captain
	// itself builds — even though codex has no --disallowedTools of its own.
	// A single backend→bool cannot say that, and RequireToolPolicySupport refuses
	// the whole run today because of it.
	Describe("provenance", func() {
		It("enforces deny on a caller tool but not an agent built-in, on codex-agent", func() {
			caps := api.PermissionCapabilitiesFor(api.BackendCodexAgent)
			Expect(caps.ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportNative))
			Expect(caps.ToolPolicySupport(api.ProvenanceAgent, api.ToolPolicyDeny).Kind).
				To(Equal(api.SupportUnsupported))
		})

		It("enforces deny on an agent built-in but not a caller tool, on claude-cli", func() {
			caps := api.PermissionCapabilitiesFor(api.BackendClaudeCLI)
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
			for _, backend := range api.AllBackends() {
				enforced := api.PermissionCapabilitiesFor(backend).
					ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyDeny).Kind == api.SupportNative
				Expect(enforced).To(Equal(api.SupportsCallerTools(backend)), string(backend))
			}
		})

		It("never claims per-tool policy over a third-party MCP server", func() {
			// Captain can stop a server loading; it cannot filter that server's tools
			// until a captain-owned gateway proxies them. Declaring it per provenance
			// means that arriving is a data change in this row.
			for _, backend := range api.AllBackends() {
				Expect(api.ToolPolicyProvenances(backend)).ToNot(ContainElement(api.ProvenanceMCP), string(backend))
			}
		})

		It("reports ask as brokered wherever caller tools exist and unsupported elsewhere", func() {
			for _, backend := range api.AllBackends() {
				want := api.SupportUnsupported
				if api.SupportsCallerTools(backend) {
					want = api.SupportRequiresBroker
				}
				Expect(api.PermissionCapabilitiesFor(backend).
					ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyAsk).Kind).To(Equal(want), string(backend))
			}
		})
	})

	// The resource axis is asymmetric today and the table has to say so, because
	// the opposite direction is accepted and then dropped in both cases.
	Describe("resources", func() {
		It("silences MCP only where a provider actually sends the empty server set", func() {
			for _, backend := range api.AllBackends() {
				want := api.SupportUnsupported
				if backend == api.BackendClaudeCLI || backend == api.BackendCodexAgent {
					want = api.SupportNative
				}
				Expect(api.PermissionCapabilitiesFor(backend).
					ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind).To(Equal(want), string(backend))
			}
		})

		It("loads skills only on claude-cli, and never unloads them anywhere", func() {
			for _, backend := range api.AllBackends() {
				caps := api.PermissionCapabilitiesFor(backend)
				want := api.SupportUnsupported
				if backend == api.BackendClaudeCLI {
					want = api.SupportNative
				}
				Expect(caps.ResourceSupport(api.ResourceKindSkills, api.ResourceEnabled).Kind).
					To(Equal(want), string(backend))
				Expect(caps.ResourceSupport(api.ResourceKindSkills, api.ResourceDisabled).Kind).
					To(Equal(api.SupportUnsupported), string(backend))
			}
		})

		It("declares permissions.plugins inert on every backend", func() {
			// The evidence for deleting the field in the next phase: it is declared
			// dead everywhere, and the matrix prints it.
			for _, backend := range api.AllBackends() {
				caps := api.PermissionCapabilitiesFor(backend)
				for _, mode := range api.AllResourceModes() {
					Expect(caps.ResourceSupport(api.ResourceKindPlugins, mode).Kind).
						To(Equal(api.SupportUnsupported), string(backend))
				}
			}
		})
	})

	Describe("tool vocabulary", func() {
		It("gives each agent family its own built-in names", func() {
			// F15: the permission catalog served Claude's tool names for every
			// backend. codex has never had a tool called Bash.
			Expect(api.PermissionCapabilitiesFor(api.BackendClaudeCLI).Tools).To(ContainElement("Bash"))
			Expect(api.PermissionCapabilitiesFor(api.BackendCodexCLI).Tools).To(ContainElement("shell"))
			Expect(api.PermissionCapabilitiesFor(api.BackendCodexCLI).Tools).ToNot(ContainElement("Bash"))
			Expect(api.PermissionCapabilitiesFor(api.BackendGeminiCLI).Tools).To(ContainElement("run_shell_command"))
		})

		It("declares no built-in vocabulary for the API backends", func() {
			// An API backend has no built-in tools at all: everything it can call is
			// caller-supplied. An empty list here is the honest answer, and it is why
			// the editor must not render an agent-tool tree there.
			for _, backend := range []api.Backend{
				api.BackendAnthropic, api.BackendOpenAI, api.BackendGemini, api.BackendDeepSeek,
			} {
				Expect(api.PermissionCapabilitiesFor(backend).Tools).To(BeEmpty(), string(backend))
			}
		})
	})
})

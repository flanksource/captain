package promptrun_test

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("promptrun.Preflight runtimes", func() {
	var in promptrun.Input
	BeforeEach(func() {
		in = promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, Prompt: api.Prompt{User: "review"}}},
			Timeout:  time.Minute,
		}
	})

	It("keeps the declared deadline the caller's timeout does not shorten", func() {
		in.Resolved.Spec.Budget = api.Budget{Cost: 1, MaxTokens: 100, MaxTurns: 2, Timeout: "30s"}
		in.Timeout = time.Hour

		_, err := promptrun.Preflight(in)

		Expect(err).NotTo(HaveOccurred())
		Expect(in.Resolved.Spec.Budget.Timeout).To(Equal("30s"))
	})

	It("rejects a malformed deadline rather than running unbounded", func() {
		in.Resolved.Spec.Budget = api.Budget{Timeout: "tomorrow"}

		_, err := promptrun.Preflight(in)

		Expect(err).To(MatchError(ContainSubstring("timeout")))
	})

	DescribeTable("rejects incompatible sandbox declarations",
		func(model api.Model, sandbox api.SandboxRef, message string) {
			in.Resolved.Spec.Model = model
			in.Resolved.Spec.Sandbox = &sandbox
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("unsupported primary", api.Model{Name: "gpt-5", Mode: api.ModeAPI}, api.SandboxRef{Mode: api.SandboxNative}, "sandbox mode"),
		Entry("invalid mode", api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, api.SandboxRef{Mode: "invalid"}, "sandbox"),
		Entry("unsupported fallback", api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent, Fallbacks: api.ModelList{{Name: "gpt-5", Mode: api.ModeAPI}}}, api.SandboxRef{Mode: api.SandboxNative}, "gpt-5"),
	)

	It("accepts a supported sandbox without starting setup", func() {
		in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxNative}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})

	It("reports fallback capability warnings using the fallback runtime", func() {
		in.Resolved.Spec.Model.Mode = api.ModeCLI
		in.Resolved.Spec.Fallbacks = api.ModelList{{Name: "gpt-5", Mode: api.ModeAgent}}
		in.Resolved.Spec.Permissions.Skills = api.ResourcePolicies{"review-tools": api.ResourceEnabled}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(ConsistOf(And(ContainSubstring("fallback[0]"), ContainSubstring("skills=enabled"), ContainSubstring("openai agent"))))
	})
})

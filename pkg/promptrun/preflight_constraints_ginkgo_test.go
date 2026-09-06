package promptrun_test

import (
	"context"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("promptrun.Preflight constraints and runtimes", func() {
	var in promptrun.Input
	BeforeEach(func() {
		in = promptrun.Input{
			Request: api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, Prompt: api.Prompt{User: "review"}},
			Timeout: time.Minute,
		}
	})

	DescribeTable("rejects a budget that the dispatched input would exceed",
		func(budget, limit api.Budget, callerTimeout time.Duration, message string) {
			in.Request.Budget = budget
			in.Constraints.Limits.Budget = limit
			in.Timeout = callerTimeout
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
			_, runErr := promptrun.Run(context.Background(), in)
			Expect(runErr).To(MatchError(err.Error()))
			Expect(in.Request.Budget).To(Equal(budget))
		},
		Entry("unbounded cost", api.Budget{}, api.Budget{Cost: 1}, time.Minute, "cost"),
		Entry("excess cost", api.Budget{Cost: 2}, api.Budget{Cost: 1}, time.Minute, "cost"),
		Entry("excess output tokens", api.Budget{MaxTokens: 200}, api.Budget{MaxTokens: 100}, time.Minute, "maxTokens"),
		Entry("unbounded turns", api.Budget{}, api.Budget{MaxTurns: 2}, time.Minute, "maxTurns"),
		Entry("declared deadline exceeds cap", api.Budget{Timeout: "2m"}, api.Budget{Timeout: "1m"}, time.Second, "timeout"),
		Entry("caller deadline exceeds cap", api.Budget{}, api.Budget{Timeout: "1m"}, 2*time.Minute, "timeout"),
	)

	It("uses the declared deadline and actual tighter budget within limits", func() {
		in.Request.Budget = api.Budget{Cost: 1, MaxTokens: 100, MaxTurns: 2, Timeout: "30s"}
		in.Constraints.Limits.Budget = api.Budget{Cost: 2, MaxTokens: 200, MaxTurns: 3, Timeout: "1m"}
		in.Timeout = time.Hour
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Request.Budget.Timeout).To(Equal("30s"))
	})

	It("returns the same permission-constraint refusal from preview and Run", func() {
		in.Request.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyAllow}
		in.Constraints.Permissions = api.PermissionConstraints{Tools: api.Tools{"Bash": api.ToolPolicyDeny}}

		_, previewErr := promptrun.Preflight(in)
		_, runErr := promptrun.Run(context.Background(), in)

		Expect(previewErr).To(MatchError(ContainSubstring("permissions.tools.Bash")))
		Expect(runErr).To(MatchError(previewErr.Error()))
	})

	DescribeTable("checks constraints against the actual run",
		func(mutate func(*promptrun.Input), message string) {
			mutate(&in)
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("model catalog", func(in *promptrun.Input) { in.Constraints.Models = []string{"gpt-5"} }, "effective model catalog"),
		Entry("fallback model catalog", func(in *promptrun.Input) {
			in.Constraints.Models = []string{"claude-sonnet-5"}
			in.Request.Fallbacks = api.ModelList{{Name: "gpt-5", Mode: api.ModeAgent}}
		}, "fallback model"),
		Entry("input ceiling counts append system", func(in *promptrun.Input) {
			in.Constraints.Limits.MaxInputTokens = 5
			in.Request.Prompt.AppendSystem = strings.Repeat("context ", 10)
		}, "input is about"),
		Entry("token quota", func(in *promptrun.Input) {
			in.Constraints.Quotas = []api.UsageQuota{{Name: "daily", Scope: api.SpecLayerGlobal, Layer: "workspace", TokenLimit: 10, TokensUsed: 10}}
		}, "quota"),
		Entry("negative ceiling", func(in *promptrun.Input) { in.Constraints.Limits.MaxInputTokens = -1 }, "non-negative"),
	)

	DescribeTable("rejects incompatible sandbox declarations",
		func(model api.Model, sandbox api.SandboxRef, message string) {
			in.Request.Model = model
			in.Request.Sandbox = &sandbox
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("unsupported primary", api.Model{Name: "gpt-5", Mode: api.ModeAPI}, api.SandboxRef{Mode: api.SandboxNative}, "sandbox mode"),
		Entry("invalid mode", api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, api.SandboxRef{Mode: "invalid"}, "sandbox"),
		Entry("unsupported fallback", api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent, Fallbacks: api.ModelList{{Name: "gpt-5", Mode: api.ModeAPI}}}, api.SandboxRef{Mode: api.SandboxNative}, "gpt-5"),
	)

	It("accepts a supported sandbox without starting setup", func() {
		in.Request.Sandbox = &api.SandboxRef{Mode: api.SandboxNative}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})

	It("reports fallback capability warnings using the fallback runtime", func() {
		in.Request.Model.Mode = api.ModeCLI
		in.Request.Fallbacks = api.ModelList{{Name: "gpt-5", Mode: api.ModeAgent}}
		in.Request.Permissions.Skills = api.ResourcePolicies{"review-tools": api.ResourceEnabled}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(ConsistOf(And(ContainSubstring("fallback[0]"), ContainSubstring("skills=enabled"), ContainSubstring("openai agent"))))
	})

	It("applies constraints to the enabled replacement construction would select", func() {
		previous := registry.Disabled()
		DeferCleanup(func() { registry.SetDisabled(previous) })
		registry.SetDisabled(registry.NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil))
		in.Constraints.Models = []string{"claude-sonnet-5"}
		_, err := promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring("effective model catalog")))
	})
})

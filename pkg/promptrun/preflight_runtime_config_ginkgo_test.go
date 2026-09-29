package promptrun_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("promptrun.Preflight runtime configuration", func() {
	var in promptrun.Input
	BeforeEach(func() {
		in = promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{Model: api.Model{Name: "sonnet", Mode: api.ModeAgent}, Prompt: api.Prompt{User: "review"}}},
			Timeout:  time.Minute,
		}
	})

	It("resolves a construction alias before comparing policies and judge models", func() {
		path := filepath.Join(GinkgoT().TempDir(), "judge.prompt")
		Expect(os.WriteFile(path, []byte("---\nmodel: claude-sonnet-5\n---\n{{role \"user\"}}\nReview."), 0o600)).To(Succeed())
		in.Resolved.Spec.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyDeny}
		in.Resolved.Spec.Workflow = &api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Resolved.Spec.Name).To(Equal("sonnet"))
	})

	DescribeTable("refuses invalid construction configuration",
		func(mutate func(*promptrun.Input), message string) {
			mutate(&in)
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
			_, runErr := promptrun.Run(context.Background(), in)
			Expect(runErr).To(MatchError(err.Error()))
		},
		Entry("unknown sandbox selection", func(in *promptrun.Input) { in.Config.SandboxSelection = &api.SandboxConfig{Kind: "invalid"} }, "sandbox"),
		Entry("missing external selection", func(in *promptrun.Input) { in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxDocker} }, "SandboxSelection"),
		Entry("mismatched external selection", func(in *promptrun.Input) {
			in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxGitAgent}
			in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxOff}
		}, "SandboxSelection"),
		Entry("unsupported sandbox selection", func(in *promptrun.Input) {
			in.Config.Model = api.Model{Name: "gpt-5", Mode: api.ModeAPI}
			in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxDocker}
		}, "docker"),
		Entry("invalid provider budget", func(in *promptrun.Input) { in.Config.Budget.Cost = -1 }, "budget"),
		Entry("invalid caller endpoint", func(in *promptrun.Input) { in.Config.CallerTools = &api.CallerToolEndpoint{Name: "review"} }, "caller-tool"),
		Entry("invalid scope", func(in *promptrun.Input) { in.Scope = "invalid" }, "scope"),
		Entry("invalid loop bound", func(in *promptrun.Input) { in.MaxIterations = -1 }, "MaxIterations"),
		Entry("unsupported Codex policy field", func(in *promptrun.Input) {
			in.Resolved.Spec.Model = api.Model{Name: "gpt-5", Mode: api.ModeAgent}
			in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{Network: &api.SandboxNetworkPolicy{AllowedDomains: []string{"example.com"}}}}
		}, "allowedDomains"),
		Entry("unsupported Claude policy field", func(in *promptrun.Input) {
			include := false
			in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{IncludeSystemTemp: &include}}}
		}, "includeSystemTemp"),
	)

	It("ignores construction configuration for a supplied provider", func() {
		in.Provider = &scriptedProvider{model: "claude-sonnet-5"}
		in.Config.SandboxSelection = &api.SandboxConfig{Kind: "invalid"}
		in.Config.Budget.Cost = -1
		in.Config.CallerTools = &api.CallerToolEndpoint{}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})

	It("warns for an absent approval broker and never calls an attached broker", func() {
		in.Resolved.Spec.ToolPreferences = api.ToolPreferences{"review": api.ToolPolicyAsk}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(ContainElement(ContainSubstring("OnApproval")))
		calls := 0
		in.Config.OnApproval = func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
			calls++
			return api.ApprovalDecision{}, nil
		}
		warnings, err = promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
		Expect(calls).To(BeZero())
	})

	It("validates an approval opt-in before admission and suppresses the missing broker warning", func() {
		in.Resolved.Spec.ToolPreferences = api.ToolPreferences{"review": api.ToolPolicyAsk}
		in.Approvals = &promptrun.ApprovalOptions{RequestedBy: "dashboard"}
		in.OnEvent = func(int, ai.Event) {}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())

		in.Config.OnApproval = func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
			return api.ApprovalDecision{}, nil
		}
		_, err = promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring("Config.OnApproval")))
		in.Config.OnApproval = nil

		in.OnEvent = nil
		_, err = promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring("OnEvent")))
		in.OnEvent = func(int, ai.Event) {}

		in.Provider = &scriptedProvider{model: "claude-sonnet-5"}
		_, err = promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring("Provider")))
		in.Provider = nil

		in.Approvals.RequestedBy = ""
		_, err = promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring("RequestedBy")))
	})

	It("reports a disabled skill still explicitly loaded through memory", func() {
		in.Resolved.Spec.Mode = api.ModeCLI
		in.Resolved.Spec.Permissions.Skills = api.ResourcePolicies{"review-tools": api.ResourceDisabled}
		in.Resolved.Spec.Memory.Skills = []string{"review-tools"}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(ContainElement(ContainSubstring("memory.skills")))
	})

	DescribeTable("refuses providerless verification whose isolation would never be applied",
		func(sandbox *api.SandboxRef, selection *api.SandboxConfig) {
			in.Resolved.Spec = api.Spec{Sandbox: sandbox, Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}}
			in.Config.SandboxSelection = selection
			var hookLog []string
			in.Hooks = []any{&recordingHook{name: "setup", log: &hookLog}}
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring("verify-only")))
			_, runErr := promptrun.Run(context.Background(), in)
			Expect(runErr).To(MatchError(err.Error()))
			Expect(hookLog).To(BeEmpty())
		},
		Entry("native policy", &api.SandboxRef{Mode: api.SandboxNative}, nil),
		Entry("Docker", &api.SandboxRef{Mode: api.SandboxDocker}, nil),
		Entry("Git Agent", &api.SandboxRef{Mode: api.SandboxGitAgent}, nil),
		Entry("configured boundary", nil, &api.SandboxConfig{Kind: api.SandboxDocker}),
	)

	DescribeTable("requires external selection to preserve authored restrictions",
		func(selection api.SandboxConfig, message string) {
			in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxGitAgent, Backend: "review-pool", Agent: "review-worker", Dispatch: &api.SandboxDispatchPolicy{MaxAttempts: 1, Paths: []string{"allowed/**"}}}
			in.Config.SandboxSelection = &selection
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("backend", api.SandboxConfig{Kind: api.SandboxGitAgent, Name: "other-pool"}, "backend"),
		Entry("agent", api.SandboxConfig{Kind: api.SandboxGitAgent, Name: "review-pool", Agent: "other-worker"}, "agent"),
		Entry("dispatch policy", api.SandboxConfig{Kind: api.SandboxGitAgent, Name: "review-pool", Agent: "review-worker"}, "dispatch"),
	)

	It("accepts an external selection with the exact authored restrictions", func() {
		in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxGitAgent, Backend: "review-pool", Agent: "review-worker", Dispatch: &api.SandboxDispatchPolicy{MaxAttempts: 1, Paths: []string{"allowed/**"}}}
		in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxGitAgent, Name: "review-pool", Agent: "review-worker", Dispatch: &api.SandboxDispatchPolicy{MaxAttempts: 1, Paths: []string{"allowed/**"}}}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})
})

package promptrun_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("promptrun.Preflight", func() {
	var in promptrun.Input
	var provider *scriptedProvider
	BeforeEach(func() {
		provider = &scriptedProvider{model: "claude-sonnet-5"}
		in = promptrun.Input{
			Request:  api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}, Prompt: api.Prompt{User: "review the change"}},
			Provider: provider, Timeout: time.Minute,
		}
	})
	AfterEach(func() { verify.Unregister(verify.KindFixture) })

	DescribeTable("refuses invalid input before dispatch",
		func(mutate func(*promptrun.Input), message string) {
			mutate(&in)
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
			_, runErr := promptrun.Run(context.Background(), in)
			Expect(runErr).To(MatchError(err.Error()))
			Expect(provider.Calls()).To(BeZero())
		},
		Entry("no work", func(in *promptrun.Input) { in.Request.Prompt = api.Prompt{} }, "workflow.verify"),
		Entry("no deadline", func(in *promptrun.Input) { in.Timeout = 0 }, "no timeout"),
		Entry("malformed declared deadline despite caller timeout", func(in *promptrun.Input) { in.Request.Budget.Timeout = "later" }, "timeout"),
		Entry("unresolved attachment", func(in *promptrun.Input) { in.Request.Prompt.Attachments = []api.AttachmentRef{{Path: "notes.txt"}} }, "not resolved"),
		Entry("missing fixture runner", func(in *promptrun.Input) {
			in.Request.Workflow = &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}
		}, "no fixture verifier is registered"),
		Entry("missing commit hook", func(in *promptrun.Input) { in.CallerOwnsCommits = true }, "nothing would commit"),
		Entry("empty judge declaration", func(in *promptrun.Input) {
			in.Request.Workflow = &api.Workflow{Verify: &api.Verify{Prompts: []string{" "}}}
		}, "prompts[0]"),
		Entry("missing judge file", func(in *promptrun.Input) {
			in.Request.Workflow = &api.Workflow{Verify: &api.Verify{Prompts: []string{filepath.Join(GinkgoT().TempDir(), "absent.prompt")}}}
		}, "absent.prompt"),
		Entry("invalid permission value", func(in *promptrun.Input) { in.Request.Permissions.Mode = "invalid" }, "invalid permission mode"),
		Entry("unsupported existing tool denial", func(in *promptrun.Input) {
			in.Provider = nil
			in.Config.Model = api.Model{Name: "gpt-5", Provider: api.OpenAI, Mode: api.ModeAPI}
			in.Request.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyDeny}
		}, "cannot enforce a per-tool policy"),
		Entry("missing generating model", func(in *promptrun.Input) { in.Provider = nil; in.Request.Model = api.Model{} }, "model"),
		Entry("unknown generating model", func(in *promptrun.Input) {
			in.Provider = nil
			in.Request.Model = api.Model{Name: "no-such-model-at-all"}
		}, "no-such-model-at-all"),
		Entry("invalid request tuning with supplied provider", func(in *promptrun.Input) {
			invalid := 3.0
			in.Request.Temperature = &invalid
		}, "temperature"),
		Entry("incompatible configured attachment", func(in *promptrun.Input) {
			in.Provider = nil
			in.Config.Model = api.Model{Name: "claude-sonnet-5", Mode: api.ModeCLI}
			in.Request.Prompt.Attachments = []api.AttachmentRef{preparedAttachment("image/png")}
		}, "image/png"),
	)

	It("has no provider, verifier, caller hook or event side effects", func() {
		factoryCalls := 0
		verify.Register(verify.KindFixture, func(context.Context, api.Verify, verify.Options) ([]*verify.Plugin, error) {
			factoryCalls++
			return nil, nil
		})
		var hookLog []string
		in.Request.Workflow = &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}
		in.Hooks = []any{&recordingHook{name: "caller", log: &hookLog}}
		in.CallerOwnsCommits = true
		events := 0
		in.OnEvent = func(int, ai.Event) { events++ }
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
		Expect([]int{provider.Calls(), factoryCalls, len(hookLog), events}).To(Equal([]int{0, 0, 0, 0}))
	})

	It("allows command and registered fixture verification without any model", func() {
		verify.Register(verify.KindFixture, func(context.Context, api.Verify, verify.Options) ([]*verify.Plugin, error) { return nil, nil })
		in.Provider = nil
		in.Request = api.Spec{Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}, Fixture: "acceptance"}}}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})

	It("uses the supplied provider instead of an ignored conflicting configuration", func() {
		in.Config.Model = api.Model{Name: "gpt-5", Provider: api.OpenAI, Mode: api.ModeAPI}
		in.Request.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyDeny}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
		result, err := promptrun.Run(context.Background(), in)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Passed).To(BeTrue())
	})

	DescribeTable("rejects unsupported judge overrides before constructing providers",
		func(frontmatter, message string) {
			path := filepath.Join(GinkgoT().TempDir(), "judge.prompt")
			Expect(os.WriteFile(path, []byte("---\n"+frontmatter+"\n---\n{{role \"user\"}}\nReview."), 0o600)).To(Succeed())
			in.Request.Workflow = &api.Workflow{Verify: &api.Verify{Prompts: []string{path}}}
			_, err := promptrun.Preflight(in)
			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(provider.Calls()).To(BeZero())
		},
		Entry("another model", "model: gpt-5", "declares model"),
		Entry("sandbox", "sandbox:\n  mode: off", "declares a sandbox"),
	)

	It("uses the explicit judge provider for model-free verification", func() {
		in.Provider = nil
		in.Verify.Provider = provider
		in.Request = api.Spec{Workflow: &api.Workflow{Verify: &api.Verify{Prompts: []string{writeJudgePrompt(GinkgoT().TempDir())}}}}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.Calls()).To(BeZero())
	})

	It("returns unsupported permissions as warnings without hiding invalid values", func() {
		in.Request.Permissions.Plugins = api.ResourcePolicies{"review-tools": api.ResourceEnabled}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(ContainElement(ContainSubstring("plugins")))
		Expect(provider.Calls()).To(BeZero())
		result, err := promptrun.Run(context.Background(), in)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Passed).To(BeTrue())
	})

	It("does not warn for skills disabled by omission", func() {
		in.Request.Permissions.Skills = api.ResourcePolicies{"review-tools": api.ResourceDisabled}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})
})

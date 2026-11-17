package cli

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/sandbox/substraterun"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type sharedSessionSandbox struct {
	wholeRunSandbox
	started chan api.Spec
	release chan struct{}
	session string
}

func (s *sharedSessionSandbox) RunRemote(ctx context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
	req := in.Resolved.Spec
	if err := started(req); err != nil {
		return promptrun.Result{}, err
	}
	s.started <- req
	if req.Prompt.User == "refused comment" {
		return promptrun.Result{}, errors.New("execution refused")
	}
	if req.Prompt.User == "first comment" {
		select {
		case <-ctx.Done():
			return promptrun.Result{}, ctx.Err()
		case <-s.release:
		}
	}
	return promptrun.Result{SessionID: s.session, Model: req.Model.Name, Response: &api.Response{Text: "done"}, Passed: true}, nil
}

var _ = Describe("shared prompt sessions", func() {
	var sandbox *sharedSessionSandbox
	var rendered PromptRenderResult
	var key string
	BeforeEach(func() {
		openCLIRecordDB()
		sandbox = &sharedSessionSandbox{started: make(chan api.Spec, 4), release: make(chan struct{}), session: uuid.NewString()}
		api.RegisterSandbox(api.SandboxSubstrate, func(api.SandboxConfig) (api.Sandbox, error) { return sandbox, nil })
		DeferCleanup(func() { api.RegisterSandbox(api.SandboxSubstrate, substraterun.Substrate) })
		DeferCleanup(func() {
			select {
			case <-sandbox.release:
			default:
				close(sandbox.release)
			}
		})
		model := api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI}
		rendered = testRenderedPrompt(model)
		rendered.Input.SetCwd(GinkgoT().TempDir())
		rendered.Input.Budget.Timeout = "10s"
		rendered.Input.Sandbox = &api.SandboxRef{Mode: api.SandboxSubstrate, Backend: "test-shared"}
		rendered.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxSubstrate, Name: "test-shared"}
		rendered.Input.Prompt.User = "first comment"
		key = uuid.NewString()
	})

	It("merges comments arriving during startup or execution into one conversation", func(ctx SpecContext) {
		first, err := launchAsyncRun(promptLaunch{Rendered: rendered, SessionKey: key})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(promptRuns.remove, first.RunID)
		Eventually(sandbox.started).Should(Receive(HaveField("Prompt.User", "first comment")))
		rendered.Input.Prompt.User = "second comment"
		second, err := launchAsyncRun(promptLaunch{Rendered: rendered, SessionKey: key})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(promptRuns.remove, second.RunID)
		Consistently(sandbox.started, 100*time.Millisecond).ShouldNot(Receive())
		close(sandbox.release)
		Eventually(sandbox.started).Should(Receive(And(HaveField("SessionID", sandbox.session), HaveField("Prompt.User", "second comment"))))
		for _, run := range []PromptRunResult{first, second} {
			summary, err := AwaitPromptRun(ctx, run.RunID)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary.Success).To(BeTrue())
			Expect(summary.SessionID).To(Equal(sandbox.session))
		}
	})

	It("starts a separate conversation when fresh is explicit, even while the old one is active", func(ctx SpecContext) {
		first, err := launchAsyncRun(promptLaunch{Rendered: rendered, SessionKey: key})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(promptRuns.remove, first.RunID)
		Eventually(sandbox.started).Should(Receive())
		rendered.Input.Prompt.User = "fresh comment"
		rendered.Input.SessionID, rendered.Config.SessionID = sandbox.session, sandbox.session
		fresh, err := launchAsyncRun(promptLaunch{Rendered: rendered, SessionKey: key, Fresh: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(promptRuns.remove, fresh.RunID)
		Eventually(sandbox.started).Should(Receive(HaveField("SessionID", "")))
		_, err = AwaitPromptRun(ctx, fresh.RunID)
		Expect(err).NotTo(HaveOccurred())
		close(sandbox.release)
		_, err = AwaitPromptRun(ctx, first.RunID)
		Expect(err).NotTo(HaveOccurred())
	})

	It("propagates a pre-provider execution failure through the launch callback", func(ctx SpecContext) {
		rendered.Input.Prompt.User = "refused comment"
		completed := make(chan PromptRunSummary, 1)
		run, err := launchAsyncRun(promptLaunch{Rendered: rendered, SessionKey: key, OnComplete: func(summary PromptRunSummary) { completed <- summary }})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(promptRuns.remove, run.RunID)
		_, err = AwaitPromptRun(ctx, run.RunID)
		Expect(err).To(MatchError(ContainSubstring("execution refused")))
		Eventually(completed).Should(Receive(And(HaveField("Success", false), HaveField("Error", ContainSubstring("execution refused")))))
	})
})

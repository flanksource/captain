package promptrun_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/commons-db/shell"
)

// testTimeout is the deadline every spec declares explicitly. promptrun.Run has
// no default of its own: a run whose spec declares no budget.timeout and whose
// host passes none is a run nobody bounded, and silently capping it at two
// minutes is how a 45-minute job died with no explanation.
const testTimeout = 2 * time.Minute

// scriptedProvider is a streaming provider that emits one scripted turn and
// counts how often it was asked to, so a spec can prove it was never called.
type scriptedProvider struct {
	mu    sync.Mutex
	calls int
	model string
}

func (p *scriptedProvider) GetModel() string { return p.model }
func (p *scriptedProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.Anthropic, api.ModeAgent)
}
func (p *scriptedProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &ai.Response{Text: "done", Model: p.model}, nil
}
func (p *scriptedProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	ch := make(chan ai.Event, 3)
	ch <- ai.Event{Kind: ai.EventSystem, SessionID: "sess-1", Model: p.model}
	ch <- ai.Event{Kind: ai.EventText, Text: "done", Model: p.model}
	ch <- ai.Event{Kind: ai.EventResult, Success: true, Model: p.model, Usage: &ai.Usage{InputTokens: 7, OutputTokens: 3}, CostUSD: 0.25}
	close(ch)
	return ch, nil
}
func (p *scriptedProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// recordingHook is a caller hook that notes every phase it is dispatched at.
type recordingHook struct {
	name string
	log  *[]string
}

func (h *recordingHook) Name() string { return h.name }
func (h *recordingHook) PreRun(*agent.HookContext) error {
	*h.log = append(*h.log, h.name+":prerun")
	return nil
}
func (h *recordingHook) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }
func (h *recordingHook) Post(_ *agent.HookContext, phase agent.Phase) error {
	*h.log = append(*h.log, h.name+":"+string(phase))
	return nil
}

// progressVerifier reports three in-flight snapshots and then passes; it is
// what a fixture runner looks like to the loop.
type progressVerifier struct{ progress func(api.VerifyReport) }

func (v *progressVerifier) SetProgress(fn func(api.VerifyReport)) { v.progress = fn }
func (v *progressVerifier) Verify(context.Context, string, []string) (verify.Verdict, error) {
	for i := 1; i <= 3; i++ {
		v.progress(api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: fmt.Sprintf("check %d", i), Running: true}))
	}
	final := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "check 3", Passed: true})
	return verify.Verdict{OK: true, Report: &final}, nil
}

func hookNames(hooks []any) []string {
	names := make([]string, 0, len(hooks))
	for _, h := range hooks {
		names = append(names, h.(interface{ Name() string }).Name())
	}
	return names
}

func writeJudgePrompt(dir string) string {
	path := filepath.Join(dir, "review.prompt")
	Expect(os.WriteFile(path, []byte("{{role \"user\"}}\nJudge the work in {{cwd}}."), 0o644)).To(Succeed())
	return path
}

var _ = Describe("promptrun.Run", func() {
	var (
		cwd      string
		provider *scriptedProvider
		request  ai.Request
	)

	BeforeEach(func() {
		cwd = GinkgoT().TempDir()
		provider = &scriptedProvider{model: "fake-model"}
		request = ai.Request{
			Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent},
			Prompt: api.Prompt{User: "fix it"},
		}
		request.SetCwd(cwd)
	})

	AfterEach(func() {
		verify.Unregister(verify.KindFixture)
	})

	Describe("hook order", func() {
		// The order is the run's safety contract: commit hooks lead so a PhaseRun
		// squash lands before any teardown; the workflow's own checks run cheap to
		// expensive; the caller's hooks sit between the checks and setup so a host
		// commit pipeline still sees a live worktree; setup trails so its teardown
		// is the last Post to fire.
		It("assembles commit → cmd → prompt → fixture → caller → setup", func() {
			verify.Register(verify.KindFixture, func(_ context.Context, spec api.Verify, _ verify.Options) ([]*verify.Plugin, error) {
				return []*verify.Plugin{verify.New("fixture:"+spec.Fixture, &progressVerifier{})}, nil
			})
			judge := writeJudgePrompt(cwd)
			request.Workflow = &api.Workflow{
				Verify:  &api.Verify{Commands: []string{"true"}, Prompts: []string{judge}, Fixture: "acceptance"},
				Commits: []api.Commit{{On: api.CommitOnRun}},
			}
			request.Setup = &shell.Setup{}
			var log []string

			hooks, err := promptrun.Hooks(context.Background(), promptrun.Input{
				Request: request,
				Hooks:   []any{&recordingHook{name: "host", log: &log}},
				Verify:  verify.Options{Provider: provider},
			}, provider)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookNames(hooks)).To(Equal([]string{
				"commit:run", "verify:true", "judge:" + judge, "fixture:acceptance", "host", "setup",
			}))
		})

		It("adds no setup hook when the caller supplies the provider that owns the workspace", func() {
			request.Setup = &shell.Setup{}
			hooks, err := promptrun.Hooks(context.Background(), promptrun.Input{Request: request, Provider: provider}, provider)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookNames(hooks)).To(BeEmpty())
		})

		It("refuses a fixture the process has no runner for", func() {
			request.Workflow = &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}
			_, err := promptrun.Hooks(context.Background(), promptrun.Input{Request: request}, provider)
			Expect(err).To(MatchError(ContainSubstring("no fixture verifier is registered")))
		})
	})

	Describe("verify-only", func() {
		// An empty prompt body means "judge the tree as it is". The provider is
		// never asked anything — and never even constructed when nothing needs
		// one, so a verify-only run works without a model to hand.
		It("runs the checks once, never calls the provider, and returns their report", func() {
			request.Prompt = api.Prompt{}
			request.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}

			res, err := promptrun.Run(context.Background(), promptrun.Input{Request: request, Provider: provider, Timeout: testTimeout})
			Expect(err).NotTo(HaveOccurred())
			Expect(provider.Calls()).To(BeZero())
			Expect(res.Passed).To(BeTrue())
			Expect(res.Report).NotTo(BeNil())
			Expect(res.Report.Passed).To(BeTrue())
			Expect(res.Report.Iteration).To(Equal(1))
			Expect(res.Verdicts).To(HaveLen(1))
			Expect(res.Loop).To(BeNil())
		})

		It("does not construct a provider when no check needs one", func() {
			request.Prompt = api.Prompt{}
			request.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"exit 3"}}}

			// A Config no provider can be built from: were one constructed, this
			// would fail there rather than at the verdict.
			res, err := promptrun.Run(context.Background(), promptrun.Input{Request: request, Config: ai.Config{}, Timeout: testTimeout})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Passed).To(BeFalse())
			Expect(res.Report.State).To(Equal(api.VerifyStateFailed))
			Expect(promptrun.FailureReason(res.Verdicts)).NotTo(BeEmpty())
		})

		// The runner used to call an empty prompt verify-only on its own, so a
		// request with attachments or a message history and nothing to verify built
		// a provider, generated nothing, and came back passed. There is one rule
		// (api.Spec.IsVerifyOnly) and anything outside it is an error.
		DescribeTable("refuses a request that neither generates nor verifies",
			func(mutate func(*ai.Request)) {
				request.Prompt = api.Prompt{}
				request.Workflow = nil
				mutate(&request)

				_, err := promptrun.Run(context.Background(), promptrun.Input{
					Request: request, Provider: provider, Timeout: testTimeout,
				})
				Expect(err).To(MatchError(ContainSubstring("workflow.verify")))
				Expect(provider.Calls()).To(BeZero())
			},
			Entry("nothing at all", func(*ai.Request) {}),
			Entry("attachments only", func(r *ai.Request) {
				r.Prompt.Attachments = []api.AttachmentRef{preparedAttachment("image/png")}
			}),
			Entry("messages only", func(r *ai.Request) {
				r.Messages = []api.Message{{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "hi"}}}}
			}),
		)
	})

	Describe("the deadline", func() {
		// DefaultTimeout silently capped a host run at two minutes. A run nobody
		// bounded is a configuration error the host can fix, not a limit to invent.
		It("refuses a run with no budget.timeout and no caller timeout", func() {
			_, err := promptrun.Run(context.Background(), promptrun.Input{Request: request, Provider: provider})
			Expect(err).To(MatchError(ContainSubstring("no timeout")))
			Expect(err).To(MatchError(ContainSubstring("budget.timeout")))
			Expect(provider.Calls()).To(BeZero())
		})

		It("prefers the spec's own budget.timeout over the caller's default", func() {
			request.Budget.Timeout = "45m"
			request.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}
			_, err := promptrun.Run(context.Background(), promptrun.Input{
				Request: request, Provider: provider, Timeout: time.Millisecond,
			})
			Expect(err).NotTo(HaveOccurred(), "a one-millisecond host default must not outrank the declared 45m")
		})
	})

	Describe("the executing model", func() {
		// Config.Model is what middleware.NewProvider actually runs. Checking the
		// policy against one model and the attachments against another let a run
		// start on a runtime that could enforce neither.
		It("checks the tool policy against the model the provider will run", func() {
			request.Model = api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}
			request.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyDeny}

			_, err := promptrun.Run(context.Background(), promptrun.Input{
				Request: request, Provider: provider, Timeout: testTimeout,
				Config: ai.Config{Model: api.Model{Name: "gpt-5", Provider: api.OpenAI, Mode: api.ModeAPI}},
			})
			Expect(err).To(MatchError(ContainSubstring(api.RuntimeOf(api.OpenAI, api.ModeAPI).String())))
			Expect(provider.Calls()).To(BeZero())
		})

		It("checks attachment compatibility against that same model", func() {
			request.Model = api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}
			request.Prompt.Attachments = []api.AttachmentRef{preparedAttachment("image/png")}

			_, err := promptrun.Run(context.Background(), promptrun.Input{
				Request: request, Provider: provider, Timeout: testTimeout,
				Config: ai.Config{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI}},
			})
			Expect(err).To(MatchError(ContainSubstring("image/png")))
			Expect(provider.Calls()).To(BeZero())
		})
	})

	Describe("tool policy", func() {
		// No Input.Provider and a Config whose model cannot be resolved: were the
		// check to happen after provider construction, the unknown model would be
		// the error, not the policy.
		It("fails on an unenforceable per-tool policy before any provider is built", func() {
			unbuildable := promptrun.Input{
				Request: request, Timeout: testTimeout,
				Config: ai.Config{Model: api.Model{Name: "no-such-model-at-all", Provider: api.Anthropic, Mode: api.ModeAgent}},
			}
			// The premise: this config cannot produce a provider, so reaching
			// construction is visible as a different error than the policy's.
			_, err := promptrun.Run(context.Background(), unbuildable)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).NotTo(ContainSubstring("per-tool policy"))

			unbuildable.Request.Permissions.Tools = api.Tools{"Bash": api.ToolPolicyAsk}
			_, err = promptrun.Run(context.Background(), unbuildable)
			Expect(err).To(MatchError(ContainSubstring(`per-tool policy "ask" (Bash)`)))
		})
	})

	Describe("a generate → verify run", func() {
		It("returns the loop, verdicts, final report, and the run's identity", func() {
			request.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}
			var events []ai.Event

			var iterations []int
			res, err := promptrun.Run(context.Background(), promptrun.Input{
				Request: request, Provider: provider, Timeout: testTimeout,
				OnEvent: func(iter int, ev ai.Event) {
					events = append(events, ev)
					iterations = append(iterations, iter)
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(iterations).NotTo(BeEmpty(), "OnEvent carries the runner's turn index, which a renderer needs")
			Expect(provider.Calls()).To(Equal(1))
			Expect(res.Passed).To(BeTrue())
			Expect(res.SessionID).To(Equal("sess-1"))
			Expect(res.Model).To(Equal("fake-model"))
			Expect(res.Usage).To(Equal(api.Usage{InputTokens: 7, OutputTokens: 3}))
			Expect(res.CostUSD).To(Equal(0.25))
			Expect(res.Response.Text).To(Equal("done"))
			Expect(res.Loop).NotTo(BeNil())
			Expect(res.Loop.Iterations).To(HaveLen(1))
			Expect(res.Report).To(BeIdenticalTo(res.Verdicts[0].Report))
			Expect(res.Duration).To(BeNumerically(">", 0))

			var kinds []api.EventKind
			for _, ev := range events {
				kinds = append(kinds, ev.Kind)
			}
			Expect(kinds).To(ContainElements(ai.EventSystem, ai.EventText, ai.EventResult, ai.EventVerified))
		})

		It("delivers in-flight progress to Options.Progress and to OnEvent", func() {
			verify.Register(verify.KindFixture, func(_ context.Context, _ api.Verify, _ verify.Options) ([]*verify.Plugin, error) {
				return []*verify.Plugin{verify.New("fixture", &progressVerifier{})}, nil
			})
			request.Workflow = &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}
			var snapshots []api.VerifyReport
			var progressEvents []ai.Event

			res, err := promptrun.Run(context.Background(), promptrun.Input{
				Request: request, Provider: provider, Timeout: testTimeout,
				Verify: verify.Options{Progress: func(r api.VerifyReport) { snapshots = append(snapshots, r) }},
				OnEvent: func(_ int, ev ai.Event) {
					if ev.Kind == ai.EventVerifyProgress {
						progressEvents = append(progressEvents, ev)
					}
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Passed).To(BeTrue())
			Expect(len(snapshots)).To(BeNumerically(">=", 1))
			Expect(snapshots[len(snapshots)-1].Tests[0].Name).To(Equal("check 3"), "the last snapshot is always flushed")
			Expect(len(progressEvents)).To(BeNumerically(">=", 1))
			_, ok := progressEvents[0].Raw.(*api.VerifyReport)
			Expect(ok).To(BeTrue(), "Raw carries the *api.VerifyReport")
			Expect(res.Response.Workspace.Notices).To(HaveLen(1), "progress leaves no notice; the verdict does")
		})

		It("reports the failing check's reason", func() {
			request.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"echo nope; exit 1"}}}

			res, err := promptrun.Run(context.Background(), promptrun.Input{Request: request, Provider: provider, Timeout: testTimeout})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Passed).To(BeFalse())
			Expect(promptrun.FailureReason(res.Verdicts)).To(Equal(res.Report.Reason))
			Expect(res.Report.Reason).To(ContainSubstring("failed"))
		})
	})

	Describe("attachments", func() {
		It("refuses an attachment the caller did not resolve", func() {
			request.Prompt.Attachments = []api.AttachmentRef{{Path: "notes.txt"}}
			_, err := promptrun.Run(context.Background(), promptrun.Input{Request: request, Provider: provider, Timeout: testTimeout})
			Expect(err).To(MatchError(ContainSubstring("attachment")))
			Expect(provider.Calls()).To(BeZero())
		})
	})
})

// preparedAttachment is an attachment a store has already resolved, which is
// the only kind promptrun will run with.
func preparedAttachment(mediaType string) api.AttachmentRef {
	ref := api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), MediaType: mediaType}
	return ref.WithPreparedContent(api.AttachmentContent{Bytes: []byte("payload")})
}

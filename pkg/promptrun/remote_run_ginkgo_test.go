// Specs for a run whose resolved sandbox runs it whole in an environment of its own: promptrun
// hands the run to the sandbox's RemoteRunner and files what the runner relays back.
package promptrun_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
)

// wholeRunSandbox stands in for a sandbox that runs whole runs: run plays the environment's side,
// and the sandbox keeps the input it was handed.
type wholeRunSandbox struct {
	closed int
	in     promptrun.Input
	run    func(ctx context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error)
}

func (s *wholeRunSandbox) Kind() api.SandboxKind { return api.SandboxSubstrate }
func (s *wholeRunSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	Fail("a whole run is handed over, not prepared here")
	return nil, nil
}
func (s *wholeRunSandbox) Close() error              { s.closed++; return nil }
func (s *wholeRunSandbox) IsolatesWorkspace() bool   { return true }
func (s *wholeRunSandbox) ProvidesEgressProxy() bool { return true }
func (s *wholeRunSandbox) RunRemote(ctx context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
	s.in = in
	return s.run(ctx, in, started)
}

// partialSandbox declares what the substrate kind does but can't run a whole run.
type partialSandbox struct{ closed int }

func (s *partialSandbox) Kind() api.SandboxKind { return api.SandboxSubstrate }
func (s *partialSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *partialSandbox) Close() error              { s.closed++; return nil }
func (s *partialSandbox) IsolatesWorkspace() bool   { return true }
func (s *partialSandbox) ProvidesEgressProxy() bool { return true }

var _ = Describe("promptrun.Run with a sandbox that runs whole runs", func() {
	var sandbox *wholeRunSandbox
	BeforeEach(func() {
		sandbox = &wholeRunSandbox{}
		api.RegisterSandbox(api.SandboxSubstrate, func(api.SandboxConfig) (api.Sandbox, error) { return sandbox, nil })
		// A run that falls through to local execution must fail here rather than start a real
		// agent CLI on this machine.
		GinkgoT().Setenv("PATH", GinkgoT().TempDir())
	})

	wholeRun := func() promptrun.Input {
		return promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{
				Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI},
				Prompt: api.Prompt{User: "fix the build"},
			}},
			Config:  ai.Config{SandboxSelection: &api.SandboxConfig{Kind: api.SandboxSubstrate, Name: "substrate-pool"}},
			Timeout: time.Minute,
		}
	}

	It("hands the run to the sandbox and relays what it reports", func(ctx SpecContext) {
		sandbox.run = func(_ context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
			Expect(started(in.Resolved.Spec)).To(Succeed())
			in.OnEvent(1, ai.Event{Kind: ai.EventText, Text: "fixing"})
			return promptrun.Result{Response: &api.Response{Text: "done in the environment"}, CostUSD: 0.2}, nil
		}
		var events []ai.Event
		in := wholeRun()
		in.OnEvent = func(_ int, event ai.Event) { events = append(events, event) }

		result, err := promptrun.Run(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Response.Text).To(Equal("done in the environment"))
		Expect(result.CostUSD).To(Equal(0.2))
		Expect(sandbox.in.Resolved.Spec.Prompt.User).To(Equal("fix the build"))
		Expect(events).To(Equal([]ai.Event{{Kind: ai.EventText, Text: "fixing"}}))
		Expect(sandbox.closed).To(Equal(1))
	})

	It("refuses the deprecated CanUseTool, whose legacy routing can't reach the environment", func(ctx SpecContext) {
		in := wholeRun()
		in.Config.CanUseTool = func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
			return api.ApprovalDecision{Allow: true}, nil
		}

		_, err := promptrun.Run(ctx, in)
		Expect(err).To(MatchError(`promptrun: sandbox "substrate" runs the whole run in an environment of its own, which asks its approvals through Config.OnApproval; set that instead of the deprecated CanUseTool`))
		Expect(sandbox.in.Resolved.Spec.Prompt.User).To(BeEmpty(), "nothing was handed over")
	})

	It("refuses a spec that declares the sandbox but resolved no selection", func(ctx SpecContext) {
		in := wholeRun()
		in.Config.SandboxSelection = nil
		in.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxSubstrate}

		_, err := promptrun.Run(ctx, in)
		Expect(err).To(MatchError(`sandbox mode "substrate" requires a resolved Config.SandboxSelection before running`))
	})

	It("reports a run its context ended as stopped", func(ctx SpecContext) {
		sandbox.run = func(ctx context.Context, _ promptrun.Input, _ func(api.Spec) error) (promptrun.Result, error) {
			<-ctx.Done()
			return promptrun.Result{}, nil
		}
		in := wholeRun()
		in.Timeout = 50 * time.Millisecond

		_, err := promptrun.Run(ctx, in)
		Expect(err).To(MatchError("promptrun: run stopped: context deadline exceeded"))
		Expect(sandbox.closed).To(Equal(1))
	})

	It("files a recorded run from what the runner relays", func(ctx SpecContext) {
		db := openRecordDB()
		providerSessionID := uuid.NewString()
		sandbox.run = func(_ context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
			spec := in.Resolved.Spec
			spec.SetCwd("/workspace/src")
			Expect(started(spec)).To(Succeed())
			in.OnEvent(1, ai.Event{Kind: ai.EventSystem, SessionID: providerSessionID})
			return promptrun.Result{Response: &api.Response{Text: "done in the environment"}, Passed: true}, nil
		}
		in := wholeRun()
		admitted := 0
		in.Record = &promptrun.Recording{
			DB: db, Placement: promptrun.Placement{Sessions: hostTree()}, AdmissionKey: "admission-" + uuid.NewString(),
			Admitted: func() error {
				Expect(sandbox.in.Resolved.Spec.Prompt.User).To(BeEmpty(), "the host hears of the admission before the run is handed over")
				admitted++
				return nil
			},
		}

		result, err := promptrun.Run(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		Expect(admitted).To(Equal(1))

		run, err := db.GetPromptRun(ctx, result.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateSucceeded))
		Expect(run.StartedAt).NotTo(BeNil(), "the runner's start is the run's")
		Expect(run.ResultText).To(Equal("done in the environment"))
		admission, err := db.GetSession(ctx, run.SessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.CWD).To(Equal("/workspace/src"), "the run started where the runner says it did")
		Expect(admission.ProviderSessionID).To(Equal(providerSessionID), "the relayed events are observed")
	})
})

var _ = Describe("a sandbox kind that runs whole runs", func() {
	It("must be able to run one", func() {
		partial := &partialSandbox{}
		api.RegisterSandbox(api.SandboxSubstrate, func(api.SandboxConfig) (api.Sandbox, error) { return partial, nil })

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxSubstrate})
		Expect(err).To(MatchError(`sandbox "substrate" declares capability "remote-run" but its adapter does not implement it`))
		Expect(partial.closed).To(Equal(1))
	})
})

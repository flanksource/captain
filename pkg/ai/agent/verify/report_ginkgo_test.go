package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("typed verify reports", func() {
	var (
		cwd string
		hc  *agent.HookContext
	)

	BeforeEach(func() {
		cwd = GinkgoT().TempDir()
		hc = &agent.HookContext{
			Context:  context.Background(),
			Request:  &ai.Request{},
			Response: &ai.Response{Workspace: &api.Workspace{Cwd: cwd}},
			Scope:    agent.ScopeAll,
			// HookContext.Iteration is the loop's 0-based index; a report and a
			// VerifyResult name the turn 1-based, so this is turn 3 of the run.
			Iteration: 2,
		}
	})

	Describe("CmdVerifier", func() {
		It("reports a failing command as one failed node carrying the command, cwd and output tail", func() {
			plugin := New("verify:sh", &CmdVerifier{Cmd: "sh", Args: []string{"-c", "echo out; exit 1"}})

			result, err := plugin.Verify(hc)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Iteration).To(Equal(3))
			Expect(result.Retry).NotTo(BeNil())

			report := result.Report
			Expect(report).NotTo(BeNil())
			Expect(report.Validate()).To(Succeed())
			Expect(report.Kind).To(Equal(api.VerifyKindCmd))
			Expect(report.Name).To(Equal("sh -c echo out; exit 1"))
			Expect(report.Iteration).To(Equal(3))
			Expect(report.Ran).To(BeTrue())
			Expect(report.Passed).To(BeFalse())
			Expect(report.State).To(Equal(api.VerifyStateFailed))
			Expect(report.Summary).To(Equal(api.VerifySummary{Total: 1, Failed: 1}))
			Expect(report.Feedback).To(ContainSubstring("out"))

			Expect(report.Tests).To(HaveLen(1))
			node := report.Tests[0]
			Expect(node.Failed).To(BeTrue())
			Expect(node.Command).To(Equal("sh -c echo out; exit 1"))
			Expect(node.WorkDir).To(Equal(cwd))
			Expect(node.Stderr).To(ContainSubstring("out"))
			Expect(node.Context.ExitCode).To(Equal(1))
			Expect(node.Duration).To(BeNumerically(">", time.Duration(0)))

			raw, err := json.Marshal(report)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"work_dir"`))
			Expect(string(raw)).To(ContainSubstring(`"exit_code":1`))
		})

		It("reports a passing command as a passed report", func() {
			plugin := New("verify:true", &CmdVerifier{Cmd: "sh", Args: []string{"-c", "echo fine"}})

			result, err := plugin.Verify(hc)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Valid).To(BeTrue())
			Expect(result.Retry).To(BeNil())
			Expect(result.Report.Validate()).To(Succeed())
			Expect(result.Report.Passed).To(BeTrue())
			Expect(result.Report.State).To(Equal(api.VerifyStatePassed))
			Expect(result.Report.Tests[0].Stdout).To(Equal("fine"))
			Expect(result.Report.Tests[0].Context.ExitCode).To(Equal(0))
		})

		It("marks a timed-out command as timed out, not merely failed", func() {
			plugin := New("verify:sleep", &CmdVerifier{Cmd: "sh", Args: []string{"-c", "sleep 5"}, Timeout: 50 * time.Millisecond})

			result, err := plugin.Verify(hc)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Report.State).To(Equal(api.VerifyStateTimedOut))
			Expect(result.Report.Tests[0].TimedOut).To(BeTrue())
			// Timed out, not merely failed, in the counters too: a check that
			// never finished is a different problem from one that disagreed.
			Expect(result.Report.Summary).To(Equal(api.VerifySummary{Total: 1, TimedOut: 1}))
		})
	})

	// A verifier that reports OK while its own report says otherwise has two
	// answers and no way to choose: silently trusting one of them is how a
	// failing check lands in the store as a pass.
	Describe("a verdict that disagrees with its own report", func() {
		It("is an error rather than a silently reconciled verdict", func() {
			contradiction := api.NewNodeReport("fixture", "acceptance", api.VerifyNode{Name: "t", Failed: true})
			plugin := New("fixture:acceptance", FuncVerifier(func(context.Context, string, []string) (Verdict, error) {
				return Verdict{OK: true, Report: &contradiction}, nil
			}))

			_, err := plugin.Verify(hc)
			Expect(err).To(MatchError(ContainSubstring("reports passed=false but its verdict says OK=true")))
		})
	})

	Describe("a Verifier that returns no report", func() {
		It("gets a one-node report synthesised from its verdict", func() {
			plugin := New("custom", FuncVerifier(func(context.Context, string, []string) (Verdict, error) {
				return Verdict{OK: false, Reason: "not yet", Feedback: "do more"}, nil
			}))

			result, err := plugin.Verify(hc)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Valid).To(BeFalse())
			Expect(result.Report).NotTo(BeNil())
			Expect(result.Report.Validate()).To(Succeed())
			Expect(result.Report.Kind).To(Equal(api.VerifyKindFunc))
			Expect(result.Report.Name).To(Equal("custom"))
			Expect(result.Report.Reason).To(Equal("not yet"))
			Expect(result.Report.Feedback).To(Equal("do more"))
			Expect(result.Report.Iteration).To(Equal(3))
			Expect(result.Report.Tests).To(HaveLen(1))
			Expect(result.Report.Tests[0].Name).To(Equal("custom"))
			Expect(result.Report.Tests[0].Failed).To(BeTrue())
		})

		It("keeps a report the Verifier supplied and fills only what is missing", func() {
			supplied := api.NewNodeReport("fixture", "", api.VerifyNode{Name: "go test ./x", Passed: true})
			plugin := New("fixture:acceptance", FuncVerifier(func(context.Context, string, []string) (Verdict, error) {
				return Verdict{OK: true, Report: &supplied}, nil
			}))

			result, err := plugin.Verify(hc)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Report.Kind).To(Equal("fixture"))
			Expect(result.Report.Name).To(Equal("fixture:acceptance"))
			Expect(result.Report.Iteration).To(Equal(3))
			Expect(result.Report.Tests[0].Name).To(Equal("go test ./x"))
		})
	})

	It("attaches the report to the verdict event on the run stream", func() {
		var streamed []ai.Event
		runner := &agent.Runner[string]{
			Provider:      &silentProvider{},
			Cwd:           cwd,
			Request:       ai.Request{Prompt: api.Prompt{User: "fix it"}},
			Hooks:         []any{New("verify:false", &CmdVerifier{Cmd: "false"})},
			MaxIterations: 1,
			OnEvent: func(_ int, ev ai.Event) {
				if ev.Kind == api.EventVerifyFailed || ev.Kind == api.EventVerified {
					streamed = append(streamed, ev)
				}
			},
		}

		res, err := runner.Run(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(streamed).To(HaveLen(1))
		Expect(streamed[0].Kind).To(Equal(ai.EventVerifyFailed))
		report, ok := streamed[0].Raw.(*api.VerifyReport)
		Expect(ok).To(BeTrue(), "Raw should carry the *api.VerifyReport")
		Expect(report.State).To(Equal(api.VerifyStateFailed))
		Expect(res.Verdicts).To(HaveLen(1))
		Expect(res.Verdicts[0].Report).To(BeIdenticalTo(report))
		// The loop indexes its turns from 0; a verdict names the turn it judged
		// the way a person does — and the way the iteration store is keyed.
		Expect(res.Verdicts[0].Iteration).To(Equal(1))
		Expect(report.Iteration).To(Equal(1))
	})

	// Progress is ephemeral: it exists so a reader watching a long check sees
	// that something is moving. Recording each snapshot as a workspace notice
	// wrote every one of them into the persisted transcript, burying the verdict
	// the notices exist to surface.
	Describe("in-flight progress", func() {
		It("streams snapshots without leaving a notice behind, while the verdict still records one", func() {
			var streamed []ai.Event
			runner := &agent.Runner[string]{
				Provider:      &silentProvider{},
				Cwd:           cwd,
				Request:       ai.Request{Prompt: api.Prompt{User: "fix it"}},
				Hooks:         []any{New("fixture", &threeSnapshotVerifier{})},
				MaxIterations: 1,
				OnEvent: func(_ int, ev ai.Event) {
					switch ev.Kind {
					case ai.EventVerifyProgress, ai.EventVerified, ai.EventVerifyFailed:
						streamed = append(streamed, ev)
					}
				},
			}

			res, err := runner.Run(context.Background())
			Expect(err).NotTo(HaveOccurred())

			var progress []ai.Event
			for _, ev := range streamed {
				if ev.Kind == ai.EventVerifyProgress {
					progress = append(progress, ev)
				}
			}
			Expect(len(progress)).To(BeNumerically(">=", 1))
			Expect(progress[0].Tool).To(Equal("fixture"))
			Expect(progress[0].Text).To(BeEmpty(), "a stream-only event carries the report, not prose about it")
			snapshot, ok := progress[len(progress)-1].Raw.(*api.VerifyReport)
			Expect(ok).To(BeTrue(), "Raw carries the *api.VerifyReport")
			Expect(snapshot.Tests[0].Name).To(Equal("check 3"), "the last snapshot is always flushed")

			notices := res.Response.Workspace.Notices
			Expect(notices).To(HaveLen(1), "only the verdict is a notice; the snapshots are not")
			Expect(notices[0].Kind).To(Equal(ai.EventVerified))
		})
	})
})

// threeSnapshotVerifier reports three in-flight snapshots and then passes.
type threeSnapshotVerifier struct{ progress func(api.VerifyReport) }

func (v *threeSnapshotVerifier) SetProgress(fn func(api.VerifyReport)) { v.progress = fn }

func (v *threeSnapshotVerifier) Verify(context.Context, string, []string) (Verdict, error) {
	for i := 1; i <= 3; i++ {
		v.progress(api.NewNodeReport(api.VerifyKindFunc, "fixture", api.VerifyNode{
			Name: fmt.Sprintf("check %d", i), Running: true,
		}))
	}
	final := api.NewNodeReport(api.VerifyKindFunc, "fixture", api.VerifyNode{Name: "check 3", Passed: true})
	return Verdict{OK: true, Report: &final}, nil
}

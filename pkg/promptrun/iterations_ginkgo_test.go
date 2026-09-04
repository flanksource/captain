package promptrun_test

import (
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// verdictReport is the report one turn's verifier produced, stamped for that
// 1-based turn exactly as verify.Plugin stamps it.
func verdictReport(iteration int, passed bool) *api.VerifyReport {
	node := api.VerifyNode{Name: "go test ./...", Passed: passed, Failed: !passed}
	report := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", node)
	report.Iteration = iteration
	return &report
}

func loopWith(turns int, base time.Time, err error) *ai.LoopResult {
	loop := &ai.LoopResult{StopReason: "condition-met"}
	for i := 0; i < turns; i++ {
		started := base.Add(time.Duration(i) * time.Minute)
		iteration := &ai.LoopIteration{
			Iteration:  i,
			Request:    ai.Request{Prompt: api.Prompt{User: "attempt " + string(rune('A'+i))}},
			StartedAt:  started,
			FinishedAt: started.Add(30 * time.Second),
			Success:    true,
		}
		if i == turns-1 {
			iteration.Err = err
		}
		loop.Iterations = append(loop.Iterations, iteration)
	}
	return loop
}

var _ = Describe("IterationRecords", func() {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)

	It("files a failing then a passing turn as two rows carrying their verdicts", func() {
		loop := loopWith(2, base, nil)
		verdicts := []agent.VerifyResult{
			{Valid: false, Iteration: 1, Report: verdictReport(1, false), Retry: &ai.Request{}},
			{Valid: true, Iteration: 2, Report: verdictReport(2, true)},
		}
		verdicts[0].Report.Feedback = "TestFoo failed"

		records := promptrun.IterationRecords(promptrun.Result{Loop: loop, Verdicts: verdicts}, false)

		Expect(records).To(HaveLen(2))
		Expect(records[0].Iteration).To(Equal(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(records[0].Feedback).To(Equal("TestFoo failed"))
		Expect(records[0].VerificationResult).To(Equal(verdicts[0].Report))
		Expect(records[0].Request).To(Equal(map[string]any{"prompt": "attempt A"}))
		Expect(records[0].StartedAt).To(HaveValue(Equal(base)))
		Expect(records[0].FinishedAt).To(HaveValue(Equal(base.Add(30 * time.Second))))
		Expect(records[1].Iteration).To(Equal(2))
		Expect(records[1].State).To(Equal(database.PromptRunIterationStateSucceeded))
		Expect(records[1].Feedback).To(BeEmpty())
		Expect(records[1].VerificationResult).To(Equal(verdicts[1].Report))
	})

	// A turn the provider could not complete is a failure of the turn, not a
	// verdict: nothing judged it, and recording it as succeeded would make a
	// crashed run read as a clean one.
	It("files a provider error as a failed, unjudged turn", func() {
		records := promptrun.IterationRecords(promptrun.Result{Loop: loopWith(1, base, errors.New("upstream 529"))}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(records[0].Error).To(Equal("upstream 529"))
		Expect(records[0].VerificationResult).To(BeNil())
	})

	// A stopped run's last turn was interrupted, not judged; "cancelled" is the
	// only state that says so.
	It("cancels the last turn of a stopped run", func() {
		verdicts := []agent.VerifyResult{{Valid: false, Iteration: 1, Report: verdictReport(1, false)}}

		records := promptrun.IterationRecords(promptrun.Result{Loop: loopWith(2, base, nil), Verdicts: verdicts}, true)

		Expect(records).To(HaveLen(2))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(records[1].State).To(Equal(database.PromptRunIterationStateCancelled))
	})

	// A round runs every verifier the workflow declares — `commands` and
	// `fixture` both vote on the same turn. Keeping the last verdict per
	// iteration stored the fixture's tree and threw the command's away.
	It("rolls a whole round into one report", func() {
		cmd := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", api.VerifyNode{Name: "go test ./...", Passed: true})
		cmd.Ran, cmd.Iteration = true, 1
		fixture := api.NewNodeReport(api.VerifyKindFixture, "acceptance", api.VerifyNode{Name: "TestFoo", Failed: true})
		fixture.Ran, fixture.Iteration, fixture.Feedback = true, 1, "TestFoo: want 3, got 4"

		records := promptrun.IterationRecords(promptrun.Result{Loop: loopWith(1, base, nil), Verdicts: []agent.VerifyResult{
			{Valid: true, Iteration: 1, Report: &cmd},
			{Valid: false, Iteration: 1, Report: &fixture},
		}}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(records[0].Feedback).To(Equal("TestFoo: want 3, got 4"))
		report := records[0].VerificationResult
		Expect(report).NotTo(BeNil())
		Expect(report.Tests).To(HaveLen(2), "both verifiers keep their own group node")
		Expect(report.Tests[0].Name).To(Equal("verify:go test ./..."))
		Expect(report.Tests[1].Name).To(Equal("acceptance"))
		Expect(report.Summary).To(Equal(api.VerifySummary{Total: 2, Passed: 1, Failed: 1}))
		Expect(report.Passed).To(BeFalse())
		Expect(report.Validate()).To(Succeed())
	})

	// A single-verdict round is that verdict's report, unwrapped: nesting a lone
	// check under a group node would change every stored row for no gain.
	It("stores a single verdict as is", func() {
		verdicts := []agent.VerifyResult{{Valid: true, Iteration: 1, Report: verdictReport(1, true)}}

		records := promptrun.IterationRecords(promptrun.Result{Loop: loopWith(1, base, nil), Verdicts: verdicts}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].VerificationResult).To(BeIdenticalTo(verdicts[0].Report))
	})

	// A run with no Verify hooks at all has nothing to fail: every completed turn
	// stands on its own.
	It("files unverified turns as succeeded", func() {
		records := promptrun.IterationRecords(promptrun.Result{Loop: loopWith(1, base, nil)}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateSucceeded))
		Expect(records[0].VerificationResult).To(BeNil())
	})

	// A verify-only run generated nothing, so the loop never ran — but the
	// verifiers judged the tree, and that verdict is the run's whole account.
	// Filing no row for it left every such run without a verification report:
	// an embedding host's dashboard read "never verified" for a check that passed.
	It("files a verify-only run as iteration 1 carrying the round's report", func() {
		started, finished := base, base.Add(45*time.Second)
		fixture := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "echo ok", Passed: true})
		fixture.Ran, fixture.Iteration, fixture.StartedAt, fixture.FinishedAt = true, 1, &started, &finished

		records := promptrun.IterationRecords(promptrun.Result{Verdicts: []agent.VerifyResult{
			{Valid: true, Iteration: 1, Report: &fixture},
		}}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].Iteration).To(Equal(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateSucceeded))
		Expect(records[0].VerificationResult).To(BeIdenticalTo(&fixture))
		Expect(records[0].Request).To(Equal(map[string]any{"verify_only": true}))
		Expect(records[0].StartedAt).To(HaveValue(Equal(started)))
		Expect(records[0].FinishedAt).To(HaveValue(Equal(finished)))
	})

	It("files a failing verify-only run as a failed iteration 1 with its feedback", func() {
		fixture := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "echo ok", Failed: true})
		fixture.Ran, fixture.Iteration, fixture.Feedback = true, 1, "echo ok: exit 1"

		records := promptrun.IterationRecords(promptrun.Result{Verdicts: []agent.VerifyResult{
			{Valid: false, Iteration: 1, Report: &fixture},
		}}, false)

		Expect(records).To(HaveLen(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(records[0].Feedback).To(Equal("echo ok: exit 1"))
		Expect(records[0].StartedAt).To(BeNil(), "a report without a clock stamps nothing")
	})

	It("cancels an interrupted verify-only run", func() {
		fixture := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "echo ok", Passed: true})
		fixture.Ran, fixture.Iteration = true, 1

		records := promptrun.IterationRecords(promptrun.Result{Verdicts: []agent.VerifyResult{
			{Valid: true, Iteration: 1, Report: &fixture},
		}}, true)

		Expect(records).To(HaveLen(1))
		Expect(records[0].State).To(Equal(database.PromptRunIterationStateCancelled))
	})

	It("files nothing for a run with neither turns nor verdicts", func() {
		Expect(promptrun.IterationRecords(promptrun.Result{}, false)).To(BeNil())
	})
})

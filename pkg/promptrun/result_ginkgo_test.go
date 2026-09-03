package promptrun_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
)

var _ = Describe("verdict helpers", func() {
	failed := api.NewNodeReport(api.VerifyKindCmd, "verify:go test", api.VerifyNode{Name: "go test", Failed: true, Message: "TestFoo failed"})
	failed.Reason = "go test failed"
	passed := api.NewNodeReport(api.VerifyKindCmd, "verify:go test", api.VerifyNode{Name: "go test", Passed: true})

	Describe("Passed", func() {
		// The runner stops each round at its first failure, so the last verdict
		// is always the final round's outcome; earlier failures are the history
		// of retries, not the answer.
		It("reads the last verdict, and passes trivially with none", func() {
			Expect(promptrun.Passed(nil)).To(BeTrue())
			Expect(promptrun.Passed([]agent.VerifyResult{{Valid: false}, {Valid: true}})).To(BeTrue())
			Expect(promptrun.Passed([]agent.VerifyResult{{Valid: true}, {Valid: false}})).To(BeFalse())
		})
	})

	Describe("FailureReason", func() {
		It("is the last failing report's reason, and empty for a pass", func() {
			Expect(promptrun.FailureReason(nil)).To(BeEmpty())
			Expect(promptrun.FailureReason([]agent.VerifyResult{{Valid: true, Report: &passed}})).To(BeEmpty())
			Expect(promptrun.FailureReason([]agent.VerifyResult{{Valid: false, Report: &failed}})).To(Equal("go test failed"))
			Expect(promptrun.FailureReason([]agent.VerifyResult{{Valid: false}})).To(Equal("verification failed"))
		})
	})

	Describe("FinalReport", func() {
		It("is the run's last round, and nothing at all with no verdicts", func() {
			report, err := promptrun.FinalReport(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(report).To(BeNil())

			report, err = promptrun.FinalReport([]agent.VerifyResult{
				{Iteration: 1, Report: &failed}, {Iteration: 2, Report: &passed},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(report).To(BeIdenticalTo(&passed), "a single-verdict round is its own report, unwrapped")
		})

		It("does not let a verdict without a report blank the one the round has", func() {
			report, err := promptrun.FinalReport([]agent.VerifyResult{{Iteration: 1, Report: &passed}, {Iteration: 1}})
			Expect(err).NotTo(HaveOccurred())
			Expect(report).To(BeIdenticalTo(&passed))
		})

		// A round runs every declared verifier. Reading only the last verdict
		// persisted the fixture's tree and threw the command's away, so the run's
		// record showed half of what had actually been checked.
		It("rolls a round of several verdicts into one report, each under its own group node", func() {
			cmd := api.NewNodeReport(api.VerifyKindCmd, "verify:go test", api.VerifyNode{Name: "go test", Passed: true})
			cmd.Ran, cmd.Iteration = true, 1
			fixture := api.NewNodeReport(api.VerifyKindFixture, "acceptance", api.VerifyNode{Name: "TestFoo", Failed: true})
			fixture.Ran, fixture.Iteration, fixture.Reason = true, 1, "TestFoo failed"

			report, err := promptrun.FinalReport([]agent.VerifyResult{
				{Iteration: 1, Valid: true, Report: &cmd},
				{Iteration: 1, Valid: false, Report: &fixture},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(report).NotTo(BeNil())
			Expect(report.Tests).To(HaveLen(2))
			Expect(report.Tests[0].Name).To(Equal("verify:go test"))
			Expect(report.Tests[1].Name).To(Equal("acceptance"))
			Expect(report.Summary).To(Equal(api.VerifySummary{Total: 2, Passed: 1, Failed: 1}))
			Expect(report.Passed).To(BeFalse())
			Expect(report.Iteration).To(Equal(1))
			Expect(report.Validate()).To(Succeed())
		})
	})
})

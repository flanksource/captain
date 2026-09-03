package api_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("MergeReports", func() {
	var (
		earlier = time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
		later   = time.Date(2026, 9, 3, 8, 5, 0, 0, time.UTC)
	)

	// A round runs every verifier the workflow declares. Keeping the last
	// verdict's report threw the others away, so a round of `commands` + `fixture`
	// persisted the fixture's tree and nothing else — and the run's summary
	// counted half of what actually ran.
	cmd := func() api.VerifyReport {
		report := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", api.VerifyNode{
			Name: "go test ./...", Framework: api.VerifyKindCmd, Passed: true, Duration: 2 * time.Second,
		})
		report.Iteration = 2
		report.Ran = true
		started, finished := earlier, earlier.Add(2*time.Second)
		report.StartedAt, report.FinishedAt = &started, &finished
		return report
	}
	fixture := func() api.VerifyReport {
		report := api.VerifyReport{
			Kind: api.VerifyKindFixture, Name: "acceptance", Ran: true, Iteration: 2,
			Passed: false, Reason: "2 of 40 failed", Feedback: "TestFoo: want 3, got 4",
			State: api.VerifyStateFailed,
			Tests: []api.VerifyNode{
				{Name: "TestFoo", Framework: api.VerifyKindFixture, Failed: true},
				{Name: "TestBar", Framework: api.VerifyKindFixture, Passed: true},
			},
			Summary:   api.VerifySummary{Total: 2, Failed: 1, Passed: 1},
			Checklist: []api.VerifyChecklistItem{{Item: "adds a test", Passed: boolPtr(false)}},
			Duration:  30 * time.Second,
		}
		started, finished := later, later.Add(30*time.Second)
		report.StartedAt, report.FinishedAt = &started, &finished
		return report
	}

	It("nests each report under a group node named after it and totals the counts", func() {
		merged, err := api.MergeReports("verify", cmd(), fixture())
		Expect(err).NotTo(HaveOccurred())

		Expect(merged.Name).To(Equal("verify"))
		Expect(merged.Iteration).To(Equal(2))
		Expect(merged.Tests).To(HaveLen(2))
		Expect(merged.Tests[0].Name).To(Equal("verify:go test ./..."))
		Expect(merged.Tests[0].Framework).To(Equal(api.VerifyKindCmd))
		Expect(merged.Tests[0].Children).To(HaveLen(1))
		Expect(merged.Tests[0].Summary).To(Equal(&api.VerifySummary{Total: 1, Passed: 1}))
		Expect(merged.Tests[1].Name).To(Equal("acceptance"))
		Expect(merged.Tests[1].Framework).To(Equal(api.VerifyKindFixture))
		Expect(merged.Tests[1].Children).To(HaveLen(2))

		Expect(merged.Summary).To(Equal(api.VerifySummary{Total: 3, Passed: 2, Failed: 1}))
		Expect(merged.Passed).To(BeFalse())
		Expect(merged.Ran).To(BeTrue())
		Expect(merged.State).To(Equal(api.VerifyStateFailed))
		Expect(merged.Reason).To(ContainSubstring("2 of 40 failed"))
		Expect(merged.Feedback).To(ContainSubstring("TestFoo: want 3, got 4"))
		Expect(merged.Checklist).To(HaveLen(1))
		Expect(merged.Validate()).To(Succeed())
	})

	It("takes the earliest start and the latest finish", func() {
		merged, err := api.MergeReports("verify", cmd(), fixture())
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.StartedAt).NotTo(BeNil())
		Expect(merged.FinishedAt).NotTo(BeNil())
		Expect(*merged.StartedAt).To(Equal(earlier))
		Expect(*merged.FinishedAt).To(Equal(later.Add(30 * time.Second)))
		Expect(merged.Duration).To(Equal(32 * time.Second))
	})

	It("keeps a shared kind and names a mixed round `round`", func() {
		one, two := cmd(), cmd()
		two.Name = "verify:make lint"
		merged, err := api.MergeReports("verify", one, two)
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.Kind).To(Equal(api.VerifyKindCmd))
		Expect(merged.Passed).To(BeTrue())
		Expect(merged.State).To(Equal(api.VerifyStatePassed))
		Expect(merged.Validate()).To(Succeed())

		mixed, err := api.MergeReports("verify", cmd(), fixture())
		Expect(err).NotTo(HaveOccurred())
		Expect(mixed.Kind).To(Equal(api.VerifyKindRound))
	})

	// Two reports from different turns are not one verdict, and silently keeping
	// one of the iteration numbers is how a turn-2 failure gets filed under turn 1.
	It("refuses reports from different iterations", func() {
		second := fixture()
		second.Iteration = 3
		_, err := api.MergeReports("verify", cmd(), second)
		Expect(err).To(MatchError(ContainSubstring("iteration")))
	})

	It("refuses an empty round", func() {
		_, err := api.MergeReports("verify")
		Expect(err).To(MatchError(ContainSubstring("no reports")))
	})

	// errored/cancelled are the host's word and no node flag maps to them, so a
	// round containing one keeps it rather than reporting the tree's own state.
	It("keeps a host-stamped state over the state the tree implies", func() {
		stopped := fixture()
		stopped.State = api.VerifyStateCancelled
		stopped.Passed = false
		merged, err := api.MergeReports("verify", cmd(), stopped)
		Expect(err).NotTo(HaveOccurred())
		Expect(merged.State).To(Equal(api.VerifyStateCancelled))
		Expect(merged.Passed).To(BeFalse())
		Expect(merged.Validate()).To(Succeed())
	})
})

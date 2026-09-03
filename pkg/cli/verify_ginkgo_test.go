package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	clickyapi "github.com/flanksource/clicky/api"
)

var _ = Describe("captain verify", func() {
	ctx := context.Background()

	It("reports a passing command as a passed report", func() {
		result, err := RunVerify(ctx, VerifyOptions{Commands: []string{"true"}, Cwd: GinkgoT().TempDir()})
		Expect(err).NotTo(HaveOccurred())

		verdict, ok := result.(VerifyResult)
		Expect(ok).To(BeTrue())
		Expect(verdict.Passed).To(BeTrue())
		Expect(verdict.Reports).To(HaveLen(1))
		Expect(verdict.Reports[0].Validate()).To(Succeed())
		Expect(verdict.Reports[0].Kind).To(Equal(api.VerifyKindCmd))
		Expect(verdict.Summary).To(Equal(api.VerifySummary{Total: 1, Passed: 1}))

		raw, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"passed":true`))
		Expect(string(raw)).To(ContainSubstring(`"reports"`))
	})

	It("fails the command when a check fails, and still carries its reports", func() {
		result, err := RunVerify(ctx, VerifyOptions{Commands: []string{"echo boom-detail; exit 1"}, Cwd: GinkgoT().TempDir()})
		Expect(err).To(HaveOccurred(), "a failing check must exit non-zero")
		Expect(err.Error()).To(ContainSubstring("1 of 1 checks did not pass"))

		verdict, ok := result.(VerifyResult)
		Expect(ok).To(BeTrue())
		Expect(verdict.Passed).To(BeFalse())
		Expect(verdict.Reports[0].Feedback).To(ContainSubstring("boom-detail"))

		Expect(clickyapi.TryTypedValue(err)).NotTo(BeNil(),
			"the error renders through the format pipeline, so a failure prints its reports")
	})

	It("counts a check that ran out of wall clock in the timed-out bucket", func() {
		result, err := RunVerify(ctx, VerifyOptions{
			Commands: []string{"sleep 5"}, Timeout: "200ms", Cwd: GinkgoT().TempDir(),
		})
		Expect(err).To(HaveOccurred(), "a check that never finished has not passed")

		verdict, ok := result.(VerifyResult)
		Expect(ok).To(BeTrue())
		Expect(verdict.Reports).To(HaveLen(1))
		Expect(verdict.Reports[0].State).To(Equal(api.VerifyStateTimedOut))
		// The run summary is rolled up by api.AddSummaries, so every bucket the
		// wire shape carries survives — a re-listing of the fields here is what
		// dropped `timedout` on the floor and reported the run as 0 of 0.
		Expect(verdict.Summary).To(Equal(api.VerifySummary{Total: 1, TimedOut: 1}))

		raw, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"timedout":1`))
	})

	It("refuses a declared fixture with no fixture runner configured", func() {
		fixture := filepath.Join(GinkgoT().TempDir(), "acceptance.md")
		Expect(os.WriteFile(fixture, []byte("# acceptance\n"), 0o644)).To(Succeed())

		_, err := RunVerify(ctx, VerifyOptions{Fixture: fixture, Cwd: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring("no fixture verifier is registered")))
	})

	It("refuses a run with nothing declared rather than passing vacuously", func() {
		_, err := RunVerify(ctx, VerifyOptions{Cwd: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring("nothing to verify")))
	})
})

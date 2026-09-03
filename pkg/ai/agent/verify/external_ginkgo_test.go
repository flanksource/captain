package verify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// fixtureRunnerScript writes a throwaway runner: it records the fixture it was
// given on stdin and the arguments it was called with, replays a canned NDJSON
// stdout, and exits with the given status — the whole contract of a real
// fixture runner, in the shape a test can assert on.
func fixtureRunnerScript(dir, stdout string, exitCode int) string {
	Expect(os.WriteFile(filepath.Join(dir, "stdout.ndjson"), []byte(stdout), 0o644)).To(Succeed())
	script := "#!/bin/sh\n" +
		"cat > " + filepath.Join(dir, "stdin.txt") + "\n" +
		"echo \"$@\" > " + filepath.Join(dir, "args.txt") + "\n" +
		"cat " + filepath.Join(dir, "stdout.ndjson") + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "runner.sh")
	Expect(os.WriteFile(path, []byte(script), 0o755)).To(Succeed())
	return path
}

// ndjson renders one protocol line: {"progress": …} or {"report": …}.
func ndjson(key string, report api.VerifyReport) string {
	raw, err := json.Marshal(map[string]api.VerifyReport{key: report})
	Expect(err).NotTo(HaveOccurred())
	return string(raw) + "\n"
}

func passingReport(name string) api.VerifyReport {
	return api.NewNodeReport("fixture", "acceptance", api.VerifyNode{Name: name, Passed: true})
}

func runningReport(name string) api.VerifyReport {
	return api.NewNodeReport("fixture", "acceptance", api.VerifyNode{Name: name, Running: true})
}

var _ = Describe("the external fixture verifier", func() {
	ctx := context.Background()

	It("forwards each progress line and returns the report as the verdict", func() {
		dir := GinkgoT().TempDir()
		stdout := ndjson("progress", runningReport("check 1")) +
			ndjson("progress", runningReport("check 2")) +
			ndjson("report", passingReport("go test ./..."))
		verifier := &ExternalVerifier{Command: []string{fixtureRunnerScript(dir, stdout, 0)}, Fixture: "# acceptance\n"}

		var snapshots []api.VerifyReport
		verifier.SetProgress(func(r api.VerifyReport) { snapshots = append(snapshots, r) })

		vd, err := verifier.Verify(ctx, dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(vd.OK).To(BeTrue())
		Expect(snapshots).To(HaveLen(2))
		Expect(snapshots[1].Tests[0].Name).To(Equal("check 2"))
		Expect(vd.Report).NotTo(BeNil())
		Expect(vd.Report.Validate()).To(Succeed())
		Expect(vd.Report.Kind).To(Equal("fixture"))
		Expect(vd.Report.Tests[0].Name).To(Equal("go test ./..."))
		Expect(vd.Report.Summary).To(Equal(api.VerifySummary{Total: 1, Passed: 1}))
	})

	It("reports a failing fixture as a verdict, not an error, even on a non-zero exit", func() {
		dir := GinkgoT().TempDir()
		failing := api.NewNodeReport("fixture", "acceptance", api.VerifyNode{
			Name: "go test ./...", Failed: true, Message: "TestFoo failed",
		})
		failing.Reason = "1 test failed"
		failing.Feedback = "TestFoo: want 3, got 4"
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, ndjson("report", failing), 1)},
			Fixture: "# acceptance\n",
		}

		vd, err := verifier.Verify(ctx, dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(vd.OK).To(BeFalse())
		Expect(vd.Reason).To(Equal("1 test failed"))
		Expect(vd.Feedback).To(Equal("TestFoo: want 3, got 4"))
		Expect(vd.Report.State).To(Equal(api.VerifyStateFailed))
	})

	It("refuses to invent a verdict when the runner exits without a report", func() {
		dir := GinkgoT().TempDir()
		verifier := &ExternalVerifier{Command: []string{fixtureRunnerScript(dir, "", 1)}, Fixture: "# acceptance\n"}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(ContainSubstring("no report line")))
	})

	It("refuses to invent a verdict from malformed output", func() {
		dir := GinkgoT().TempDir()
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, "{\"report\": not-json}\n", 0)},
			Fixture: "# acceptance\n",
		}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(ContainSubstring("malformed stdout line")))
	})

	It("hands the fixture over on stdin and names the cwd and every changed file", func() {
		dir := GinkgoT().TempDir()
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, ndjson("report", passingReport("ok")), 0), "check"},
			Fixture: "# acceptance\n- [ ] it works\n",
		}

		_, err := verifier.Verify(ctx, dir, []string{"pkg/a.go", "pkg/b.go"})
		Expect(err).NotTo(HaveOccurred())

		Expect(os.ReadFile(filepath.Join(dir, "stdin.txt"))).To(Equal([]byte("# acceptance\n- [ ] it works\n")))
		args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(args))).To(Equal(
			"check --cwd " + dir + " --changed pkg/a.go --changed pkg/b.go"))
	})
})

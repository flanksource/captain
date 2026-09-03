package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// writeScript drops an executable shell script and returns its path.
func writeScript(dir, name, body string) string {
	GinkgoHelper()
	path := filepath.Join(dir, name)
	Expect(os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755)).To(Succeed())
	return path
}

// The protocol is the whole contract between captain and a host's fixture
// runner, so every way a runner can break it has to end in an error rather than
// a verdict: a verdict invented from broken output is a definition of done that
// silently passed.
var _ = Describe("the external fixture verifier's stdout contract", func() {
	ctx := context.Background()

	It("reports a runner that never finished as a timeout, not a verdict", func() {
		dir := GinkgoT().TempDir()
		verifier := &ExternalVerifier{
			Command: []string{writeScript(dir, "slow.sh", "sleep 30\n")},
			Fixture: "# acceptance\n",
			Timeout: 200 * time.Millisecond,
		}

		vd, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(ContainSubstring("timed out after 200ms")))
		Expect(vd.Report).To(BeNil(), "a check with no answer must not report one")
	})

	It("names the process error when the runner binary cannot be executed", func() {
		dir := GinkgoT().TempDir()
		missing := filepath.Join(dir, "gavle") // the classic transposed typo
		verifier := &ExternalVerifier{Command: []string{missing}, Fixture: "# acceptance\n"}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(And(
			ContainSubstring("no report line"),
			ContainSubstring(missing),
			ContainSubstring("no such file or directory"),
		)), "exec's own error is the only thing that names the misspelled runner")
	})

	It("refuses a runner that emits two report lines", func() {
		dir := GinkgoT().TempDir()
		stdout := ndjson("report", passingReport("first")) + ndjson("report", passingReport("second"))
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, stdout, 0)}, Fixture: "# acceptance\n",
		}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(ContainSubstring("more than one report line")))
	})

	It("refuses a stdout line carrying neither key, and quotes the line", func() {
		dir := GinkgoT().TempDir()
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, "{\"summary\":{\"passed\":1}}\n", 0)},
			Fixture: "# acceptance\n",
		}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(And(
			ContainSubstring(`carried neither "progress" nor "report"`),
			ContainSubstring(`{"summary":{"passed":1}}`),
		)))
	})

	It("still parses a final report line the runner left unterminated", func() {
		dir := GinkgoT().TempDir()
		stdout := strings.TrimSuffix(ndjson("report", passingReport("go test ./...")), "\n")
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, stdout, 0)}, Fixture: "# acceptance\n",
		}

		vd, err := verifier.Verify(ctx, dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(vd.OK).To(BeTrue())
		Expect(vd.Report.Tests[0].Name).To(Equal("go test ./..."))
	})

	It("refuses a report whose verdict contradicts its own state", func() {
		dir := GinkgoT().TempDir()
		contradictory := api.VerifyReport{
			Kind: "fixture", Name: "acceptance", Ran: true, Passed: true, State: api.VerifyStateFailed,
		}
		verifier := &ExternalVerifier{
			Command: []string{fixtureRunnerScript(dir, ndjson("report", contradictory), 0)},
			Fixture: "# acceptance\n",
		}

		_, err := verifier.Verify(ctx, dir, nil)
		Expect(err).To(MatchError(ContainSubstring(`passed=true with state "failed"`)))
	})

	It("runs the runner through the confinement wrapper, argv intact", func() {
		dir := GinkgoT().TempDir()
		runner := fixtureRunnerScript(dir, ndjson("report", passingReport("ok")), 0)
		marker := filepath.Join(dir, "wrapped.txt")
		wrapper := writeScript(dir, "wrapper.sh", "echo wrapped > "+marker+"\nexec \"$@\"\n")

		var sawCmd string
		var sawArgs []string
		verifier := &ExternalVerifier{
			Command: []string{runner, "check"},
			Fixture: "# acceptance\n",
			Wrap: func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
				sawCmd, sawArgs = cmd, args
				return wrapper, append([]string{cmd}, args...), env, nil
			},
		}

		vd, err := verifier.Verify(ctx, dir, []string{"pkg/a.go"})
		Expect(err).NotTo(HaveOccurred())
		Expect(vd.OK).To(BeTrue())
		Expect(sawCmd).To(Equal(runner), "the wrapper is handed the runner, not a shell")
		Expect(sawArgs).To(Equal([]string{"check", "--cwd", dir, "--changed", "pkg/a.go"}))
		Expect(marker).To(BeAnExistingFile(), "the wrapped command is what actually ran")
	})
})

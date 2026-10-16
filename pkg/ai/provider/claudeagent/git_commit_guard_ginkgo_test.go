package claudeagent

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("git commit guard", func() {
	// The guard exists because captain cuts the run's commits; an agent committing
	// its own would race them. A run captain does not commit for — a rebase whose
	// agent must `git add` a resolved file — has nothing to guard.
	DescribeTable("is requested from the bridge only when the run's commits are owned",
		func(workflow *api.Workflow, want bool) {
			params, err := (&Provider{}).initializeParams(ai.Request{Workflow: workflow})
			Expect(err).NotTo(HaveOccurred())
			Expect(params.GuardGitCommits).To(Equal(want))
		},
		Entry("no workflow", nil, false),
		Entry("verify only", &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}, false),
		Entry("after steps only", &api.Workflow{After: []api.AfterStep{{Commands: []string{"true"}}}}, false),
		Entry("declared commits", &api.Workflow{Commits: []api.Commit{{On: api.CommitOnRun}}}, true),
	)

	It("leaves every non-guarded Bash command to the permission rules (agent.ts hook contract)", func() {
		node, err := exec.LookPath("node")
		Expect(err).NotTo(HaveOccurred(), "node is required to run the bridge's hook tests")
		// go test runs in the package directory, beside bash_guard.ts.
		out, err := exec.Command(node, "--test", "bash_guard.test.ts").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	})
})

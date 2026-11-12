package gitagent

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// readServices re-checks its path at the read: the tunnel's task and repo come
// from the network, so a caller forgetting ValidateTaskID must not let a task
// name reach a file outside the repo's task state directory.
var _ = Describe("task services registry path", func() {
	var repo string

	BeforeEach(func() {
		repo = filepath.Join(GinkgoT().TempDir(), "mailbox.git")
		Expect(os.MkdirAll(filepath.Join(repo, "captain", "tasks"), 0o755)).To(Succeed())
	})

	DescribeTable("refuses a task name that would leave the task state directory",
		func(task string) {
			outside := filepath.Join(repo, "captain", servicesFile)
			Expect(os.WriteFile(outside, []byte(`{"tools":"127.0.0.1:1"}`), 0o600)).To(Succeed())

			_, err := readServices(repo, task)

			Expect(err).To(MatchError(ContainSubstring("must match")))
		},
		Entry("parent traversal", ".."),
		Entry("nested traversal", "t-1/../.."),
		Entry("absolute path", "/etc"),
	)

	DescribeTable("refuses a task state read outside the task state directory",
		func(task string) {
			_, _, err := loadTaskStateUnlocked(repo, task)
			Expect(err).To(MatchError(ContainSubstring("not a local path")))
		},
		Entry("parent traversal", "../x"),
		Entry("absolute path", "/etc"),
	)

	It("reads a valid task's registry from inside the task state directory", func() {
		Expect(WriteServices(repo, "t-1", Services{"tools": "127.0.0.1:1"})).To(Succeed())

		services, err := readServices(repo, "t-1")

		Expect(err).NotTo(HaveOccurred())
		Expect(services).To(Equal(Services{"tools": "127.0.0.1:1"}))
	})
})

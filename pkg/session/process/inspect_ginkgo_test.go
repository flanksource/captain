package process

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("inspected process labelling", func() {
	DescribeTable("names a process even when it is not an agent",
		func(command, expected string) {
			Expect(InspectedSource(command)).To(Equal(expected))
		},
		Entry("claude keeps its agent source", claudeInCaptain, "claude"),
		Entry("codex keeps its agent source", "/Users/acme/.local/bin/codex-darwin-arm64 exec", "codex"),
		Entry("captain names itself rather than reporting nothing", captainServeCommand, "captain"),
		Entry("agent-browser daemon", "/Users/acme/Library/pnpm/agent-browser daemon --session designpg", "agent-browser"),
		Entry("a plain shell", "-zsh", "-zsh"),
		Entry("quoted executable", `"/usr/bin/vim" notes.md`, "vim"),
		Entry("empty command", "", ""),
	)

	It("drops repeated PIDs so one process is reported once", func() {
		Expect(dedupePIDs([]int{7, 3, 7, 3, 9})).To(Equal([]int{7, 3, 9}))
	})

	It("reports nothing without erroring when no PID was requested", func() {
		agents, err := Inspect(GinkgoT().Context(), InspectOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(agents).To(BeEmpty())
	})

	It("errors on a PID that is not running rather than returning an empty list", func() {
		// PID 0 is never a live user process, so this exercises the miss path
		// against the real host snapshot without depending on what is running.
		_, err := Inspect(GinkgoT().Context(), InspectOptions{PIDs: []int{0}})
		Expect(err).To(MatchError(ContainSubstring("no such process: 0")))
	})
})

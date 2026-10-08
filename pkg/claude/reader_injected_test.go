package claude

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// flaggedUserLine builds a text-only user record carrying one of Claude's
// top-level flags for a message the person did not type.
func flaggedUserLine(uuid, flag, text string) string {
	return fmt.Sprintf(
		`{"type":"user","uuid":%q,"sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","cwd":"/repo",%q:true,"message":{"role":"user","content":[{"type":"text","text":%s}]}}`,
		uuid, flag, jsonString(text))
}

var _ = Describe("user lines Claude injected on the person's behalf", func() {
	DescribeTable("are marked injected and keep the provider's user role",
		func(line string) {
			entries := readEntries(line)

			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Injected).To(BeTrue())
			Expect(entries[0].Message.Role).To(Equal(MessageRoleUser))
		},
		Entry("a meta message", flaggedUserLine("u1", "isMeta", "Continue from where you left off.")),
		Entry("a compaction summary", flaggedUserLine("u2", "isCompactSummary", "This session is being continued from a previous conversation.")),
		Entry("an interrupt", userLine("u3", "[Request interrupted by user]")),
		Entry("an interrupt during a tool call", userLine("u4", "[Request interrupted by user for tool use]")),
	)

	It("leaves a message the person typed unmarked", func() {
		entries := readEntries(userLine("u5", "Implement the plan"), flaggedUserLine("u6", "isMeta", "Implement the plan"))

		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Injected).To(BeFalse())
		Expect(entries[1].Injected).To(BeTrue())
	})
})

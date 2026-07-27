package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Codex file changes", func() {
	ginkgo.It("collects native apply_patch write paths", func() {
		stream := strings.Join([]string{
			`{"timestamp":"2026-07-26T17:00:00Z","type":"session_meta","payload":{"id":"patch-session","cwd":"/repo"}}`,
			`{"timestamp":"2026-07-26T17:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"patch-1","input":"*** Begin Patch\n*** Update File: /repo/pkg/existing.go\n*** Move to: /repo/pkg/moved.go\n*** Add File: /repo/pkg/added.go\n+content := \"*** Delete File: /repo/pkg/ignored.go\"\n*** Delete File: /repo/pkg/deleted.go\n*** Update File: /repo/pkg/existing.go\n*** End Patch"}}`,
			`{"timestamp":"2026-07-26T17:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"patch-1","output":"Success"}}`,
		}, "\n")

		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).NotTo(HaveOccurred())

		built := buildCodexSession(uses, &history.CodexSessionInfo{ID: "patch-session", CWD: "/repo"})

		Expect(built.Files.Written).To(Equal([]string{
			"pkg/added.go",
			"pkg/deleted.go",
			"pkg/existing.go",
			"pkg/moved.go",
		}))
	})
})

package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/claude"
)

var _ = Describe("tool write-path analysis", func() {
	It("extracts paths from native apply_patch input", func() {
		analysis := AnalyzeToolUseLegacy(claude.ToolUse{
			Tool: "apply_patch",
			Input: map[string]any{"input": `*** Begin Patch
*** Delete File: pkg/deleted.go
*** End Patch`},
			CWD: "/repo",
		}, "/repo")

		Expect(analysis.WritePaths).To(Equal([]string{"/repo/pkg/deleted.go"}))
	})

	It("extracts apply_patch paths embedded in Codex custom tool input", func() {
		analysis := AnalyzeToolUseLegacy(claude.ToolUse{
			Tool:  "exec",
			Input: map[string]any{"input": `const patch = "*** Begin Patch\n*** Update File: /repo/old.go\n*** Move to: /repo/new.go\n-old := \"*** Update File: /repo/ignored.go\"\n+new := true\n*** Add File: /repo/nested/added.go\n*** End Patch"; tools.apply_patch(patch);`},
		}, "/repo")

		Expect(analysis.WritePaths).To(ConsistOf("/repo/old.go", "/repo/new.go", "/repo/nested/added.go"))
	})

	It("reads bash redirections through the shell parser, not the raw command text", func() {
		// `<>` is SQL's not-equal operator inside a quoted argument, and `mkdir`
		// here is a literal being matched, not a command. Scanning the raw string
		// for `>` or `mkdir` mistakes both for writes.
		analysis := AnalyzeToolUseLegacy(claude.ToolUse{
			Tool: "Bash",
			Input: map[string]any{"command": `psql -c "SELECT count(*) FROM t WHERE parent_id <> root_id) AND name <> 'mkdir /etc/passwd'" > out.txt`},
		}, "/repo")

		Expect(analysis.WritePaths).To(Equal([]string{"/repo/out.txt"}))
	})
})

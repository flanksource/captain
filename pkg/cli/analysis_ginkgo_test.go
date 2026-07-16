package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/claude"
)

var _ = Describe("tool write-path analysis", func() {
	It("extracts apply_patch paths embedded in Codex custom tool input", func() {
		analysis := AnalyzeToolUseLegacy(claude.ToolUse{
			Tool:  "exec",
			Input: map[string]any{"input": `const patch = "*** Begin Patch\n*** Update File: /repo/old.go\n*** Move to: /repo/new.go\n-old := \"*** Update File: /repo/ignored.go\"\n+new := true\n*** Add File: /repo/nested/added.go\n*** End Patch"; tools.apply_patch(patch);`},
		}, "/repo")

		Expect(analysis.WritePaths).To(ConsistOf("old.go", "new.go", "nested/added.go"))
	})
})

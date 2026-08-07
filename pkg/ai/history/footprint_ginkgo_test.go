package history

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ToolFootprint", func() {
	footprint := func(tool string, input map[string]any) Footprint {
		return ToolFootprint(ToolUse{Tool: tool, Input: input})
	}

	ginkgo.DescribeTable("attributes a tool's writes",
		func(tool string, input map[string]any, expected []string) {
			Expect(footprint(tool, input).Written).To(Equal(expected))
		},
		ginkgo.Entry("Edit names its file",
			"Edit", map[string]any{"file_path": "pkg/a.go"}, []string{"pkg/a.go"}),
		ginkgo.Entry("Write names its file",
			"Write", map[string]any{"file_path": "pkg/a.go"}, []string{"pkg/a.go"}),
		ginkgo.Entry("MultiEdit names one file for many edits",
			"MultiEdit", map[string]any{"file_path": "pkg/a.go"}, []string{"pkg/a.go"}),
		ginkgo.Entry("NotebookEdit names notebook_path, which file_path lookups miss",
			"NotebookEdit", map[string]any{"notebook_path": "nb.ipynb"}, []string{"nb.ipynb"}),
		ginkgo.Entry("a shell redirect is a write no input lookup can see",
			"Bash", map[string]any{"command": "echo hi > out.txt"}, []string{"out.txt"}),
		ginkgo.Entry("sed -i rewrites in place",
			"Bash", map[string]any{"command": "sed -i '' s/a/b/ pkg/a.go"}, []string{"pkg/a.go"}),
		ginkgo.Entry("a patch piped through the shell touches every file in it",
			"Bash", map[string]any{"command": "apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: pkg/a.go\n*** Update File: pkg/b.go\n*** End Patch\nEOF"},
			[]string{"pkg/a.go", "pkg/b.go"}),
		ginkgo.Entry("codex exec carries the patch in input",
			"exec", map[string]any{"input": "*** Begin Patch\n*** Update File: pkg/a.go\n*** End Patch"},
			[]string{"pkg/a.go"}),
		ginkgo.Entry("a codex script carries the patch in script",
			"CodexExecScript", map[string]any{"script": "*** Begin Patch\n*** Update File: pkg/a.go\n*** End Patch"},
			[]string{"pkg/a.go"}),
	)

	ginkgo.DescribeTable("attributes a tool's reads",
		func(tool string, input map[string]any, expected []string) {
			Expect(footprint(tool, input).Read).To(Equal(expected))
		},
		ginkgo.Entry("Read names its file", "Read", map[string]any{"file_path": "pkg/a.go"}, []string{"pkg/a.go"}),
		ginkgo.Entry("Grep names its search root", "Grep", map[string]any{"path": "pkg"}, []string{"pkg"}),
		ginkgo.Entry("Glob names its search root", "Glob", map[string]any{"path": "pkg"}, []string{"pkg"}),
	)

	ginkgo.It("drops /dev/null, which a patch names as the empty side of a hunk", func() {
		written := footprint("exec", map[string]any{
			"input": "*** Begin Patch\n*** Delete File: pkg/gone.go\n*** End Patch",
		}).Written

		Expect(written).NotTo(ContainElement("/dev/null"))
	})

	ginkgo.It("counts a renamed file's destination as written", func() {
		Expect(footprint("exec", map[string]any{
			"input": "*** Begin Patch\n*** Update File: pkg/old.go\n*** Move to: pkg/new.go\n*** End Patch",
		}).Written).To(ContainElements("pkg/old.go", "pkg/new.go"))
	})

	ginkgo.It("classifies a file that is read and then rewritten as a write only", func() {
		result := footprint("Bash", map[string]any{"command": "sed -i '' s/a/b/ pkg/a.go"})

		Expect(result.Written).To(ContainElement("pkg/a.go"))
		Expect(result.Read).NotTo(ContainElement("pkg/a.go"))
	})

	ginkgo.It("reports the same path once however many times a tool names it", func() {
		Expect(footprint("Bash", map[string]any{
			"command": "echo a > out.txt; echo b >> out.txt",
		}).Written).To(Equal([]string{"out.txt"}))
	})

	ginkgo.DescribeTable("touches nothing",
		func(tool string, input map[string]any) {
			result := footprint(tool, input)

			Expect(result.Written).To(BeNil())
			Expect(result.Read).To(BeNil())
		},
		ginkgo.Entry("a tool with no file surface", "WebSearch", map[string]any{"query": "x"}),
		ginkgo.Entry("an empty input", "Edit", map[string]any{}),
		ginkgo.Entry("a non-string path", "Edit", map[string]any{"file_path": 42}),
		ginkgo.Entry("an empty bash command", "Bash", map[string]any{"command": ""}),
		ginkgo.Entry("a nil input", "Edit", nil),
	)
})

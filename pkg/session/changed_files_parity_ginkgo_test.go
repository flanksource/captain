package session

import (
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The Claude and Codex builders used to decide which files a session changed
// with two different rule sets, and `captain changes` used a third. The same
// edits therefore produced different stored file lists depending on which agent
// ran, and a different list again depending on which surface asked. All three
// now resolve through history.ToolFootprint; these specs pin that they agree.
var _ = ginkgo.Describe("changed-file derivation parity", func() {
	const repo = "/repo"

	edits := []struct {
		tool  string
		input map[string]any
	}{
		{"Edit", map[string]any{"file_path": "/repo/pkg/edited.go"}},
		{"Write", map[string]any{"file_path": "/repo/pkg/written.go"}},
		{"MultiEdit", map[string]any{"file_path": "/repo/pkg/multi.go"}},
		{"NotebookEdit", map[string]any{"notebook_path": "/repo/analysis.ipynb"}},
		{"Bash", map[string]any{"command": "sed -i '' s/a/b/ /repo/pkg/sedded.go"}},
		{"Bash", map[string]any{"command": "echo x > /repo/pkg/redirected.go"}},
		{"Bash", map[string]any{
			"command": "apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: /repo/pkg/patched.go\n*** Delete File: /repo/pkg/removed.go\n*** End Patch\nEOF",
		}},
		{"Read", map[string]any{"file_path": "/repo/pkg/read.go"}},
	}

	expected := []string{
		"pkg/edited.go", "pkg/multi.go", "pkg/patched.go", "pkg/redirected.go",
		"pkg/removed.go", "pkg/sedded.go", "pkg/written.go", "analysis.ipynb",
	}

	claudeFiles := func() ChangedFiles {
		uses := make([]claude.ToolUse, 0, len(edits))
		for _, edit := range edits {
			uses = append(uses, claude.ToolUse{
				Tool: edit.tool, Input: edit.input, CWD: repo, ProjectRoot: repo,
			})
		}
		return changedFiles(uses)
	}

	codexFiles := func() ChangedFiles {
		var read, written []string
		for _, edit := range edits {
			collectCodexPaths(history.ToolUse{Tool: edit.tool, Input: edit.input, CWD: repo}, &read, &written)
		}
		return ChangedFiles{
			Read:    sortedUnique(relativizeAll(read, repo)),
			Written: sortedUnique(relativizeAll(written, repo)),
		}
	}

	ginkgo.It("attributes the same writes on both backends", func() {
		Expect(claudeFiles().Written).To(ConsistOf(expected))
		Expect(codexFiles().Written).To(ConsistOf(expected))
	})

	ginkgo.It("attributes the same reads on both backends", func() {
		Expect(claudeFiles().Read).To(Equal(codexFiles().Read))
		Expect(claudeFiles().Read).To(ContainElement("pkg/read.go"))
	})

	ginkgo.It("agrees with the set captain changes reports", func() {
		// captain changes resolves the same footprint per tool use; comparing the
		// aggregate here is what stops the CLI and the stored row from drifting.
		var reported []string
		for _, edit := range edits {
			for _, path := range history.ToolFootprint(history.ToolUse{
				Tool: edit.tool, Input: edit.input, CWD: repo,
			}).Written {
				reported = append(reported, claude.RelativePath(path, repo))
			}
		}

		Expect(sortedUnique(reported)).To(Equal(claudeFiles().Written))
	})

	ginkgo.It("never reports /dev/null, which a delete hunk names", func() {
		Expect(claudeFiles().Written).NotTo(ContainElement("/dev/null"))
		Expect(codexFiles().Written).NotTo(ContainElement("/dev/null"))
	})
})

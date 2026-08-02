package tools

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Bash shell metadata", func() {
	It("renders the transformed shell and actual command in every format", func() {
		tool := &BashTool{BaseTool: BaseTool{Input: map[string]any{
			"command": `python -c 'print(42)'`,
			"shell":   "zsh",
		}}}

		Expect(tool.Name()).To(Equal("Bash"))
		Expect(tool.Pretty().String()).To(And(
			ContainSubstring("zsh"),
			ContainSubstring(`python -c 'print(42)'`),
			Not(ContainSubstring("/bin/zsh -lc")),
		))
		Expect(tool.Detail().Markdown()).To(ContainSubstring(`python -c 'print(42)'`))
	})
})

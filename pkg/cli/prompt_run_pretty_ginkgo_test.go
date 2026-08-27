package cli

import (
	"strings"

	clickyapi "github.com/flanksource/clicky/api"
	clickymarkdown "github.com/flanksource/clicky/markdown"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("multi-model prompt output", func() {
	It("renders a single Markdown response across terminal and document formats", func() {
		response := "# Database sizes\n\nUse **pg_database**.\n\n```sql\nSELECT datname FROM pg_database;\n```\n"
		pretty := (PromptRunResult{Status: "completed", Text: response}).Pretty()

		Expect(pretty.String()).To(And(
			ContainSubstring("Database sizes"),
			ContainSubstring("SELECT datname"),
			Not(ContainSubstring("```")),
		))
		Expect(pretty.ANSI()).To(And(
			ContainSubstring("Database sizes"),
			ContainSubstring("SELECT"),
			ContainSubstring("\x1b["),
			Not(ContainSubstring("```")),
		))
		Expect(pretty.Markdown()).To(And(
			ContainSubstring("# Database sizes"),
			ContainSubstring("```sql"),
		))
		Expect(pretty.HTML()).To(And(
			ContainSubstring("<h1>Database sizes</h1>"),
			ContainSubstring("SELECT"),
		))
	})

	It("treats a leading frontmatter delimiter as response content", func() {
		pretty := (PromptRunResult{Text: "---\ntitle: response body\n---\n\nResult text.\n"}).Pretty()

		Expect(pretty.Children).To(HaveLen(1))
		document, ok := pretty.Children[0].(*clickymarkdown.Document)
		Expect(ok).To(BeTrue())
		Expect(document.Metadata).To(BeEmpty())
		Expect(document.String()).To(ContainSubstring("title: response body"))
	})

	It("renders metadata in the table and full responses after it", func() {
		pretty := (PromptRunResult{
			Status: "completed", Total: 2, Succeeded: 2,
			Runs: []PromptRunItem{
				{Selector: "api:sonnet-5", Status: "completed", Text: "first full response"},
				{Selector: "cmux:opus", Status: "completed", Text: "second full response"},
			},
		}).Pretty()

		tableIndex := -1
		var table clickyapi.TextTable
		for i, child := range pretty.Children {
			if candidate, ok := child.(clickyapi.TextTable); ok {
				tableIndex = i
				table = candidate
				break
			}
		}
		Expect(tableIndex).To(BeNumerically(">=", 0))
		metrics := make([]string, 0, len(table.Rows))
		for _, row := range table.Rows {
			metrics = append(metrics, row["metric"].String())
		}
		Expect(metrics).NotTo(ContainElement("Response"))

		var afterTable strings.Builder
		for _, child := range pretty.Children[tableIndex+1:] {
			afterTable.WriteString(child.String())
		}
		output := afterTable.String()
		Expect(output).To(ContainSubstring("api:sonnet-5"))
		Expect(output).To(ContainSubstring("first full response"))
		Expect(output).To(ContainSubstring("cmux:opus"))
		Expect(output).To(ContainSubstring("second full response"))
		Expect(strings.Index(output, "first full response")).To(BeNumerically("<", strings.Index(output, "second full response")))
	})
})

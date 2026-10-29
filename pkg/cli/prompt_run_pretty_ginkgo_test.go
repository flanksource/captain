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

var _ = Describe("Jev prompt response", func() {
	const response = `{"value":{"input_valid":0.8,"in_scope":"yes","confidence":1.25},"typesafe":{"model":"jev-1.14.0","answers":{"input_valid":{"type":"noul","noul":0.8},"in_scope":{"type":"choice","choice":"yes","confidence":0.9,"probabilities":{"yes":0.9,"no":0.1}},"confidence":{"type":"score","score":1.25,"confidence":0.7,"legend":{"0":"Low","1":"Medium","2":"High"},"probabilities":{"0":0,"1":0.75,"2":0.25}}},"usage":{"input_tokens":12,"output_tokens":3}}}`

	It("shows typed answers and confidence as a readable table", func() {
		pretty := (PromptRunResult{Provider: "jev", Text: response}).Pretty()
		Expect(pretty.String()).To(And(
			ContainSubstring("Model: jev-1.14.0"),
			ContainSubstring("Tokens: 12 in / 3 out"),
			ContainSubstring("Input Valid"),
			ContainSubstring("80% yes"),
			ContainSubstring("In Scope"),
			ContainSubstring("90%"),
			ContainSubstring("1.25 / 2"),
			ContainSubstring("70%"),
			Not(ContainSubstring(`"typesafe"`)),
		))
		Expect(pretty.ANSI()).To(And(ContainSubstring("Input Valid"), ContainSubstring("1.25 / 2")))
		Expect(pretty.Markdown()).To(And(
			ContainSubstring("Input Valid"),
			ContainSubstring("In Scope"),
			ContainSubstring("Low 0%, Medium 75%, High 25%"),
			ContainSubstring("no 10%, yes 90%"),
		))
		Expect(pretty.HTML()).To(And(ContainSubstring("new gridjs.Grid"), ContainSubstring("1.25 / 2")))
	})

	Context("a batched run's block", func() {
		const base = "/x/jira/10303"

		It("names the input relative to the common directory and renders the full answer table", func() {
			block := inputRunBlock(PromptRunItem{
				Inputs: []string{base + "/RALT-1321.md"}, Status: "completed", Provider: "jev", Text: response, Duration: "1.7s",
			}, base)
			Expect(block.String()).To(And(
				ContainSubstring("✓ RALT-1321.md"), ContainSubstring("1.7s"), Not(ContainSubstring(base)),
				ContainSubstring("80% yes"), ContainSubstring("no 10%, yes 90%"), Not(ContainSubstring(`"typesafe"`)),
			))
		})

		It("shows a skip reason without the common directory, and a failure's error", func() {
			skipped := inputRunBlock(PromptRunItem{
				Inputs: []string{base + "/RALT-1400.md"}, Status: "skipped", SkipReason: base + "/RALT-1400.md: already classified",
			}, base)
			Expect(skipped.String()).To(And(ContainSubstring("⊘ RALT-1400.md"), ContainSubstring("RALT-1400.md: already classified"), Not(ContainSubstring(base))))

			failed := inputRunBlock(PromptRunItem{Inputs: []string{base + "/RALT-1401.md"}, Status: "failed", Error: "API key not found"}, base)
			Expect(failed.String()).To(And(ContainSubstring("✗ RALT-1401.md"), ContainSubstring("API key not found")))
		})

		It("leaves the batch's final print to a one-line tally", func() {
			tally := PromptRunResult{Status: "partial", Total: 3, Succeeded: 1, Failed: 1, Skipped: 1, Duration: "6.2s", Runs: []PromptRunItem{
				{Inputs: []string{base + "/a.md"}, Status: "completed", Provider: "jev", Text: response},
				{Inputs: []string{base + "/b.md"}, Status: "failed", Error: "boom"},
				{Inputs: []string{base + "/c.md"}, Status: "skipped", SkipReason: "done"},
			}}.Pretty().String()
			Expect(tally).To(And(
				ContainSubstring("✓ 1 completed"), ContainSubstring("✗ 1 failed"), ContainSubstring("⊘ 1 skipped"),
				ContainSubstring("3 files in "+base), ContainSubstring("6.2s"),
				Not(ContainSubstring("80% yes")), Not(ContainSubstring("boom")), Not(ContainSubstring("Inputs")),
			))
			Expect(strings.Count(strings.TrimSpace(tally), "\n")).To(BeZero())
		})
	})

	It("uses the same answer presentation in a multi-model run", func() {
		pretty := (PromptRunResult{Runs: []PromptRunItem{{Provider: "jev", Selector: "api:jev-latest", Text: response}}}).Pretty()
		Expect(pretty.String()).To(And(ContainSubstring("api:jev-latest"), ContainSubstring("80% yes")))
	})

	It("renders a scalar zero probability without dropping the answer", func() {
		response := `{"value":0,"typesafe":{"model":"jev-1.14.0","answers":{"value":{"type":"noul","noul":0}},"usage":{"input_tokens":1,"output_tokens":1}}}`
		pretty := (PromptRunResult{Provider: "jev", Text: response}).Pretty()
		Expect(pretty.String()).To(And(ContainSubstring("Value"), ContainSubstring("0% yes")))
	})
})

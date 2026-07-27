package cli

import (
	"strings"

	clickyapi "github.com/flanksource/clicky/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("multi-model prompt output", func() {
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

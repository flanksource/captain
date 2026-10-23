package prompt

import (
	"os"

	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Inline tool frontmatter", func() {
	It("renders run variables into the declaration and decodes it onto the spec", func() {
		source, err := os.ReadFile("testdata/inline_tools.prompt")
		Expect(err).NotTo(HaveOccurred())

		spec, _, err := LoadNamed("inline_tools.prompt", string(source)).Render(RenderOptions{
			Data: map[string]any{"project": "acme ops", "ticket": "T-1"}, Declared: true,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Prompt.User).To(Equal("Summarise ticket T-1."))
		Expect(spec.Tools).To(Equal(api.InlineTools{"lookup_ticket": {
			Setup:       shell.Setup{DotEnv: []string{".env"}},
			Description: "Fetch a ticket by id",
			Input:       map[string]any{"id": "string"},
			Output:      map[string]any{"title": "string", "status": "string"},
			Exec:        "./hack/ticket.sh --project 'acme ops'",
			ReadOnly:    true,
			Timeout:     "30s",
			Retry:       &api.ToolRetry{Attempts: 3, Delay: "1s", ExitCodes: []int{75}},
		}}))
	})
})

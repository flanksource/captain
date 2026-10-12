package query

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("providerForHistorySource", func() {
	anthropic := "anthropic"
	apiRun := database.SessionOverview{ModelProvider: &anthropic}

	DescribeTable("names the recovered transcript's provider",
		func(source string, overview database.SessionOverview, want string) {
			Expect(providerForHistorySource(source, overview)).To(Equal(want))
		},
		Entry("an API transcript takes the run's model provider", api.TranscriptSourceAPI, apiRun, "anthropic"),
		Entry("a claude transcript is anthropic", "claude", database.SessionOverview{}, "anthropic"),
		Entry("a codex transcript is openai", "codex", database.SessionOverview{}, "openai"),
	)
})

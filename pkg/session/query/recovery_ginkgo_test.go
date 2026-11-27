package query

import (
	"os"
	"path/filepath"

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

// recoveryFixtureID is the root session id recorded in recoveryFixture.
const recoveryFixtureID = "provider-recovery"

var recoveryFixture = filepath.Join("..", "..", "aichat", "testdata", "recovery-claude.jsonl")

// isolatedHistoryHome points Claude and Codex history at an empty home.
func isolatedHistoryHome() string {
	GinkgoHelper()
	home := GinkgoT().TempDir()
	GinkgoT().Setenv("HOME", home)
	GinkgoT().Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	return home
}

var _ = Describe("locateProviderHistory", func() {
	It("names the Claude source of a transcript it found for a run that recorded no model", func() {
		home := isolatedHistoryHome()
		fixture, err := os.ReadFile(recoveryFixture)
		Expect(err).NotTo(HaveOccurred())
		want := filepath.Join(home, ".claude", "projects", "-repo", recoveryFixtureID+".jsonl")
		Expect(os.MkdirAll(filepath.Dir(want), 0o755)).To(Succeed())
		Expect(os.WriteFile(want, fixture, 0o600)).To(Succeed())

		source, path, err := locateProviderHistory(providerHistorySource(database.SessionOverview{}, nil), recoveryFixtureID, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect([]string{source, path}).To(Equal([]string{"claude", want}))
		recovered, err := parseProviderHistory(source, path, recoveryFixtureID)
		Expect(err).NotTo(HaveOccurred())
		Expect(recovered.Messages).NotTo(BeEmpty())
	})

	It("reports a provider session found in neither history", func() {
		isolatedHistoryHome()

		_, _, err := locateProviderHistory("", "provider-missing", nil)

		Expect(err).To(MatchError(ContainSubstring("provider-missing")))
	})
})

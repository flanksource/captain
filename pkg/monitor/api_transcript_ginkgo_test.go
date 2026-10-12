package monitor

import (
	"encoding/json"
	"path/filepath"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An API-mode run has no agent writing a log, so Captain writes one in Claude
// Code's format under its own directory. It must land on the session the run
// registered, by id, like any agent transcript.
var _ = Describe("API transcripts", Ordered, func() {
	const model = "claude-haiku-5-5"
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_api_transcript"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	BeforeEach(func() { GinkgoT().Setenv("HOME", GinkgoT().TempDir()) })

	It("ingests a registered API transcript with its tool calls, usage and cost", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: api.TranscriptSourceAPI, Provider: "anthropic", HostID: "api-test",
		})
		Expect(err).NotTo(HaveOccurred())
		path, err := history.APITranscriptPath(providerSessionID)
		Expect(err).NotTo(HaveOccurred())
		transcript, err := claude.CreateSyntheticTranscript(path, providerSessionID, "/home/dev/example")
		Expect(err).NotTo(HaveOccurred())
		Expect(transcript.Start(model, []string{"lookup"})).To(Succeed())
		Expect(transcript.Append(model, api.Message{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Find alpha."}}}, nil)).To(Succeed())
		Expect(transcript.Append(model, api.Message{Role: api.RoleAssistant, Parts: []api.Part{{
			Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "toolu_lookup", Name: "lookup", Input: json.RawMessage(`{"key":"alpha"}`)},
		}}}, &api.Usage{InputTokens: 2000, OutputTokens: 500})).To(Succeed())
		Expect(transcript.Append(model, api.Message{Role: api.RoleTool, Parts: []api.Part{{
			Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "toolu_lookup", Output: json.RawMessage(`{"value":"found"}`)},
		}}}, nil)).To(Succeed())
		Expect(transcript.Append(model, api.Message{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: `{"status":`}}},
			&api.Usage{InputTokens: 3000, OutputTokens: 700})).To(Succeed())
		Expect(transcript.Fail(model, "schema validation failed: data is not valid JSON")).To(Succeed())
		Expect(transcript.Close()).To(Succeed())

		registered, err := RegisterTranscriptSource(ctx, db, session.ID, providerSessionID, api.TranscriptSourceAPI)
		Expect(err).NotTo(HaveOccurred())
		Expect(registered).To(Equal(path))
		monitor, err := New(Config{DB: db, HostID: "api-test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(newIngestor(monitor).ingestFile(ctx, api.TranscriptSourceAPI, path)).To(Succeed())

		overview, err := db.GetSessionOverviewByIdentity(ctx, session.ID.String())
		Expect(err).NotTo(HaveOccurred())
		Expect(overview.Source).To(Equal(api.TranscriptSourceAPI))
		Expect(overview.InputTokens).To(BeEquivalentTo(5000))
		Expect(overview.OutputTokens).To(BeEquivalentTo(1200))
		Expect(overview.CostUSD).To(BeNumerically(">", 0))
		Expect(overview.ToolCallCount).To(BeEquivalentTo(1))
		Expect(overview.ModelProvider).To(HaveValue(Equal("anthropic")))
		Expect(overview.ModelMode).To(HaveValue(Equal(string(api.ModeAPI))))
	})

	It("refuses an API transcript path outside Captain's transcript directory", func(ctx SpecContext) {
		monitor, err := New(Config{DB: db, HostID: "api-test"})
		Expect(err).NotTo(HaveOccurred())
		outside := filepath.Join(GinkgoT().TempDir(), "elsewhere.jsonl")
		Expect(newIngestor(monitor).ingestFile(ctx, api.TranscriptSourceAPI, outside)).
			To(MatchError(ContainSubstring("outside the api session root")))
	})
})

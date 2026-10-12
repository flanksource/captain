package promptrun_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

// apiEventsProvider is eventsProvider on the API runtime, which writes no
// transcript of its own.
type apiEventsProvider struct{ eventsProvider }

func (p *apiEventsProvider) GetModel() string { return apiModel }
func (p *apiEventsProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.Anthropic, api.ModeAPI)
}

const apiModel = "claude-haiku-5-5"

func apiTurn(role api.MessageRole, usage *ai.Usage, parts ...api.Part) ai.Event {
	return ai.Event{Kind: ai.EventTurn, Model: apiModel, Usage: usage, Message: &api.Message{Role: role, Parts: parts}}
}

var _ = Describe("a recorded API-mode prompt run", func() {
	var (
		db       *database.DB
		resolved api.ResolvedSpec
	)

	BeforeEach(func() {
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		db = openRecordDB()
		spec := api.Spec{
			Model:  api.Model{Name: apiModel, Provider: api.Anthropic, Mode: api.ModeAPI, Effort: api.EffortHigh},
			Prompt: api.Prompt{System: "Triage this TODO.", User: "Avoid document payload traversal."},
		}
		spec.SetCwd(GinkgoT().TempDir())
		resolved = api.ResolvedSpec{Spec: spec}
	})

	var tools []api.ToolDefinition
	BeforeEach(func() { tools = nil })

	run := func(ctx SpecContext, events ...ai.Event) (*database.PromptRun, *database.Session, error) {
		GinkgoHelper()
		rec := &promptrun.Recording{
			DB: db, Placement: promptrun.Placement{Sessions: hostTree()}, Origin: "gavel.todos",
			AdmissionKey: "admission-" + uuid.NewString(),
			Runtime: database.PromptRunRuntime{Mode: "run", Driver: "api-claude",
				Requested: database.PromptRunRuntimeSelection{Provider: "anthropic", Mode: "api", Model: apiModel}},
		}
		res, runErr := promptrun.Run(ctx, promptrun.Input{
			Resolved: resolved, Provider: &apiEventsProvider{eventsProvider{events: events}}, Timeout: testTimeout, Record: rec,
			Config: ai.Config{Tools: tools},
		})
		stored, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.ExecutionSessionID).NotTo(BeNil(), "an API run binds the transcript Captain writes for it")
		transcript, err := db.GetSession(ctx, *stored.ExecutionSessionID)
		Expect(err).NotTo(HaveOccurred())
		return stored, transcript, runErr
	}

	readTranscript := func(transcript *database.Session) *session.Session {
		GinkgoHelper()
		Expect(transcript.Source).To(Equal(api.TranscriptSourceAPI))
		Expect(transcript.ProviderSessionID).NotTo(BeEmpty())
		path, err := history.APITranscriptPath(transcript.ProviderSessionID)
		Expect(err).NotTo(HaveOccurred())
		parsed, info, err := session.BuildTranscriptFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.RootSessionID).To(Equal(transcript.ProviderSessionID))
		return parsed
	}

	It("writes every turn, tool call and the failure of a tool loop whose final output was rejected", func(ctx SpecContext) {
		lookup := api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "toolu_search", Name: "todo_search", Input: json.RawMessage(`{"query":"payload"}`)}}
		failure := "schema validation failed: model failed to generate output matching expected schema: data is not valid JSON: unexpected end of JSON input"
		stored, transcript, err := run(ctx,
			apiTurn(api.RoleSystem, nil, api.Part{Type: api.PartText, Text: "Triage this TODO."}),
			apiTurn(api.RoleUser, nil, api.Part{Type: api.PartText, Text: "Avoid document payload traversal."}),
			apiTurn(api.RoleAssistant, &ai.Usage{InputTokens: 2000, OutputTokens: 500}, lookup),
			ai.Event{Kind: ai.EventToolUse, Tool: "todo_search", ToolCallID: "toolu_search", Input: map[string]any{"query": "payload"}, Model: apiModel},
			ai.Event{Kind: ai.EventToolResult, Tool: "todo_search", ToolCallID: "toolu_search", Success: true, Text: "[]", Model: apiModel},
			apiTurn(api.RoleTool, nil, api.Part{Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "toolu_search", Output: json.RawMessage(`[]`)}}),
			apiTurn(api.RoleAssistant, &ai.Usage{InputTokens: 3000, OutputTokens: 700}, api.Part{Type: api.PartText, Text: `{"summary":"Separate`}),
			ai.Event{Kind: ai.EventError, Error: failure, Model: apiModel, Usage: &ai.Usage{InputTokens: 5000, OutputTokens: 1200}},
		)
		Expect(err).To(MatchError(ContainSubstring("unexpected end of JSON input")))
		Expect(stored.State).To(Equal(database.PromptRunStateFailed))

		parsed := readTranscript(transcript)
		var usage api.Usage
		for _, turn := range parsed.Turns {
			usage = usage.Add(turn.Usage)
		}
		Expect(usage).To(Equal(api.Usage{InputTokens: 5000, OutputTokens: 1200}))
		var parts []session.Part
		for _, message := range parsed.Messages {
			parts = append(parts, message.Parts...)
		}
		Expect(parts).To(ContainElements(
			HaveField("Text", "Avoid document payload traversal."),
			And(HaveField("ToolName", "todo_search"), HaveField("ToolCallID", "toolu_search")),
			HaveField("Text", `{"summary":"Separate`),
			And(HaveField("ToolName", "ApiError"), HaveField("Input", ContainSubstring("unexpected end of JSON input"))),
		))

		sources, err := db.ListSessionSources(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(ContainElement(And(
			HaveField("SessionID", transcript.ID), HaveField("SourceKind", api.TranscriptSourceAPI),
		)), "the transcript is registered for ingest")
	})

	It("records the caller tools it offered, in order, with their permission and the schema the provider is sent", func(ctx SpecContext) {
		handler := func(context.Context, map[string]any) (any, error) { return nil, nil }
		tools = []api.ToolDefinition{
			{Name: "todo_search", DefaultPermission: api.ToolPolicyAllow, Handler: handler,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}},
			{Name: "triage_prepare", DefaultPermission: api.ToolPolicyAsk, Handler: handler, InputSchema: map[string]any{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"$ref":    "#/$defs/Envelope",
				"$defs":   map[string]any{"Envelope": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}},
			}},
		}
		stored, transcript, err := run(ctx,
			apiTurn(api.RoleUser, nil, api.Part{Type: api.PartText, Text: "Avoid document payload traversal."}),
			ai.Event{Kind: ai.EventError, Error: "tools.1.custom.input_schema: Field required", Model: apiModel},
		)
		Expect(err).To(HaveOccurred())

		recorded, err := json.Marshal(stored.Metadata["callerTools"])
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(MatchJSON(`[
			{"name":"todo_search","permission":"allow","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"name":"triage_prepare","permission":"ask","inputSchema":{"type":"object","properties":{"title":{"type":"string"}}}}
		]`))
		var init []session.Part
		for _, message := range readTranscript(transcript).Messages {
			for _, part := range message.Parts {
				if part.ToolName == "SessionInit" {
					init = append(init, part)
				}
			}
		}
		Expect(init).To(ConsistOf(HaveField("Input", ContainSubstring(`"tools":["todo_search","triage_prepare"]`))))
	})

	It("records the prompt and the error of a run the provider rejected on its first request", func(ctx SpecContext) {
		_, transcript, err := run(ctx,
			apiTurn(api.RoleSystem, nil, api.Part{Type: api.PartText, Text: "Triage this TODO."}),
			apiTurn(api.RoleUser, nil, api.Part{Type: api.PartText, Text: "Avoid document payload traversal."}),
			ai.Event{Kind: ai.EventError, Error: "tools.2.custom.input_schema: Field required", Model: apiModel},
		)
		Expect(err).To(MatchError(ContainSubstring("input_schema")))

		parsed := readTranscript(transcript)
		Expect(parsed.InitialPrompt).To(Equal("Avoid document payload traversal."))
		var parts []session.Part
		for _, message := range parsed.Messages {
			parts = append(parts, message.Parts...)
		}
		Expect(parts).To(ContainElement(And(HaveField("ToolName", "ApiError"), HaveField("Input", ContainSubstring("tools.2")))))
	})
})

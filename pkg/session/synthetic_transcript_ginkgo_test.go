package session

import (
	"encoding/json"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Synthetic API transcript", func() {
	const (
		sessionID = "0195c1de-4ab8-7000-8000-00000000a91e"
		model     = "claude-haiku-5-5"
		cwd       = "/home/dev/example"
	)

	It("reads back an API tool loop that failed on its final output as a Claude session", func() {
		path := filepath.Join(GinkgoT().TempDir(), "api", sessionID+".jsonl")
		transcript, err := claude.CreateSyntheticTranscript(path, sessionID, cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(transcript.Start(model, []string{"lookup"})).To(Succeed())
		for _, turn := range []struct {
			message api.Message
			usage   *api.Usage
		}{
			{message: api.Message{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: "Look things up."}}}},
			{message: api.Message{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Find alpha."}}}},
			{message: api.Message{Role: api.RoleAssistant, Parts: []api.Part{
				{Type: api.PartReasoning, Text: "I should look it up."},
				{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "toolu_lookup", Name: "lookup", Input: json.RawMessage(`{"key":"alpha"}`)}},
			}}, usage: &api.Usage{InputTokens: 20, OutputTokens: 5}},
			{message: api.Message{Role: api.RoleTool, Parts: []api.Part{
				{Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "toolu_lookup", Output: json.RawMessage(`{"value":"found"}`)}},
			}}},
			{message: api.Message{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: `{"status":`}}}, usage: &api.Usage{InputTokens: 30, OutputTokens: 7}},
		} {
			Expect(transcript.Append(model, turn.message, turn.usage)).To(Succeed())
		}
		Expect(transcript.Fail(model, "schema validation failed: data is not valid JSON")).To(Succeed())
		Expect(transcript.Close()).To(Succeed())

		parsed, info, err := BuildTranscriptFile(path)
		Expect(err).NotTo(HaveOccurred())

		Expect(info.RootSessionID).To(Equal(sessionID))
		Expect(parsed.CWD).To(Equal(cwd))
		Expect(parsed.InitialPrompt).To(Equal("Find alpha."))
		var usage api.Usage
		for _, turn := range parsed.Turns {
			usage = usage.Add(turn.Usage)
		}
		Expect(usage).To(Equal(api.Usage{InputTokens: 50, OutputTokens: 12}))
		Expect(partsOf(parsed.Messages)).To(ContainElements(
			HaveField("Text", "Find alpha."),
			HaveField("Text", "I should look it up."),
			And(HaveField("ToolName", "lookup"), HaveField("ToolCallID", "toolu_lookup"), HaveField("Input", MatchJSON(`{"key":"alpha"}`))),
			And(HaveField("ToolCallID", "toolu_lookup"), HaveField("Output", MatchJSON(`"{\"value\":\"found\"}"`))),
			HaveField("Text", `{"status":`),
		))
		Expect(partsOf(parsed.Messages)).To(ContainElement(And(
			HaveField("ToolName", "ApiError"), HaveField("Input", ContainSubstring("schema validation failed")),
		)))
	})

	It("requires the session id every line is keyed by", func() {
		_, err := claude.CreateSyntheticTranscript(filepath.Join(GinkgoT().TempDir(), "x.jsonl"), "", cwd)
		Expect(err).To(MatchError(ContainSubstring("session id is required")))
	})
})

func partsOf(messages []Message) []Part {
	var parts []Part
	for _, message := range messages {
		parts = append(parts, message.Parts...)
	}
	return parts
}

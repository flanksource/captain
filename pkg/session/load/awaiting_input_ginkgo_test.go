package load_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("awaiting input", func() {
	const summary = "The todo and its plan describe unrelated work."
	askQuestions := []api.TerminalQuestion{
		{Text: "Which work should I implement?", Context: "They are disjoint.", Options: []string{"The plan", "The todo", "Both"}},
		{Text: "What does the same layout mean?", Options: []string{"A rail", "Side-by-side table", "Reuse the rail"}},
	}
	askEnvelope := map[string]any{"summary": summary, "endStatus": "ask", "questions": []any{
		map[string]any{"text": "Which work should I implement?", "context": "They are disjoint.", "options": []any{"The plan", "The todo", "Both"}},
		map[string]any{"text": "What does the same layout mean?", "options": []any{"A rail", "Side-by-side table", "Reuse the rail"}},
	}}

	rawJSON := func(value any) json.RawMessage {
		GinkgoHelper()
		raw, err := json.Marshal(value)
		Expect(err).NotTo(HaveOccurred())
		return raw
	}
	userText := func(id, text string) session.Message {
		return session.Message{ID: id, Role: "user", Parts: []session.Part{{Type: session.PartText, Text: text}}}
	}
	assistantText := func(id, text string) session.Message {
		return session.Message{ID: id, Role: "assistant", Parts: []session.Part{{Type: session.PartText, Text: text}}}
	}
	toolCall := func(id, tool, state string, input any) session.Message {
		return session.Message{ID: id, Role: "assistant", Parts: []session.Part{{
			Type: session.PartTool, ToolName: tool, ToolCallID: id + "-call", State: state, Input: rawJSON(input),
		}}}
	}
	toolResult := func(id string) session.Message {
		return session.Message{ID: id, Role: "user", Parts: []session.Part{{Type: session.PartTool, State: session.ToolStateOutputAvailable}}}
	}
	// The tail a Claude run leaves after its envelope: the tool result the SDK
	// writes back, then the title it names the session with. Neither is a reply.
	envelopeTail := func() []session.Message {
		return []session.Message{
			userText("prompt", "Implement the todo"),
			toolCall("ask", "StructuredOutput", session.ToolStateInputAvailable, askEnvelope),
			toolResult("ask-result"),
			toolCall("title", session.TitleToolName, session.ToolStateInputAvailable, map[string]any{"aiTitle": "Fix layout"}),
		}
	}
	derive := func(aggregate *session.Session) *session.AwaitingInput {
		GinkgoHelper()
		Expect(load.DeriveAwaitingInput(aggregate)).To(Succeed())
		return aggregate.AwaitingInput
	}

	It("holds the questions of a run that ended by asking", func() {
		Expect(derive(&session.Session{Messages: envelopeTail()})).To(Equal(&session.AwaitingInput{
			Origin: session.AwaitingInputEnvelope, Summary: summary, Questions: askQuestions,
			MessageID: "ask", ToolCallID: "ask-call",
		}))
	})

	It("is answered by a later user message with text", func() {
		messages := append(envelopeTail(), userText("answer", "Implement the plan"))

		Expect(derive(&session.Session{Messages: messages})).To(BeNil())
	})

	It("is not answered by a notice the provider wrote after it", func() {
		notice := session.Message{ID: "interrupt", Role: "system", Parts: []session.Part{{Type: session.PartText, Text: "[Request interrupted by user]"}}}
		aggregate := &session.Session{Messages: append(envelopeTail(), notice)}

		Expect(derive(aggregate)).To(HaveField("MessageID", "ask"))
	})

	It("is answered once the agent carries on", func() {
		messages := append(envelopeTail(), assistantText("next", "Starting on the plan."))

		Expect(derive(&session.Session{Messages: messages})).To(BeNil())
	})

	It("is not pending once a later turn completed", func() {
		completed := map[string]any{"summary": "Implemented the plan.", "endStatus": "completed", "questions": []any{}}
		messages := append(envelopeTail(), userText("answer", "The plan"),
			toolCall("done", "StructuredOutput", session.ToolStateInputAvailable, completed))

		Expect(derive(&session.Session{Messages: messages})).To(BeNil())
	})

	It("reads the stored envelope when the transcript kept only its summary", func() {
		aggregate := &session.Session{
			Messages:         []session.Message{userText("prompt", "Implement the todo"), assistantText("final", summary)},
			StructuredOutput: askEnvelope,
		}

		Expect(derive(aggregate)).To(Equal(&session.AwaitingInput{
			Origin: session.AwaitingInputEnvelope, Summary: summary, Questions: askQuestions, MessageID: "final",
		}))
	})

	It("reads a prompt run's ask when no transcript was recorded", func() {
		result, failures := load.Load(context.Background(), load.PromptRun(load.PromptRunFacts{
			RunID: "run-1", State: "succeeded", PromptMarkdown: "Implement the todo", ResultJSON: askEnvelope,
		}))

		Expect(failures).To(BeEmpty())
		Expect(result.Session.AwaitingInput).To(Equal(&session.AwaitingInput{
			Origin: session.AwaitingInputEnvelope, Summary: summary, Questions: askQuestions, MessageID: "run-1-assistant",
		}))
	})

	It("holds an AskUserQuestion the run could not put to anyone", func() {
		input := map[string]any{"questions": []any{map[string]any{"question": "Which databases?", "multiSelect": true, "options": []any{
			map[string]any{"label": "PostgreSQL", "description": "Production"}, map[string]any{"label": "SQLite"},
		}}}}
		aggregate := &session.Session{Messages: []session.Message{
			toolCall("ask", "AskUserQuestion", session.ToolStateOutputError, input),
		}}

		Expect(derive(aggregate)).To(Equal(&session.AwaitingInput{
			Origin: session.AwaitingInputAskUserQuestion,
			Questions: []api.TerminalQuestion{{
				Text: "Which databases?", MultiSelect: true, Options: []string{"PostgreSQL", "SQLite"},
				OptionDescriptions: map[string]string{"PostgreSQL": "Production"},
			}},
			MessageID: "ask", ToolCallID: "ask-call",
		}))
	})

	It("does not put forward an AskUserQuestion the user already refused", func() {
		input := map[string]any{"questions": []any{map[string]any{"question": "Which database?"}}}
		aggregate := &session.Session{Messages: []session.Message{
			toolCall("ask", "AskUserQuestion", session.ToolStateOutputDenied, input),
		}}

		Expect(derive(aggregate)).To(BeNil())
	})

	It("does not fail on a stored envelope whose questions were already answered", func() {
		broken := map[string]any{"summary": summary, "endStatus": "ask", "questions": []any{map[string]any{"context": "no text"}}}
		aggregate := &session.Session{
			Messages: []session.Message{
				assistantText("final", summary), userText("answer", "The plan"), assistantText("next", "Starting."),
			},
			StructuredOutput: broken,
		}

		Expect(derive(aggregate)).To(BeNil())
	})

	It("leaves an AskUserQuestion awaiting approval to the approval flow", func() {
		input := map[string]any{"questions": []any{map[string]any{"question": "Which database?"}}}
		aggregate := &session.Session{
			Messages:  []session.Message{toolCall("ask", "AskUserQuestion", session.ToolStateApprovalRequested, input)},
			Approvals: session.ApprovalStats{Pending: 1},
		}

		Expect(derive(aggregate)).To(BeNil())
	})

	It("prefers the envelope a run ended with over an earlier AskUserQuestion", func() {
		input := map[string]any{"questions": []any{map[string]any{"question": "Earlier?"}}}
		messages := append([]session.Message{toolCall("earlier", "AskUserQuestion", session.ToolStateOutputError, input)},
			envelopeTail()[1:]...)

		Expect(derive(&session.Session{Messages: messages})).To(HaveField("Origin", session.AwaitingInputEnvelope))
	})

	It("reports an envelope whose questions cannot be asked", func() {
		broken := map[string]any{"summary": summary, "endStatus": "ask", "questions": []any{map[string]any{"context": "no text"}}}
		aggregate := &session.Session{Messages: []session.Message{
			toolCall("ask", "StructuredOutput", session.ToolStateInputAvailable, broken),
		}}

		Expect(load.DeriveAwaitingInput(aggregate)).To(MatchError(ContainSubstring("message ask: envelope questions")))
		Expect(aggregate.AwaitingInput).To(BeNil())
	})
})

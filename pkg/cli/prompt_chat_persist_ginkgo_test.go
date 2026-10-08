package cli

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// streamProviderStub answers a turn with the events it holds, or refuses to
// start one when it holds an error.
type streamProviderStub struct {
	interruptProviderStub
	events []ai.Event
	err    error
}

func (p *streamProviderStub) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	events := make(chan ai.Event, len(p.events))
	for _, event := range p.events {
		events <- event
	}
	close(events)
	return events, nil
}

// A chat that resumes a saved session has no batch binding, so each turn that
// reached the provider is the only record of what the continuation did — which
// is what Gavel reads to settle a run a person answered from Captain.
var _ = Describe("persisting a resumed chat turn", func() {
	const providerID = "0195c1de-4ab8-7000-8000-00000000c4a7"
	const answer = "Answers:\n1. Which work should I implement?\n→ The plan"
	var db *database.DB

	BeforeEach(func() {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_chat_turn_persist"})
		var err error
		db, err = database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)
	})

	resumedChat := func(binding *promptSessionBinding) *chatSession {
		rendered := PromptRenderResult{Name: "resume", Model: "claude-opus-5", Provider: api.Anthropic.Name, Mode: string(api.ModeAgent)}
		rendered.Input.SessionID = providerID
		rendered.Input.Prompt.User = answer
		return newChatSession("resume-run", rendered, 0, newRunStream(), binding)
	}
	reachedProvider := func(summary PromptRunSummary) chatTurn {
		summary.SessionID, summary.Model, summary.Provider, summary.Mode = providerID, "claude-opus-5", api.Anthropic.Name, string(api.ModeAgent)
		return chatTurn{Summary: summary, Delivered: true}
	}
	interrupted := func(turn chatTurn) chatTurn {
		turn.Interrupted = true
		return turn
	}
	persistedTurns := func() []database.PromptRun {
		GinkgoHelper()
		runs, err := db.ListPromptRuns(GinkgoT().Context(), database.PromptRunFilter{})
		Expect(err).NotTo(HaveOccurred())
		return runs
	}

	DescribeTable("records a turn that reached the provider, however it ended",
		func(turn chatTurn, turnErr error, stopped bool, wantErr string, want Fields) {
			chat := resumedChat(nil)
			if stopped {
				chat.stream.requestStop()
			}

			err := chat.endTurn(chat.rendered.Input, turn, turnErr)

			if wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			want["PromptMarkdown"] = Equal(answer)
			Expect(persistedTurns()).To(ConsistOf(MatchFields(IgnoreExtras, want)))
		},
		Entry("a turn that answered", reachedProvider(PromptRunSummary{Success: true}), nil, false, "",
			Fields{"State": Equal(database.PromptRunStateSucceeded), "Error": BeEmpty()}),
		Entry("a turn the provider failed", reachedProvider(PromptRunSummary{Error: "provider exited with status 1"}), errors.New("provider exited with status 1"), false,
			"provider exited with status 1", Fields{"State": Equal(database.PromptRunStateFailed), "Error": Equal("provider exited with status 1")}),
		Entry("a turn the person interrupted", interrupted(reachedProvider(PromptRunSummary{Success: true})), nil, false, "",
			Fields{"State": Equal(database.PromptRunStateCancelled), "Error": BeEmpty()}),
		Entry("a turn the person stopped", reachedProvider(PromptRunSummary{}), context.Canceled, true, "stopped",
			Fields{"State": Equal(database.PromptRunStateCancelled), "Error": Equal("stopped")}),
	)

	It("records nothing for a follow-up turn that never reached the provider", func() {
		chat := resumedChat(nil)
		chat.state.SessionID, chat.state.Turn = providerID, 2

		err := chat.endTurn(chat.rendered.Input, chatTurn{}, errors.New("spawn claude: executable not found"))

		Expect(err).To(MatchError("spawn claude: executable not found"))
		Expect(persistedTurns()).To(BeEmpty())
	})

	DescribeTable("knows whether a turn reached the provider",
		func(provider *streamProviderStub, delivered bool) {
			chat := resumedChat(nil)
			chat.provider, chat.streamer = provider, provider
			chat.acc = newPromptEventAccumulator(chat.stream.publish, discardTaskSink{}, chat.rendered.Model, chat.rendered.Mode)

			turn, _ := chat.runTurn(GinkgoT().Context(), chat.rendered.Input)

			Expect(turn.Delivered).To(Equal(delivered))
		},
		Entry("a provider that answered and then failed",
			&streamProviderStub{events: []ai.Event{{Kind: ai.EventSystem, SessionID: providerID}, {Kind: ai.EventError, Error: "overloaded"}}}, true),
		Entry("a provider that could not start the turn", &streamProviderStub{err: errors.New("spawn claude: executable not found")}, false),
	)

	It("keeps a batch chat's stopped turn cancelled when the chat then fails", func() {
		rendered := PromptRenderResult{Name: "compare", Provider: api.Anthropic.Name, Mode: string(api.ModeAgent), Model: "claude-opus-5"}
		rendered.Input.Prompt.User = answer
		batch, err := createPromptBatchSessions(GinkgoT().Context(), rendered, resolveAll(
			api.Model{Name: "claude-opus-5", Mode: api.ModeAgent}, api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAgent},
		))
		Expect(err).NotTo(HaveOccurred())
		chat := resumedChat(&promptSessionBinding{BatchID: batch.ID, SessionID: batch.Runs[0].SessionID})
		chat.stream.requestStop()

		err = chat.endTurn(chat.rendered.Input, reachedProvider(PromptRunSummary{}), context.Canceled)
		Expect(err).To(MatchError("stopped"))
		chat.recordFailedRun(err)

		Expect(persistedTurns()).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"State": Equal(database.PromptRunStateCancelled), "Error": Equal("stopped"),
		})))
	})
})

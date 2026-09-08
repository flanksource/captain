package load_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("PromptRun", func() {
	queued := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	finished := queued.Add(time.Minute)

	run := load.PromptRunFacts{
		RunID: "9b1d0a5e-0000-4000-8000-0000000000aa", State: "failed",
		PromptMarkdown: "review the diff", ResultText: `{"verdict":"pass"}`,
		RenderedSpec: map[string]any{
			"model":        "claude-opus-4",
			"outputSchema": map[string]any{"type": "object"},
		},
		Error: "provider hung up", Provider: "anthropic", Model: "claude-opus-4",
		Mode: "cli", Effort: "high", QueuedAt: queued, FinishedAt: &finished,
	}

	It("applies every prompt-run fact even when a transcript already won the messages", func() {
		// The defect: a transcript made loadSessionDetail skip sessionFromPromptRun
		// entirely, dropping the run's prompt, structured output and error event.
		result, failures := load.Load(context.Background(),
			load.Transcript(&session.Session{Messages: []session.Message{{ID: "transcript-m1"}}}),
			load.PromptRun(run))

		Expect(failures).To(BeEmpty())
		Expect(messageIDs(result.Session)).To(Equal([]string{"transcript-m1"}))
		Expect(string(result.Session.Prompt)).To(ContainSubstring(`"outputSchema"`))
		Expect(result.Session.StructuredOutput).To(Equal(map[string]any{"verdict": "pass"}))
		Expect(result.Session.Events).To(HaveLen(1))
		Expect(result.Session.Events[0].Data).To(HaveKeyWithValue("message", "provider hung up"))
		Expect(result.Provenance.Of(load.FacetPrompt)).To(Equal(load.SourcePromptRun))
		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourceTranscript))
	})

	It("synthesises the prompt and result messages when nothing else supplied any", func() {
		result, _ := load.Load(context.Background(), load.PromptRun(run))

		Expect(result.Session.Messages).To(HaveLen(2))
		Expect(result.Session.Messages[0].Role).To(Equal("user"))
		Expect(result.Session.Messages[0].Parts[0].Text).To(Equal("review the diff"))
		Expect(result.Session.Messages[1].Role).To(Equal("assistant"))
		Expect(result.Session.Messages[1].Parts[0].Text).To(Equal(`{"verdict":"pass"}`))
		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourcePromptRun))
	})

	It("renders the result object as text when the run stored no result text", func() {
		jsonOnly := run
		jsonOnly.ResultText = ""
		jsonOnly.ResultJSON = map[string]any{"verdict": "fail"}

		result, _ := load.Load(context.Background(), load.PromptRun(jsonOnly))

		Expect(result.Session.Messages[1].Parts[0].Text).To(Equal(`{"verdict":"fail"}`))
		Expect(result.Session.StructuredOutput).To(Equal(map[string]any{"verdict": "fail"}))
	})

	It("decodes structured output only for a run whose spec declares an output schema", func() {
		unschematised := run
		unschematised.RenderedSpec = map[string]any{"model": "claude-opus-4"}

		result, _ := load.Load(context.Background(), load.PromptRun(unschematised))

		Expect(result.Session.StructuredOutput).To(BeNil())
	})

	It("prefers the persisted structured output over re-reading the result text", func() {
		// ResultJSON is what the run actually returned; ResultText is the raw
		// transport copy. When both are present the stored object wins, and the
		// text still stands as the assistant's message.
		both := run
		both.ResultJSON = map[string]any{"source": "stored"}
		both.ResultText = `{"source":"text"}`

		result, _ := load.Load(context.Background(), load.PromptRun(both))

		Expect(result.Session.StructuredOutput).To(Equal(map[string]any{"source": "stored"}))
		Expect(result.Session.Messages).To(HaveLen(2))
		Expect(result.Session.Messages[1].Parts[0].Text).To(Equal(`{"source":"text"}`))
	})

	It("fills runtime and timing facts the overview left absent, without overwriting it", func() {
		overviewStart := queued.Add(-time.Hour)
		result, _ := load.Load(context.Background(),
			load.Overview(load.OverviewFacts{Provider: "overview-provider", StartedAt: &overviewStart}),
			load.PromptRun(run))

		Expect(result.Session.Provider).To(Equal("overview-provider"))
		Expect(result.Session.Model).To(Equal("claude-opus-4"))
		Expect(result.Session.ReasoningEffort).To(Equal("high"))
		Expect(result.Session.StartedAt).To(Equal(&overviewStart))
		Expect(result.Session.EndedAt).To(Equal(&finished))
	})

	It("falls back to the queue time when the run never started", func() {
		result, _ := load.Load(context.Background(), load.PromptRun(run))

		Expect(result.Session.StartedAt).To(Equal(&queued))
	})

	It("supplies the initial prompt when the overview has none", func() {
		result, _ := load.Load(context.Background(), load.PromptRun(run))

		Expect(result.Session.InitialPrompt).To(Equal("review the diff"))
	})

	It("treats an absent run as having nothing to say", func() {
		result, failures := load.Load(context.Background(), load.PromptRun(load.PromptRunFacts{}))

		Expect(failures).To(BeEmpty())
		Expect(result.Provenance.Of(load.FacetPrompt)).To(Equal(load.SourceNone))
		Expect(result.Session.Messages).To(BeEmpty())
	})
})

package load_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("Overview", func() {
	started := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	ended := started.Add(90 * time.Second)

	facts := load.OverviewFacts{
		ID: "0f9c1c1e-0000-4000-8000-000000000001", ProviderSessionID: "prov-1", Revision: 7,
		Source: "claude", Project: "captain", CWD: "/src/captain", Slug: "load-slice",
		Title: "Compose session detail", InitialPrompt: "compose the detail", Version: "1.2.3",
		Provider: "anthropic", ModelMode: api.RuntimeMode("cli"), ExecutionMode: api.RuntimeMode("prompt"),
		Model: "claude-opus-4", ReasoningEffort: "high", HistoryFile: "/hist/1.jsonl",
		LifecycleStatus: "running", ActivityState: "waiting_approval", HealthState: "ok",
		StateReason: "tool approval", StartedAt: &started, EndedAt: &ended,
	}

	It("supplies the stored row's identity, runtime and lifecycle facts", func() {
		result, failures := load.Load(context.Background(), load.Overview(facts))

		Expect(failures).To(BeEmpty())
		Expect(*result.Session).To(Equal(session.Session{
			ID: facts.ID, ProviderSessionID: facts.ProviderSessionID, Revision: facts.Revision,
			LifecycleStatus: facts.LifecycleStatus, ActivityState: facts.ActivityState,
			HealthState: facts.HealthState, StateReason: facts.StateReason, Source: facts.Source,
			Project: facts.Project, CWD: facts.CWD, Slug: facts.Slug, Title: facts.Title,
			InitialPrompt: facts.InitialPrompt, Version: facts.Version, Provider: facts.Provider,
			ModelMode: facts.ModelMode, ExecutionMode: facts.ExecutionMode, Model: facts.Model,
			ReasoningEffort: facts.ReasoningEffort, HistoryFile: facts.HistoryFile,
			StartedAt: &started, EndedAt: &ended,
		}))
	})

	It("claims no contested facet, so it never blocks a transcript or a prompt run", func() {
		result, _ := load.Load(context.Background(), load.Overview(facts))

		for facet, source := range result.Provenance.Facets() {
			Expect(source).To(Equal(string(load.SourceNone)), "facet %s", facet)
		}
	})

	It("reports an all-zero row as having nothing to say", func() {
		result, failures := load.Load(context.Background(), load.Overview(load.OverviewFacts{}))

		Expect(failures).To(BeEmpty())
		Expect(*result.Session).To(Equal(session.Session{}))
	})
})

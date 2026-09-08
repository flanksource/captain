package load_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("Transcript and Database", func() {
	parsed := func(marker string) *session.Session {
		return &session.Session{
			Provider: marker, Model: marker + "-model",
			Messages: []session.Message{{ID: marker + "-m1"}},
			Events:   []session.Event{{Type: marker + "-event"}},
			Turns:    []session.Turn{{ID: marker + "-t1"}},
			Usage:    api.Usage{InputTokens: 11, OutputTokens: 3},
			Cost:     api.Cost{TotalTokens: 14, ProviderCostUSD: 0.42},
			Context:  &session.Context{UsedTokens: 900, WindowTokens: 2000, FreePercent: 55},
			Files:    session.ChangedFiles{Written: []string{marker + ".go"}},
		}
	}

	It("merges an already-parsed transcript and records it as the source", func() {
		result, failures := load.Load(context.Background(), load.Transcript(parsed("transcript")))

		Expect(failures).To(BeEmpty())
		Expect(messageIDs(result.Session)).To(Equal([]string{"transcript-m1"}))
		Expect(result.Session.Turns).To(HaveLen(1))
		Expect(result.Session.Usage.InputTokens).To(Equal(11))
		Expect(result.Session.Cost.ProviderCostUSD).To(Equal(0.42))
		Expect(result.Provenance.Facets()).To(SatisfyAll(
			HaveKeyWithValue("messages", "transcript"),
			HaveKeyWithValue("turns", "transcript"),
			HaveKeyWithValue("usage", "transcript"),
		))
	})

	It("gives the transcript the contested facets and leaves the database aggregate out", func() {
		result, failures := load.Load(context.Background(),
			load.Database(parsed("database")), load.Transcript(parsed("transcript")))

		Expect(failures).To(BeEmpty())
		Expect(messageIDs(result.Session)).To(Equal([]string{"transcript-m1"}))
		Expect(result.Session.Turns[0].ID).To(Equal("transcript-t1"))
		Expect(result.Provenance.Of(load.FacetUsage)).To(Equal(load.SourceTranscript))
	})

	It("uses the database aggregate when no transcript was parsed", func() {
		result, _ := load.Load(context.Background(), load.Database(parsed("database")))

		Expect(messageIDs(result.Session)).To(Equal([]string{"database-m1"}))
		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourceDatabase))
	})

	It("leaves identity the overview already supplied alone", func() {
		result, _ := load.Load(context.Background(),
			load.Overview(load.OverviewFacts{ID: "captain-id", Provider: "overview-provider"}),
			load.Transcript(parsed("transcript")))

		Expect(result.Session.ID).To(Equal("captain-id"))
		Expect(result.Session.Provider).To(Equal("overview-provider"))
		Expect(result.Session.Model).To(Equal("transcript-model"), "the overview left the model empty")
	})

	It("never contests approvals, which only the requests source may supply", func() {
		aggregate := parsed("database")
		aggregate.Approvals = session.ApprovalStats{Approved: 200}
		aggregate.Requests = []session.Request{{ID: "stale"}}

		result, _ := load.Load(context.Background(), load.Database(aggregate))

		// The stored transcript copy counts every operational tool use as an
		// approval and reads as "200 approved"; captain_turn_requests is the truth.
		Expect(result.Session.Approvals).To(Equal(session.ApprovalStats{}))
		Expect(result.Session.Requests).To(BeEmpty())
		Expect(result.Provenance.Of(load.FacetApprovals)).To(Equal(load.SourceNone))
	})

	It("treats a nil aggregate as having nothing to say", func() {
		result, failures := load.Load(context.Background(), load.Transcript(nil), load.Database(nil))

		Expect(failures).To(BeEmpty())
		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourceNone))
	})
})

package query_test

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type transcriptStoreStub struct {
	session *database.Session
	err     error
	called  bool
}

func (s *transcriptStoreStub) GetTranscriptSessionByIdentity(context.Context, string) (*database.Session, error) {
	s.called = true
	return s.session, s.err
}

var _ = Describe("TranscriptCandidate", func() {
	It("uses the transcript-bearing sibling for an admission root", func(ctx SpecContext) {
		providerID := "provider-session"
		store := &transcriptStoreStub{session: &database.Session{
			ID: uuid.New(), ProviderSessionID: providerID, Source: "codex", Path: "/history/rollout.jsonl",
		}}

		candidate, ok, err := query.TranscriptCandidate(ctx, store, database.SessionOverview{
			ID: uuid.New(), Source: "gavel", ProviderSessionID: &providerID,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(candidate).To(Equal(query.Candidate{
			ID: providerID, Source: "codex", Path: "/history/rollout.jsonl",
		}))
	})

	It("does not borrow a sibling for a provider row without a path", func(ctx SpecContext) {
		providerID := "provider-session"
		store := &transcriptStoreStub{}

		_, ok, err := query.TranscriptCandidate(ctx, store, database.SessionOverview{
			ID: uuid.New(), Source: "codex", ProviderSessionID: &providerID,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(store.called).To(BeFalse())
	})

	It("returns transcript lookup failures", func(ctx SpecContext) {
		providerID := "provider-session"
		lookupErr := errors.New("lookup failed")
		store := &transcriptStoreStub{err: lookupErr}

		_, _, err := query.TranscriptCandidate(ctx, store, database.SessionOverview{
			ID: uuid.New(), Source: "gavel", ProviderSessionID: &providerID,
		})

		Expect(err).To(MatchError(ContainSubstring("lookup failed")))
	})
})

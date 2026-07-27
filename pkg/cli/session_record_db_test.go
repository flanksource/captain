package cli

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type sessionOverviewStoreStub struct {
	getErr     error
	listCalled bool
}

func (s *sessionOverviewStoreStub) ListSessionOverviewsByIdentity(context.Context, string) ([]database.SessionOverview, error) {
	return nil, s.getErr
}

func (s *sessionOverviewStoreStub) ListSessionOverviews(context.Context, database.SessionOverviewFilter) ([]database.SessionOverview, error) {
	s.listCalled = true
	return nil, nil
}

func (s *sessionOverviewStoreStub) ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error) {
	return nil, nil
}

var _ = Describe("session route identity", func() {
	It("never scans all overviews when an identity is not found", func(ctx SpecContext) {
		store := &sessionOverviewStoreStub{getErr: database.ErrSessionNotFound}

		_, err := resolveOverviewsByIdentity(ctx, store, "codex-22ea4efed82ed44e")

		Expect(errors.Is(err, database.ErrSessionNotFound)).To(BeTrue())
		Expect(store.listCalled).To(BeFalse())
	})

	It("never scans all overviews when an identity is ambiguous", func(ctx SpecContext) {
		store := &sessionOverviewStoreStub{getErr: &database.SessionConflictError{Identity: "ad4c854e"}}

		_, err := resolveOverviewsByIdentity(ctx, store, "ad4c854e")

		Expect(errors.Is(err, database.ErrSessionConflict)).To(BeTrue())
		Expect(store.listCalled).To(BeFalse())
	})

	It("uses the Captain UUID as the list and route key", func() {
		captainID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		providerID := "019f7c25-9adf-7901-add9-8c46693472fb"
		path := "/home/acme/.codex/sessions/rollout.jsonl"

		record := recordFromOverview(database.SessionOverview{
			ID: captainID, ProviderSessionID: &providerID, Source: "codex", Path: &path,
		})

		Expect(record.Key).To(Equal(captainID.String()))
		Expect(record.ID).To(Equal(providerID))
	})
})

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
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

func TestResolveOverviewsByAnyIDDoesNotScanOnResolutionFailure(t *testing.T) {
	store := &sessionOverviewStoreStub{getErr: &database.SessionConflictError{Identity: "ad4c854e"}}
	_, err := resolveOverviewsByAnyID(t.Context(), store, "ad4c854e")
	if !errors.Is(err, database.ErrSessionConflict) {
		t.Fatalf("error = %v, want session conflict", err)
	}
	if store.listCalled {
		t.Fatal("conflict triggered full overview fallback scan")
	}
}

package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session/transcript"
)

// Candidate is the transcript identity returned by session queries.
type Candidate = transcript.Candidate

// TranscriptStore resolves the row that owns a provider transcript.
type TranscriptStore interface {
	GetTranscriptSessionByIdentity(context.Context, string) (*database.Session, error)
}

// TranscriptCandidate selects the transcript that supplies facts for an
// overview. Admission roots may borrow their provider row's transcript, while
// provider rows never borrow another provider row's log.
func TranscriptCandidate(
	ctx context.Context,
	db TranscriptStore,
	overview database.SessionOverview,
) (Candidate, bool, error) {
	if path := overviewTranscriptPath(overview); path != "" {
		return CandidateFromOverview(overview), true, nil
	}
	if overview.Source == "claude" || overview.Source == "codex" {
		return Candidate{}, false, nil
	}
	providerID := stringValue(overview.ProviderSessionID)
	if providerID == "" {
		return Candidate{}, false, nil
	}
	bearing, err := db.GetTranscriptSessionByIdentity(ctx, providerID)
	if errors.Is(err, database.ErrSessionNotFound) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, fmt.Errorf("resolve transcript for provider session %s: %w", providerID, err)
	}
	if bearing == nil || bearing.Path == "" {
		return Candidate{}, false, nil
	}
	return Candidate{ID: bearing.ProviderSessionID, Source: bearing.Source, Path: bearing.Path}, true, nil
}

// CandidateFromOverview projects a transcript-bearing overview into a parser
// candidate.
func CandidateFromOverview(overview database.SessionOverview) Candidate {
	return Candidate{
		ID:     firstString(stringValue(overview.ProviderSessionID), overview.ID.String()),
		Source: overview.Source,
		Path:   overviewTranscriptPath(overview),
	}
}

func overviewTranscriptPath(overview database.SessionOverview) string {
	return firstString(stringValue(overview.HistoryFile), stringValue(overview.Path))
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

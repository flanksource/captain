package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// facetsFingerprintLength is how many hex digits of the SHA-256 FollowState
// carries: enough that two facet sets never collide in practice, short enough
// to stay a cheap equality key on every state frame.
const facetsFingerprintLength = 16

// sessionFacets is what one thread session contributes to the aggregate beyond
// its messages and lifecycle: native plans, the plan/todos/files ingest projects
// into metadata, and approval requests. Only fields whose change alters the
// composed aggregate are kept, so a wake that rewrites nothing emits nothing.
type sessionFacets struct {
	ID       uuid.UUID      `json:"id"`
	Metadata metadataFacets `json:"metadata"`
	Plans    []planFacet    `json:"plans"`
	Requests []requestFacet `json:"requests"`
}

type metadataFacets struct {
	Plan  json.RawMessage `json:"plan,omitempty"`
	Todos json.RawMessage `json:"todos,omitempty"`
	Files json.RawMessage `json:"files,omitempty"`
}

type planFacet struct {
	ID                 uuid.UUID                  `json:"id"`
	UpdatedAt          time.Time                  `json:"updatedAt"`
	ApprovalState      database.PlanApprovalState `json:"approvalState"`
	ApprovedRevisionID *uuid.UUID                 `json:"approvedRevisionId,omitempty"`
	LatestRevision     int                        `json:"latestRevision"`
	LatestContentHash  string                     `json:"latestContentHash"`
}

type requestFacet struct {
	ID      uuid.UUID                 `json:"id"`
	State   database.TurnRequestState `json:"state"`
	Version int64                     `json:"version"`
}

// facets fingerprints the non-message facets of every session the thread
// resolves to (each folded session and the transcript it executed in). It
// reads only store rows the aggregate composes from -- no transcript parse.
func (f *follower) facets(ctx context.Context, thread followThread) (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, overview := range thread.overviews() {
		facet, err := f.sessionFacets(ctx, overview)
		if err != nil {
			return "", fmt.Errorf("follow Captain session %s facets of %s: %w", f.identity, overview.ID, err)
		}
		if err := encoder.Encode(facet); err != nil {
			return "", fmt.Errorf("follow Captain session %s: encode facets of %s: %w", f.identity, overview.ID, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))[:facetsFingerprintLength], nil
}

func (f *follower) sessionFacets(ctx context.Context, overview database.SessionOverview) (sessionFacets, error) {
	facet := sessionFacets{ID: overview.ID}
	if len(overview.Metadata) > 0 {
		if err := json.Unmarshal(overview.Metadata, &facet.Metadata); err != nil {
			return sessionFacets{}, fmt.Errorf("decode metadata: %w", err)
		}
	}
	plans, err := f.db.ListPlans(ctx, database.PlanFilter{SourceSessionID: &overview.ID})
	if err != nil {
		return sessionFacets{}, err
	}
	for _, plan := range plans {
		entry := planFacet{
			ID: plan.ID, UpdatedAt: plan.UpdatedAt, ApprovalState: plan.ApprovalState,
			ApprovedRevisionID: plan.ApprovedRevisionID,
		}
		if plan.LatestRevision != nil {
			entry.LatestRevision, entry.LatestContentHash = plan.LatestRevision.Revision, plan.LatestRevision.ContentHash
		}
		facet.Plans = append(facet.Plans, entry)
	}
	requests, err := f.db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: overview.ID})
	if err != nil {
		return sessionFacets{}, err
	}
	for _, request := range requests {
		facet.Requests = append(facet.Requests, requestFacet{ID: request.ID, State: request.State, Version: request.Version})
	}
	return facet, nil
}

// overviews is each folded session followed by the transcript it executed in,
// once each, in resolution order so the fingerprint is stable across wakes.
func (t followThread) overviews() []database.SessionOverview {
	seen := map[uuid.UUID]bool{}
	var out []database.SessionOverview
	add := func(overview database.SessionOverview) {
		if !seen[overview.ID] {
			seen[overview.ID] = true
			out = append(out, overview)
		}
	}
	for _, item := range t.folded {
		add(item.Session)
		if item.Transcript != nil {
			add(*item.Transcript)
		}
	}
	return out
}

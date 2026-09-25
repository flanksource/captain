package query

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// TranscriptChildStore lists the transcript-owned child rows of sessions.
type TranscriptChildStore interface {
	ListTranscriptChildOverviews(context.Context, []uuid.UUID) ([]database.SessionOverview, error)
}

// FoldStore is what FoldTranscripts needs to complete a parent/transcript pair.
type FoldStore interface {
	OverviewStore
	TranscriptChildStore
}

// FoldedOverview is one displayed session: the row that owns its identity and,
// when the provider's log landed on a separate transcript row, that row.
type FoldedOverview struct {
	Session    database.SessionOverview
	Transcript *database.SessionOverview
}

// ResolveFolded resolves an identity to the sessions it names, each paired with
// the transcript row it executed in, so one conversation is one item however
// the identity reached it. Every session read and follow resolves through here.
func ResolveFolded(ctx context.Context, store FoldStore, identity string) ([]FoldedOverview, error) {
	overviews, err := Resolve(ctx, store, identity)
	if err != nil {
		return nil, err
	}
	return FoldTranscripts(ctx, store, overviews)
}

// FoldTranscripts presents a session and its `transcript` child as one session.
//
// A launcher that books a run before the provider session exists (a Gavel run,
// a Captain batch run, an aichat thread) owns the identity row, and the monitor
// ingests the provider's log into a child row with the same provider session id.
// Both rows answer a lookup by that id, which rendered one conversation twice.
// Whichever of the pair a lookup returned, the result carries the parent with its
// transcript child attached, in the position the pair first appeared.
func FoldTranscripts(ctx context.Context, store FoldStore, overviews []database.SessionOverview) ([]FoldedOverview, error) {
	folded := make([]FoldedOverview, 0, len(overviews))
	index := make(map[uuid.UUID]int, len(overviews))
	resolved := make(map[uuid.UUID]database.SessionOverview, len(overviews))
	for _, row := range overviews {
		resolved[row.ID] = row
	}
	slot := func(row database.SessionOverview) int {
		if i, ok := index[row.ID]; ok {
			return i
		}
		index[row.ID] = len(folded)
		folded = append(folded, FoldedOverview{Session: row})
		return len(folded) - 1
	}
	for _, row := range overviews {
		if !isTranscriptChild(row) {
			slot(row)
			continue
		}
		parent, ok := resolved[*row.ParentSessionID]
		if !ok {
			loaded, err := loadTranscriptParent(ctx, store, row)
			if err != nil {
				return nil, err
			}
			parent = loaded
		}
		if err := attachTranscript(&folded[slot(parent)], row); err != nil {
			return nil, err
		}
	}
	if err := attachTranscriptChildren(ctx, store, folded); err != nil {
		return nil, err
	}
	reparentOntoFolded(folded)
	return folded, nil
}

// reparentOntoFolded points a session whose parent is a folded-away transcript
// row (a sub-agent the monitor parented to the provider's log) at the session
// that row folded into, so it stays attached to a listed session.
func reparentOntoFolded(folded []FoldedOverview) {
	owners := make(map[uuid.UUID]uuid.UUID, len(folded))
	for _, item := range folded {
		if item.Transcript != nil {
			owners[item.Transcript.ID] = item.Session.ID
		}
	}
	for i := range folded {
		parent := folded[i].Session.ParentSessionID
		if parent == nil {
			continue
		}
		if owner, ok := owners[*parent]; ok {
			folded[i].Session.ParentSessionID = &owner
		}
	}
}

func isTranscriptChild(row database.SessionOverview) bool {
	return row.ParentRelation == database.SessionParentRelationTranscript && row.ParentSessionID != nil
}

func loadTranscriptParent(ctx context.Context, store OverviewStore, child database.SessionOverview) (database.SessionOverview, error) {
	parentID := *child.ParentSessionID
	rows, err := store.ListSessionOverviewsByIdentity(ctx, parentID.String())
	if err != nil {
		return database.SessionOverview{}, fmt.Errorf("load parent %s of transcript session %s: %w", parentID, child.ID, err)
	}
	for _, row := range rows {
		if row.ID == parentID {
			return row, nil
		}
	}
	return database.SessionOverview{}, fmt.Errorf("load parent %s of transcript session %s: %w", parentID, child.ID, database.ErrSessionNotFound)
}

// attachTranscriptChildren looks up the transcript children of every folded
// session, including one a lookup already paired, so a second child is refused
// wherever the first was found.
func attachTranscriptChildren(ctx context.Context, store TranscriptChildStore, folded []FoldedOverview) error {
	parentIDs := make([]uuid.UUID, 0, len(folded))
	for i := range folded {
		parentIDs = append(parentIDs, folded[i].Session.ID)
	}
	if len(parentIDs) == 0 {
		return nil
	}
	children, err := store.ListTranscriptChildOverviews(ctx, parentIDs)
	if err != nil {
		return err
	}
	byParent := make(map[uuid.UUID]int, len(folded))
	for i := range folded {
		byParent[folded[i].Session.ID] = i
	}
	for _, child := range children {
		i, ok := byParent[*child.ParentSessionID]
		if !ok {
			return fmt.Errorf("%w: transcript session %s names parent %s outside the requested sessions",
				database.ErrInvalidSession, child.ID, *child.ParentSessionID)
		}
		if err := attachTranscript(&folded[i], child); err != nil {
			return err
		}
	}
	return nil
}

func attachTranscript(target *FoldedOverview, child database.SessionOverview) error {
	if target.Transcript != nil && target.Transcript.ID != child.ID {
		return fmt.Errorf("%w: session %s has multiple transcript children (%s, %s)",
			database.ErrSessionConflict, target.Session.ID, target.Transcript.ID, child.ID)
	}
	transcript := child
	target.Transcript = &transcript
	return nil
}

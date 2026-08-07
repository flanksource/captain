package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// noticeRole is the transcript role lifecycle notices are written under, so a
// reader can tell "the harness committed this" from anything the model said.
const noticeRole = "system"

// PutSessionNotices records what a run's lifecycle hooks did as transcript
// messages, so a commit cut between two turns is readable from the transcript
// rather than only from whatever scrolled past in the terminal.
//
// Sequences are negative, counting down from the lowest already present. The
// positive space belongs to the transcript ingester, which keys every message on
// its line number in the provider's JSONL (see monitor.transcriptSequence) and
// converges on (session_id, sequence): a notice written at MAX(sequence)+1 —
// what PutChatMessage would give it — is the line number the file is about to
// grow into, so the next ingest pass would overwrite the notice with that line's
// real content and nobody would see either loss. No JSONL line is ever numbered
// below 1, so the negative half of the space is ours alone.
//
// Ordering is therefore carried by OccurredAt, not by sequence; the transcript
// reads sort on it first.
func (db *DB) PutSessionNotices(ctx context.Context, sessionID uuid.UUID, notices []api.Notice) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	if sessionID == uuid.Nil {
		return fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}
	records := make([]messageRecord, 0, len(notices))
	for i, notice := range notices {
		text := strings.TrimSpace(notice.Text)
		if text == "" {
			continue
		}
		parts, err := json.Marshal([]session.Part{{Type: session.PartText, Text: text}})
		if err != nil {
			return fmt.Errorf("encode notice %d: %w", i, err)
		}
		at := notice.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		records = append(records, messageRecord{
			ID: uuid.New(), SessionID: sessionID, Role: noticeRole,
			Parts: parts, OccurredAt: &at,
			// Stable across replays of the same run, so re-flushing the same
			// workspace (a resumed run, a retried write) updates rather than
			// duplicates.
			ProviderMessageID: noticeMessageID(sessionID, notice, i),
		})
	}
	if len(records) == 0 {
		return nil
	}

	var floor int64
	if err := db.gorm.WithContext(ctx).Model(&messageRecord{}).Where("session_id = ?", sessionID).
		Select("LEAST(COALESCE(MIN(sequence), 0), 0)").Scan(&floor).Error; err != nil {
		return fmt.Errorf("select Captain notice sequence floor: %w", err)
	}
	for i := range records {
		records[i].Sequence = floor - int64(i+1)
	}

	// A replayed flush must not stack duplicates, so the provider message ID is
	// the conflict target rather than the sequence — the sequence is fresh on
	// every attempt and would never collide with itself.
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "session_id"}, {Name: "provider_message_id"}},
		// captain_messages_provider_message_id_key is partial; an inference
		// clause only matches a partial index when it repeats the predicate.
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "provider_message_id IS NOT NULL"},
		}},
		DoUpdates: clause.AssignmentColumns([]string{"role", "parts", "occurred_at"}),
	}).CreateInBatches(&records, ingestBatchSize).Error
	if err != nil {
		return fmt.Errorf("write %d Captain session notice(s): %w", len(records), err)
	}
	return nil
}

// noticeMessageID identifies a notice across replays of the same run. The phase
// and text alone are not unique — a run commits at PhaseTurn repeatedly with the
// same wording — so the index within the flush disambiguates.
func noticeMessageID(sessionID uuid.UUID, notice api.Notice, index int) *string {
	id := fmt.Sprintf("notice-%s-%d-%d", sessionID, notice.At.UTC().UnixNano(), index)
	return &id
}

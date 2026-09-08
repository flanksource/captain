package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// registeredParserVersion marks a source row that names a transcript nobody has
// parsed yet. It is deliberately not the ingestor's parser version: needsIngest
// compares the two, so a registration always reads as work still to do rather
// than as an up-to-date ingest that can be skipped.
const registeredParserVersion = 1

func (db *DB) GetTranscriptSession(ctx context.Context, parentID uuid.UUID) (*Session, error) {
	if parentID == uuid.Nil {
		return nil, fmt.Errorf("%w: parent session ID is required", ErrInvalidSession)
	}
	var records []sessionRecord
	err := db.gorm.WithContext(ctx).
		Where("parent_session_id = ? AND parent_relation = ?", parentID, SessionParentRelationTranscript).
		Order("created_at, id").Limit(2).Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("find provider transcript session: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: transcript child of %s", ErrSessionNotFound, parentID)
	}
	if len(records) > 1 {
		return nil, fmt.Errorf("%w: session %s has multiple transcript children", ErrSessionConflict, parentID)
	}
	result := sessionFromRecord(records[0])
	return &result, nil
}

func (db *DB) RegisterSessionSource(ctx context.Context, sessionID uuid.UUID, input IngestSourceInput) error {
	if sessionID == uuid.Nil || strings.TrimSpace(input.SourceKind) == "" || strings.TrimSpace(input.Path) == "" || input.ParserVersion <= 0 {
		return fmt.Errorf("%w: session, source kind, path, and parser version are required", ErrInvalidIngest)
	}
	if _, err := db.GetSession(ctx, sessionID); err != nil {
		return err
	}
	return db.upsertSessionSource(ctx, sessionID, input)
}

// RegisterTranscriptSource binds a session to the transcript file its provider
// identity resolves to. It is the id-keyed counterpart to filesystem discovery:
// an agent that ran in a fresh git worktree writes into a project directory no
// working-directory scan has ever seen, and this records the file the moment the
// provider session id is known rather than when a scan happens to find it.
//
// The ingestor owns the bookkeeping columns, so an existing row for the path is
// left exactly as written: re-registering an already-ingested transcript must
// not rewind its byte offset and force a full replay.
func (db *DB) RegisterTranscriptSource(ctx context.Context, sessionID uuid.UUID, sourceKind, path, sourceIdentity string) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	sourceKind, path = strings.TrimSpace(sourceKind), strings.TrimSpace(path)
	if sessionID == uuid.Nil || sourceKind == "" || path == "" {
		return fmt.Errorf("%w: session, source kind, and path are required", ErrInvalidIngest)
	}
	if _, err := db.GetSession(ctx, sessionID); err != nil {
		return err
	}
	record := sessionSourceRecord{
		ID: uuid.New(), SessionID: sessionID, SourceKind: sourceKind, Path: path,
		SourceIdentity: nullableTrimmed(sourceIdentity), ParserVersion: registeredParserVersion,
	}
	if err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_kind"}, {Name: "path"}}, DoNothing: true,
	}).Create(&record).Error; err != nil {
		return fmt.Errorf("register Captain transcript source %s: %w", path, err)
	}
	return nil
}

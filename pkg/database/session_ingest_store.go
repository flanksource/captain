package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidIngest = errors.New("invalid Captain transcript ingest")

type TurnStatus string

const (
	TurnStatusOpen        TurnStatus = "open"
	TurnStatusEnded       TurnStatus = "ended"
	TurnStatusError       TurnStatus = "error"
	TurnStatusInterrupted TurnStatus = "interrupted"
)

// SessionSourceState is the ingest bookkeeping for one transcript file, used to
// decide whether a file changed since it was last parsed and where to resume.
type SessionSourceState struct {
	SessionID       uuid.UUID  `json:"sessionId"`
	SourceKind      string     `json:"sourceKind"`
	Path            string     `json:"path"`
	ParserVersion   int        `json:"parserVersion"`
	ByteOffset      int64      `json:"byteOffset"`
	ObservedSize    int64      `json:"observedSize"`
	ObservedModTime *time.Time `json:"observedModTime,omitempty"`
	LastEventKey    string     `json:"lastEventKey,omitempty"`
}

type sessionSourceRecord struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID       uuid.UUID  `gorm:"column:session_id;type:uuid"`
	SourceKind      string     `gorm:"column:source_kind"`
	Path            string     `gorm:"column:path"`
	SourceIdentity  *string    `gorm:"column:source_identity"`
	ParserVersion   int        `gorm:"column:parser_version"`
	ByteOffset      int64      `gorm:"column:byte_offset"`
	ObservedSize    int64      `gorm:"column:observed_size"`
	ObservedModTime *time.Time `gorm:"column:observed_mod_time"`
	LastEventKey    *string    `gorm:"column:last_event_key"`
}

func (sessionSourceRecord) TableName() string { return "captain_session_sources" }

type turnRecord struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID      uuid.UUID  `gorm:"column:session_id;type:uuid"`
	ProviderTurnID *string    `gorm:"column:provider_turn_id"`
	TurnIndex      int        `gorm:"column:turn_index"`
	Description    *string    `gorm:"column:description"`
	Status         TurnStatus `gorm:"column:status"`
	StopReason     *string    `gorm:"column:stop_reason"`
	StartedAt      *time.Time `gorm:"column:started_at"`
	EndedAt        *time.Time `gorm:"column:ended_at"`
}

func (turnRecord) TableName() string { return "captain_turns" }

type modelCallRecord struct {
	ID                  uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	TurnID              uuid.UUID  `gorm:"column:turn_id;type:uuid"`
	PromptRunID         *uuid.UUID `gorm:"column:prompt_run_id;type:uuid"`
	ProviderCallID      *string    `gorm:"column:provider_call_id"`
	CallIndex           int        `gorm:"column:call_index"`
	Model               string     `gorm:"column:model"`
	Backend             string     `gorm:"column:backend"`
	Effort              *string    `gorm:"column:effort"`
	Status              string     `gorm:"column:status"`
	StopReason          *string    `gorm:"column:stop_reason"`
	InputTokens         int64      `gorm:"column:input_tokens"`
	OutputTokens        int64      `gorm:"column:output_tokens"`
	ReasoningTokens     int64      `gorm:"column:reasoning_tokens"`
	CacheReadTokens     int64      `gorm:"column:cache_read_tokens"`
	CacheWriteTokens    int64      `gorm:"column:cache_write_tokens"`
	ContextTokens       int64      `gorm:"column:context_tokens"`
	ContextWindowTokens int64      `gorm:"column:context_window_tokens"`
	InputCost           float64    `gorm:"column:input_cost"`
	OutputCost          float64    `gorm:"column:output_cost"`
	ReasoningCost       float64    `gorm:"column:reasoning_cost"`
	CacheReadCost       float64    `gorm:"column:cache_read_cost"`
	CacheWriteCost      float64    `gorm:"column:cache_write_cost"`
	ProviderCostUSD     float64    `gorm:"column:provider_cost_usd"`
	StartedAt           *time.Time `gorm:"column:started_at"`
	EndedAt             *time.Time `gorm:"column:ended_at"`
}

func (modelCallRecord) TableName() string { return "captain_model_calls" }

type messageRecord struct {
	ID                uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID         uuid.UUID  `gorm:"column:session_id;type:uuid"`
	TurnID            *uuid.UUID `gorm:"column:turn_id;type:uuid"`
	ProviderMessageID *string    `gorm:"column:provider_message_id"`
	Sequence          int64      `gorm:"column:sequence"`
	Role              string     `gorm:"column:role"`
	Parts             []byte     `gorm:"column:parts;type:jsonb"`
	Raw               []byte     `gorm:"column:raw;type:jsonb"`
	SourceLine        *int64     `gorm:"column:source_line"`
	OccurredAt        *time.Time `gorm:"column:occurred_at"`
}

func (messageRecord) TableName() string { return "captain_messages" }

// IngestSessionInput identifies and projects the session one transcript file
// belongs to. Agent transcripts are child sessions of the root transcript's
// session via ParentSessionID.
type IngestSessionInput struct {
	ProviderSessionID string
	Source            string
	HostID            string
	ParentSessionID   *uuid.UUID
	Path              string
	Project           string
	CWD               string
	Title             string
	InitialPrompt     string
	Slug              string
	AgentType         string
	Description       string
	CLIVersion        string
	StartedAt         *time.Time
	LastActivityAt    *time.Time
	Git               map[string]any
	Metadata          map[string]any
}

// IngestSourceInput is the post-batch bookkeeping for the transcript file.
type IngestSourceInput struct {
	SourceKind      string
	Path            string
	SourceIdentity  string
	ParserVersion   int
	ByteOffset      int64
	ObservedSize    int64
	ObservedModTime time.Time
	LastEventKey    string
}

type IngestModelCall struct {
	Model               string
	Backend             string
	Effort              string
	StopReason          string
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	ContextTokens       int64
	ContextWindowTokens int64
	InputCost           float64
	OutputCost          float64
	CacheReadCost       float64
	CacheWriteCost      float64
	StartedAt           *time.Time
	EndedAt             *time.Time
}

// IngestTurn is one model turn. Call carries the turn-aggregate usage/cost as a
// single model call at call_index 0; re-ingesting an index updates it in place
// so an append-extended final turn converges.
type IngestTurn struct {
	Index          int
	ProviderTurnID string
	Description    string
	Status         TurnStatus
	StopReason     string
	StartedAt      *time.Time
	EndedAt        *time.Time
	Call           *IngestModelCall
}

// IngestMessage is one transcript message. Sequence must be stable and unique
// per session; the transcript line number satisfies both and doubles as the
// file seek reference surfaced to the UI.
type IngestMessage struct {
	Sequence          int64
	ProviderMessageID string
	Role              string
	TurnIndex         *int
	PartsJSON         []byte
	RawJSON           []byte
	SourceLine        int64
	OccurredAt        *time.Time
	// Provisional marks a row a later pass of a still-growing transcript can
	// complete. It is not persisted; it tells incremental ingest not to seal the
	// row behind its resume mark.
	Provisional bool
}

type IngestTranscriptInput struct {
	Session  IngestSessionInput
	Source   IngestSourceInput
	Turns    []IngestTurn
	Messages []IngestMessage
}

// ListSessionSources returns every transcript bookkeeping row keyed by path so
// a scanner can diff os.Stat results in one round trip.
func (db *DB) ListSessionSources(ctx context.Context) (map[string]SessionSourceState, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var records []sessionSourceRecord
	if err := db.gorm.WithContext(ctx).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain session sources: %w", err)
	}
	out := make(map[string]SessionSourceState, len(records))
	for _, r := range records {
		out[r.Path] = SessionSourceState{
			SessionID: r.SessionID, SourceKind: r.SourceKind, Path: r.Path,
			ParserVersion: r.ParserVersion, ByteOffset: r.ByteOffset, ObservedSize: r.ObservedSize,
			ObservedModTime: r.ObservedModTime, LastEventKey: optionalString(r.LastEventKey),
		}
	}
	return out, nil
}

// ingestBatchSize bounds one INSERT statement. Large enough that the first
// ingest of a long transcript is a handful of round trips, small enough that
// even the widest of these rows stays far inside the bind-parameter limit.
const ingestBatchSize = 500

// IngestTranscript persists one parsed transcript batch in three phases:
//
//  1. the session row, its projected columns, and the turn and model-call
//     aggregates, in one short transaction;
//  2. the message rows, outside that transaction;
//  3. the file's bookkeeping row, committed last.
//
// Phase 1 holds a write lock on the session row and phase 2 is by far the
// longest, so running them together kept that lock held for the whole message
// stream and blocked every other writer of the same session. Messages are
// immutable and idempotent under (session_id, sequence) and
// (session_id, provider_message_id), so they need not be atomic with the
// session row.
//
// Phase 3 commits last on purpose: a crash mid-ingest must leave the
// bookkeeping stale so the file is re-ingested — harmlessly, since every write
// here is idempotent — and never mark a file done for rows that were not
// written. Re-running the same batch changes nothing; an extended batch only
// adds or converges rows.
func (db *DB) IngestTranscript(ctx context.Context, input IngestTranscriptInput) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if err := validateIngest(input); err != nil {
		return nil, err
	}
	var session *Session
	var turnIDs map[int]uuid.UUID
	// Every write is idempotent (ON CONFLICT upserts plus column projection), so
	// a deadlock against a concurrent migration's exclusive locks is retried
	// rather than dropping the batch.
	err := retryTransientTx(ctx, "ingest Captain transcript", func() error {
		session, turnIDs = nil, nil
		return db.Transaction(ctx, func(tx *DB) error {
			var err error
			session, err = tx.CreateOrGetSession(ctx, CreateSessionInput{
				ProviderSessionID: input.Session.ProviderSessionID,
				Source:            input.Session.Source,
				HostID:            input.Session.HostID,
				ParentSessionID:   input.Session.ParentSessionID,
				Path:              input.Session.Path,
				Project:           input.Session.Project,
				CWD:               input.Session.CWD,
				Title:             input.Session.Title,
				InitialPrompt:     input.Session.InitialPrompt,
				Slug:              input.Session.Slug,
				AgentType:         input.Session.AgentType,
				Description:       input.Session.Description,
				CLIVersion:        input.Session.CLIVersion,
			})
			if err != nil {
				return err
			}
			if err := tx.projectSessionColumns(ctx, session.ID, input.Session); err != nil {
				return err
			}
			turnIDs, err = tx.upsertTurns(ctx, session.ID, input.Turns)
			return err
		})
	})
	if err != nil {
		return nil, err
	}

	if err := db.insertMessages(ctx, session.ID, turnIDs, input.Messages); err != nil {
		return nil, err
	}

	err = retryTransientTx(ctx, "record Captain transcript source", func() error {
		return db.upsertSessionSource(ctx, session.ID, input.Source)
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

func validateIngest(input IngestTranscriptInput) error {
	if strings.TrimSpace(input.Session.ProviderSessionID) == "" {
		return fmt.Errorf("%w: provider session ID is required", ErrInvalidIngest)
	}
	if strings.TrimSpace(input.Source.Path) == "" || strings.TrimSpace(input.Source.SourceKind) == "" {
		return fmt.Errorf("%w: source kind and path are required", ErrInvalidIngest)
	}
	if input.Source.ParserVersion <= 0 {
		return fmt.Errorf("%w: parser version must be positive", ErrInvalidIngest)
	}
	for _, m := range input.Messages {
		if m.Sequence < 0 || strings.TrimSpace(m.Role) == "" {
			return fmt.Errorf("%w: message sequence %d needs a nonnegative sequence and role", ErrInvalidIngest, m.Sequence)
		}
	}
	return nil
}

// projectSessionColumns overwrites the monitor-owned projection columns with
// the freshly parsed values. Identity, hierarchy, and state-machine columns are
// never touched here. last_activity_at is the exception to the overwrite: it
// only ever advances (see below).
func (db *DB) projectSessionColumns(ctx context.Context, id uuid.UUID, input IngestSessionInput) error {
	updates := map[string]any{}
	for column, value := range map[string]string{
		"path": input.Path, "project": input.Project, "cwd": normalizeCWD(input.CWD), "title": input.Title,
		"initial_prompt": input.InitialPrompt, "slug": input.Slug, "agent_type": input.AgentType,
		"description": input.Description, "cli_version": input.CLIVersion,
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			updates[column] = trimmed
		}
	}
	if input.StartedAt != nil {
		updates["started_at"] = *input.StartedAt
	}
	if input.LastActivityAt != nil {
		// The transcript max is real evidence: entries the child rows never
		// receive (non-conversational lines, events) push it past anything the
		// activity trigger can derive. But it is only ever evidence of activity,
		// never of its absence -- a re-ingest re-derives a single file and must
		// not erase prompt-run, turn, or event activity that file never
		// contained. Monotonic here, exactly as in captain_touch_session_activity.
		updates["last_activity_at"] = gorm.Expr("GREATEST(last_activity_at, ?)", input.LastActivityAt.UTC())
	}
	if input.Git != nil {
		updates["git"] = jsonbValue(input.Git)
	}
	if input.Metadata != nil {
		updates["metadata"] = gorm.Expr("metadata || ?::jsonb", jsonbValue(input.Metadata))
	}
	if len(updates) == 0 {
		return nil
	}
	if err := db.gorm.WithContext(ctx).Model(&sessionRecord{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("project Captain session columns: %w", err)
	}
	return nil
}

func (db *DB) upsertSessionSource(ctx context.Context, sessionID uuid.UUID, input IngestSourceInput) error {
	record := sessionSourceRecord{
		ID: uuid.New(), SessionID: sessionID, SourceKind: strings.TrimSpace(input.SourceKind),
		Path: strings.TrimSpace(input.Path), SourceIdentity: nullableTrimmed(input.SourceIdentity),
		ParserVersion: input.ParserVersion, ByteOffset: input.ByteOffset, ObservedSize: input.ObservedSize,
		LastEventKey: nullableTrimmed(input.LastEventKey),
	}
	if !input.ObservedModTime.IsZero() {
		modTime := input.ObservedModTime.UTC()
		record.ObservedModTime = &modTime
	}
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_kind"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"session_id", "source_identity", "parser_version", "byte_offset",
			"observed_size", "observed_mod_time", "last_event_key",
		}),
	}).Create(&record).Error
	if err != nil {
		return fmt.Errorf("upsert Captain session source %s: %w", record.Path, err)
	}
	return nil
}

// upsertTurns converges turn and per-turn aggregate model-call rows and returns
// the turn UUID for each ingested turn index so messages can reference turns.
//
// Both writes are batched. A transcript append re-offers every historical turn,
// and the previous shape — upsert, read back, upsert the call — cost three
// round trips per turn on every append.
func (db *DB) upsertTurns(ctx context.Context, sessionID uuid.UUID, turns []IngestTurn) (map[int]uuid.UUID, error) {
	ids := make(map[int]uuid.UUID, len(turns))
	records, err := turnRecords(sessionID, turns)
	if err != nil || len(records) == 0 {
		return ids, err
	}
	// RETURNING carries turn_index back beside the ID, so the map is keyed from
	// each row's own index rather than from the order PostgreSQL returns them.
	err = db.gorm.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns: []clause.Column{{Name: "session_id"}, {Name: "turn_index"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"provider_turn_id", "description", "status", "stop_reason", "started_at", "ended_at",
			}),
		},
		clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "turn_index"}}},
	).CreateInBatches(&records, ingestBatchSize).Error
	if err != nil {
		return nil, fmt.Errorf("upsert %d Captain turns: %w", len(records), err)
	}
	for _, record := range records {
		ids[record.TurnIndex] = record.ID
	}

	calls := make([]modelCallRecord, 0, len(turns))
	for _, turn := range turns {
		if turn.Call == nil {
			continue
		}
		turnID, ok := ids[turn.Index]
		if !ok {
			return nil, fmt.Errorf("upsert Captain model call: turn %d was not persisted", turn.Index)
		}
		calls = append(calls, turnCallRecord(turnID, *turn.Call))
	}
	if err := db.upsertTurnCalls(ctx, calls); err != nil {
		return nil, err
	}
	return ids, nil
}

// turnRecords maps the batch to rows, keeping the last entry for a repeated
// index: a single INSERT cannot apply DO UPDATE to the same row twice, and
// last-wins is what the previous row-at-a-time upsert produced.
func turnRecords(sessionID uuid.UUID, turns []IngestTurn) ([]turnRecord, error) {
	position := make(map[int]int, len(turns))
	records := make([]turnRecord, 0, len(turns))
	for _, turn := range turns {
		if turn.Index < 0 {
			return nil, fmt.Errorf("%w: turn index %d is negative", ErrInvalidIngest, turn.Index)
		}
		status := turn.Status
		if status == "" {
			status = TurnStatusEnded
		}
		record := turnRecord{
			ID: uuid.New(), SessionID: sessionID, TurnIndex: turn.Index,
			ProviderTurnID: nullableTrimmed(turn.ProviderTurnID), Description: nullableTrimmed(turn.Description),
			Status: status, StopReason: nullableTrimmed(turn.StopReason),
			StartedAt: turn.StartedAt, EndedAt: turn.EndedAt,
		}
		if at, ok := position[turn.Index]; ok {
			records[at] = record
			continue
		}
		position[turn.Index] = len(records)
		records = append(records, record)
	}
	return records, nil
}

func turnCallRecord(turnID uuid.UUID, call IngestModelCall) modelCallRecord {
	model := strings.TrimSpace(call.Model)
	if model == "" {
		model = "unknown"
	}
	backend := strings.TrimSpace(call.Backend)
	if backend == "" {
		backend = "unknown"
	}
	return modelCallRecord{
		ID: uuid.New(), TurnID: turnID, CallIndex: 0, Model: model, Backend: backend,
		Effort: nullableTrimmed(call.Effort), Status: "succeeded", StopReason: nullableTrimmed(call.StopReason),
		InputTokens: call.InputTokens, OutputTokens: call.OutputTokens, ReasoningTokens: call.ReasoningTokens,
		CacheReadTokens: call.CacheReadTokens, CacheWriteTokens: call.CacheWriteTokens,
		ContextTokens: call.ContextTokens, ContextWindowTokens: call.ContextWindowTokens,
		InputCost: call.InputCost, OutputCost: call.OutputCost,
		CacheReadCost: call.CacheReadCost, CacheWriteCost: call.CacheWriteCost,
		StartedAt: call.StartedAt, EndedAt: call.EndedAt,
	}
}

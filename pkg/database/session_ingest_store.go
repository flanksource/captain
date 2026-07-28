package database

import (
	"bytes"
	"context"
	"encoding/json"
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
	CacheReadCost       float64    `gorm:"column:cache_read_cost"`
	CacheWriteCost      float64    `gorm:"column:cache_write_cost"`
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
		updates["metadata"] = jsonbValue(input.Metadata)
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

func (db *DB) upsertTurnCalls(ctx context.Context, calls []modelCallRecord) error {
	if len(calls) == 0 {
		return nil
	}
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "turn_id"}, {Name: "call_index"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"model", "backend", "effort", "stop_reason",
			"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_write_tokens",
			"context_tokens", "context_window_tokens",
			"input_cost", "output_cost", "cache_read_cost", "cache_write_cost",
			"started_at", "ended_at",
		}),
	}).CreateInBatches(&calls, ingestBatchSize).Error
	if err != nil {
		return fmt.Errorf("upsert %d Captain model calls: %w", len(calls), err)
	}
	return nil
}

// insertMessages writes message rows. A message's identity is durable -- either
// (session_id, sequence) or (session_id, provider_message_id) -- but its content
// is not: a row parsed from a still-growing transcript can be re-offered later
// with the tool result or the closing reasoning record it was missing. Identity
// is established here and content converges in convergeMessages.
func (db *DB) insertMessages(ctx context.Context, sessionID uuid.UUID, turnIDs map[int]uuid.UUID, messages []IngestMessage) error {
	records := make([]messageRecord, 0, len(messages))
	for _, message := range messages {
		parts := message.PartsJSON
		if len(parts) == 0 {
			parts = []byte("[]")
		}
		parts, err := sanitizePostgresJSON(parts)
		if err != nil {
			return fmt.Errorf("insert Captain message seq %d parts: %w", message.Sequence, err)
		}
		raw, err := sanitizePostgresJSON(message.RawJSON)
		if err != nil {
			return fmt.Errorf("insert Captain message seq %d raw: %w", message.Sequence, err)
		}
		record := messageRecord{
			ID: uuid.New(), SessionID: sessionID,
			ProviderMessageID: nullableTrimmed(message.ProviderMessageID),
			Sequence:          message.Sequence, Role: strings.TrimSpace(message.Role),
			Parts: parts, Raw: raw, OccurredAt: message.OccurredAt,
		}
		if message.SourceLine > 0 {
			line := message.SourceLine
			record.SourceLine = &line
		}
		if message.TurnIndex != nil {
			if turnID, ok := turnIDs[*message.TurnIndex]; ok {
				record.TurnID = &turnID
			}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil
	}
	// Do not name one conflict target: a provider replay can retain the same
	// message ID while its parser sequence shifts. PostgreSQL must suppress
	// either unique-key conflict, while check/FK failures still surface.
	// DO NOTHING also tolerates a duplicate inside a single batch, which
	// DO UPDATE would reject.
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&records, ingestBatchSize).Error
	if err != nil {
		return fmt.Errorf("insert %d Captain messages: %w", len(records), err)
	}
	return db.convergeMessages(ctx, records)
}

// convergeMessages rewrites rows an earlier pass stored before the transcript had
// caught up. Transcripts are append-only, so re-parsing a line never yields less
// than it did before -- it yields the tool result that had not been written yet,
// or the reasoning record that closed the span. Without this, the truncated row
// stood forever: 36 818 of 172 743 Codex tool parts carry no result today.
//
// It has to be its own statement because the insert above cannot both suppress
// two unique-key conflicts and update on one of them. provider_message_id is
// deliberately never assigned here: leaving identity alone is what makes this
// update incapable of violating a unique constraint.
func (db *DB) convergeMessages(ctx context.Context, records []messageRecord) error {
	const columns = "role, parts, raw, turn_id, source_line, occurred_at"
	for start := 0; start < len(records); start += ingestBatchSize {
		batch := records[start:min(start+ingestBatchSize, len(records))]
		rows := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*8)
		for _, record := range batch {
			rows = append(rows, "(?::uuid,?::bigint,?::text,?::jsonb,?::jsonb,?::uuid,?::bigint,?::timestamptz)")
			args = append(args, record.SessionID, record.Sequence, record.Role,
				jsonbArg(record.Parts), jsonbArg(record.Raw),
				record.TurnID, record.SourceLine, record.OccurredAt)
		}
		query := `UPDATE captain_messages m SET
			role = v.role, parts = v.parts, raw = v.raw,
			turn_id = v.turn_id, source_line = v.source_line, occurred_at = v.occurred_at
		FROM (VALUES ` + strings.Join(rows, ",") + `)
			AS v(session_id, sequence, ` + columns + `)
		WHERE m.session_id = v.session_id AND m.sequence = v.sequence
		  AND (m.` + strings.ReplaceAll(columns, ", ", ", m.") + `)
		      IS DISTINCT FROM (v.` + strings.ReplaceAll(columns, ", ", ", v.") + `)`
		if err := db.gorm.WithContext(ctx).Exec(query, args...).Error; err != nil {
			return fmt.Errorf("converge %d Captain messages: %w", len(batch), err)
		}
	}
	return nil
}

// jsonbArg keeps an empty document out of a ::jsonb cast, which rejects "", and
// keeps a populated one off the bytea path pgx would otherwise choose for bytes.
func jsonbArg(document []byte) any {
	if len(document) == 0 {
		return nil
	}
	return string(document)
}

func jsonbValue(value map[string]any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	encoded, err = sanitizePostgresJSON(encoded)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// sanitizePostgresJSON replaces JSON string escapes for U+0000 with U+FFFD.
// PostgreSQL jsonb rejects U+0000 because its text representation cannot store
// a zero byte, even though \u0000 is valid JSON. This scanner only rewrites an
// active JSON escape: a literal string such as "\\u0000" remains unchanged.
func sanitizePostgresJSON(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON")
	}
	if !bytes.Contains(data, []byte(`\u0000`)) {
		return data, nil
	}

	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	changed := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if !inString {
			out = append(out, b)
			if b == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			out = append(out, b)
			escaped = false
			continue
		}
		if b == '"' {
			out = append(out, b)
			inString = false
			continue
		}
		if b != '\\' {
			out = append(out, b)
			continue
		}
		if i+5 < len(data) && data[i+1] == 'u' && bytes.Equal(data[i+2:i+6], []byte("0000")) {
			out = append(out, []byte(`\ufffd`)...)
			i += 5
			changed = true
			continue
		}
		out = append(out, b)
		escaped = true
	}
	if !changed {
		return data, nil
	}
	return out, nil
}

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

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
			"input_cost", "output_cost", "reasoning_cost", "cache_read_cost", "cache_write_cost", "currency",
			"started_at", "ended_at",
		}),
	}).CreateInBatches(&calls, ingestBatchSize).Error
	if err != nil {
		return fmt.Errorf("upsert %d Captain model calls: %w", len(calls), err)
	}
	return nil
}

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
			ID: uuid.New(), SessionID: sessionID, ProviderMessageID: nullableTrimmed(message.ProviderMessageID),
			Sequence: message.Sequence, Role: strings.TrimSpace(message.Role), Parts: parts, Raw: raw, OccurredAt: message.OccurredAt,
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
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&records, ingestBatchSize).Error
	if err != nil {
		return fmt.Errorf("insert %d Captain messages: %w", len(records), err)
	}
	return db.convergeMessages(ctx, records)
}

func (db *DB) convergeMessages(ctx context.Context, records []messageRecord) error {
	const columns = "role, parts, raw, turn_id, source_line, occurred_at"
	for start := 0; start < len(records); start += ingestBatchSize {
		batch := records[start:min(start+ingestBatchSize, len(records))]
		rows := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*8)
		for _, record := range batch {
			rows = append(rows, "(?::uuid,?::bigint,?::text,?::jsonb,?::jsonb,?::uuid,?::bigint,?::timestamptz)")
			args = append(args, record.SessionID, record.Sequence, record.Role,
				jsonbArg(record.Parts), jsonbArg(record.Raw), record.TurnID, record.SourceLine, record.OccurredAt)
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
	inString, escaped, changed := false, false, false
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

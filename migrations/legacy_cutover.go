package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
)

const (
	legacyCutoverKey       = "captain-session-cache-v1"
	legacySessionsTable    = "captain_sessions"
	legacyPromptsTable     = "captain_session_prompts"
	archiveSessionsTable   = "captain_sessions_legacy_v1"
	archivePromptsTable    = "captain_session_prompts_legacy_v1"
	legacyCutoverStateNote = "imported from the legacy Captain session cache"
)

var (
	legacySessionNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/flanksource/captain/legacy-session-cache/v1"))
	legacyPromptNamespace  = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/flanksource/captain/legacy-session-prompt/v1"))
	legacySourceNamespace  = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/flanksource/captain/legacy-session-source/v1"))
)

// LegacySessionCutoverReport is the durable validation result for the explicit
// legacy path-keyed session-cache cutover. The same values are stored in
// captain_legacy_session_cutovers; the versioned archive tables remain intact
// as the rollback artifact.
type LegacySessionCutoverReport struct {
	CutoverKey               string          `json:"cutoverKey"`
	LegacySessionsTable      string          `json:"legacySessionsTable"`
	LegacyPromptsTable       *string         `json:"legacyPromptsTable,omitempty"`
	LegacySessionRows        int64           `json:"legacySessionRows"`
	LegacyPromptRows         int64           `json:"legacyPromptRows"`
	ImportedSessionRows      int64           `json:"importedSessionRows"`
	ImportedPromptRunRows    int64           `json:"importedPromptRunRows"`
	LegacySessionsChecksum   string          `json:"legacySessionsChecksum"`
	LegacyPromptsChecksum    *string         `json:"legacyPromptsChecksum,omitempty"`
	NativeSessionsChecksum   string          `json:"nativeSessionsChecksum"`
	NativePromptRunsChecksum *string         `json:"nativePromptRunsChecksum,omitempty"`
	Details                  json.RawMessage `json:"details"`
	StartedAt                time.Time       `json:"startedAt"`
	CompletedAt              time.Time       `json:"completedAt"`
	UpdatedAt                time.Time       `json:"updatedAt"`
}

type archiveState struct {
	hasArchive       bool
	hasPromptArchive bool
}

type legacySessionRow struct {
	Raw       json.RawMessage
	Data      map[string]any
	Path      string
	ID        string
	ParentID  string
	Source    string
	StartedAt *time.Time
	EndedAt   *time.Time
	UpdatedAt *time.Time
	NativeID  uuid.UUID
	Parent    *legacySessionRow
	Root      *legacySessionRow
	Depth     int
}

type legacyPromptRow struct {
	Raw       json.RawMessage
	Data      map[string]any
	SessionID string
	RunID     string
	NativeID  uuid.UUID
}

// ApplyWithLegacySessionCutover is the explicit host opt-in for replacing the
// old GORM session summary cache with Captain's authoritative HCL schema. It:
//
//  1. validates and copies the legacy tables to versioned archive tables,
//  2. verifies the copies byte-for-byte before dropping the conflicting names,
//  3. applies Captain's commons-db/migrate HCL bundle,
//  4. backfills native sessions and prompt runs, and
//  5. persists and returns a checksum/count validation report.
//
// Every phase is idempotent. A crash after archiving resumes from the archive;
// a crash after migration resumes the upserts. Apply remains conservative and
// continues to reject the legacy shape unless a host calls this API explicitly.
func ApplyWithLegacySessionCutover(ctx context.Context, connection string) (_ *LegacySessionCutoverReport, resultErr error) {
	connection = strings.TrimSpace(connection)
	if connection == "" {
		return nil, errors.New("Captain migration connection string is empty")
	}

	lock, err := acquireMigrationLock(ctx, connection)
	if err != nil {
		return nil, fmt.Errorf("acquire Captain migration lock: %w", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release Captain migration lock: %w", err))
		}
	}()

	db, err := commonsdb.NewDB(connection)
	if err != nil {
		return nil, fmt.Errorf("open Captain legacy cutover database: %w", err)
	}
	state, err := archiveLegacySessionCache(ctx, db)
	if closeErr := db.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close Captain legacy cutover database: %w", closeErr))
	}
	if err != nil {
		return nil, err
	}

	if err := defaultApplyDependencies.migrate(ctx, connection); err != nil {
		return nil, fmt.Errorf("migrate Captain database after legacy archive: %w", err)
	}

	db, err = commonsdb.NewDB(connection)
	if err != nil {
		return nil, fmt.Errorf("open migrated Captain cutover database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close migrated Captain cutover database: %w", err))
		}
	}()

	if !state.hasArchive {
		exists, err := storedCutoverExists(ctx, db)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("Captain legacy cutover report exists but rollback table public.%s is missing", archiveSessionsTable)
		}
		return nil, nil
	}

	return backfillLegacySessionCache(ctx, db, state)
}

func archiveLegacySessionCache(ctx context.Context, db *sql.DB) (archiveState, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return archiveState{}, fmt.Errorf("begin Captain legacy archive transaction: %w", err)
	}
	defer tx.Rollback()

	sessionsExists, err := relationExists(ctx, tx, legacySessionsTable)
	if err != nil {
		return archiveState{}, err
	}
	sessionsArchiveExists, err := relationExists(ctx, tx, archiveSessionsTable)
	if err != nil {
		return archiveState{}, err
	}
	promptsExists, err := relationExists(ctx, tx, legacyPromptsTable)
	if err != nil {
		return archiveState{}, err
	}
	promptsArchiveExists, err := relationExists(ctx, tx, archivePromptsTable)
	if err != nil {
		return archiveState{}, err
	}

	if promptsArchiveExists && !sessionsArchiveExists {
		return archiveState{}, fmt.Errorf("orphaned Captain rollback table public.%s exists without public.%s", archivePromptsTable, archiveSessionsTable)
	}
	if sessionsArchiveExists {
		if sessionsExists {
			columns, err := relationColumns(ctx, tx, legacySessionsTable)
			if err != nil {
				return archiveState{}, err
			}
			if isLegacySessionSchema(columns) {
				return archiveState{}, fmt.Errorf("both live legacy table public.%s and rollback table public.%s exist; refusing to choose or overwrite either", legacySessionsTable, archiveSessionsTable)
			}
			if !isAuthoritativeSessionSchema(columns) {
				return archiveState{}, fmt.Errorf("unknown public.%s shape exists beside the rollback archive; refusing to migrate it", legacySessionsTable)
			}
		}
		if promptsExists {
			return archiveState{}, fmt.Errorf("legacy prompt table public.%s still exists beside an already archived session cache", legacyPromptsTable)
		}
		return archiveState{hasArchive: true, hasPromptArchive: promptsArchiveExists}, tx.Commit()
	}

	if !sessionsExists {
		if promptsExists {
			return archiveState{}, fmt.Errorf("legacy prompt table public.%s exists without public.%s", legacyPromptsTable, legacySessionsTable)
		}
		return archiveState{}, tx.Commit()
	}

	columns, err := relationColumns(ctx, tx, legacySessionsTable)
	if err != nil {
		return archiveState{}, err
	}
	if !isLegacySessionSchema(columns) {
		if !isAuthoritativeSessionSchema(columns) {
			return archiveState{}, fmt.Errorf("unknown public.%s shape; expected the legacy path-keyed cache or Captain's authoritative UUID schema", legacySessionsTable)
		}
		if promptsExists {
			return archiveState{}, fmt.Errorf("stray legacy table public.%s exists beside Captain's authoritative session schema", legacyPromptsTable)
		}
		return archiveState{}, tx.Commit()
	}
	// Freeze both cache tables before the first snapshot. Without this lock a
	// legacy cache writer could commit between checksum validation and DROP.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE public.captain_sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
		return archiveState{}, fmt.Errorf("freeze Captain legacy session cache: %w", err)
	}
	if promptsExists {
		if _, err := tx.ExecContext(ctx, `LOCK TABLE public.captain_session_prompts IN ACCESS EXCLUSIVE MODE`); err != nil {
			return archiveState{}, fmt.Errorf("freeze Captain legacy prompt cache: %w", err)
		}
	}

	sessions, sessionRaw, err := loadLegacySessions(ctx, tx, legacySessionsTable)
	if err != nil {
		return archiveState{}, err
	}
	prompts, promptRaw, err := loadLegacyPromptsIfPresent(ctx, tx, promptsExists, legacyPromptsTable)
	if err != nil {
		return archiveState{}, err
	}
	if err := validateLegacyDataset(sessions, prompts); err != nil {
		return archiveState{}, fmt.Errorf("validate Captain legacy session cache: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `CREATE TABLE public.captain_sessions_legacy_v1
		(LIKE public.captain_sessions INCLUDING ALL)`); err != nil {
		return archiveState{}, fmt.Errorf("create Captain legacy session rollback table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO public.captain_sessions_legacy_v1
		SELECT * FROM public.captain_sessions`); err != nil {
		return archiveState{}, fmt.Errorf("copy Captain legacy session rollback data: %w", err)
	}
	if promptsExists {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE public.captain_session_prompts_legacy_v1
			(LIKE public.captain_session_prompts INCLUDING ALL)`); err != nil {
			return archiveState{}, fmt.Errorf("create Captain legacy prompt rollback table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.captain_session_prompts_legacy_v1
			SELECT * FROM public.captain_session_prompts`); err != nil {
			return archiveState{}, fmt.Errorf("copy Captain legacy prompt rollback data: %w", err)
		}
	}

	_, archivedSessionRaw, err := loadLegacySessions(ctx, tx, archiveSessionsTable)
	if err != nil {
		return archiveState{}, err
	}
	if checksumJSONRows(sessionRaw) != checksumJSONRows(archivedSessionRaw) || len(sessionRaw) != len(archivedSessionRaw) {
		return archiveState{}, errors.New("Captain legacy session rollback copy failed count/checksum validation")
	}
	if promptsExists {
		_, archivedPromptRaw, err := loadLegacyPromptsIfPresent(ctx, tx, true, archivePromptsTable)
		if err != nil {
			return archiveState{}, err
		}
		if checksumJSONRows(promptRaw) != checksumJSONRows(archivedPromptRaw) || len(promptRaw) != len(archivedPromptRaw) {
			return archiveState{}, errors.New("Captain legacy prompt rollback copy failed count/checksum validation")
		}
	}

	if promptsExists {
		if _, err := tx.ExecContext(ctx, `DROP TABLE public.captain_session_prompts`); err != nil {
			return archiveState{}, fmt.Errorf("remove validated legacy prompt table name: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE public.captain_sessions`); err != nil {
		return archiveState{}, fmt.Errorf("remove validated legacy session table name: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return archiveState{}, fmt.Errorf("commit Captain legacy rollback archive: %w", err)
	}
	return archiveState{hasArchive: true, hasPromptArchive: promptsExists}, nil
}

func isAuthoritativeSessionSchema(columns map[string]string) bool {
	return columns["id"] == "uuid" &&
		columns["lifecycle_status"] == "USER-DEFINED" &&
		columns["activity_state"] == "USER-DEFINED" &&
		columns["health_state"] == "USER-DEFINED"
}

func backfillLegacySessionCache(ctx context.Context, db *sql.DB, state archiveState) (*LegacySessionCutoverReport, error) {
	startedAt := time.Now().UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin Captain legacy backfill transaction: %w", err)
	}
	defer tx.Rollback()

	sessions, sessionRaw, err := loadLegacySessions(ctx, tx, archiveSessionsTable)
	if err != nil {
		return nil, err
	}
	prompts, promptRaw, err := loadLegacyPromptsIfPresent(ctx, tx, state.hasPromptArchive, archivePromptsTable)
	if err != nil {
		return nil, err
	}
	if err := validateLegacyDataset(sessions, prompts); err != nil {
		return nil, fmt.Errorf("revalidate Captain legacy rollback archive: %w", err)
	}
	if err := setLegacyBackfillEmitTriggers(ctx, tx, false); err != nil {
		return nil, err
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Depth == sessions[j].Depth {
			return sessions[i].Path < sessions[j].Path
		}
		return sessions[i].Depth < sessions[j].Depth
	})

	nativeSessionLines := make([]string, 0, len(sessions))
	nativeSourceLines := make([]string, 0, len(sessions))
	for _, session := range sessions {
		line, err := upsertAndValidateNativeSession(ctx, tx, session)
		if err != nil {
			return nil, err
		}
		nativeSessionLines = append(nativeSessionLines, string(line))
		sourceLine, err := upsertAndValidateNativeSessionSource(ctx, tx, session)
		if err != nil {
			return nil, err
		}
		nativeSourceLines = append(nativeSourceLines, string(sourceLine))
	}
	sort.Strings(nativeSessionLines)
	sort.Strings(nativeSourceLines)

	nativePromptLines := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		line, err := upsertAndValidateNativePromptRun(ctx, tx, prompt, sessions)
		if err != nil {
			return nil, err
		}
		nativePromptLines = append(nativePromptLines, string(line))
	}
	sort.Strings(nativePromptLines)
	if err := setLegacyBackfillEmitTriggers(ctx, tx, true); err != nil {
		return nil, err
	}

	// Re-select after every insert and trigger has completed. The durable report
	// hashes database state, not the intended inputs returned by the upsert
	// helpers, so later drift cannot masquerade as a successful replay.
	nativeSessionLines = nativeSessionLines[:0]
	nativeSourceLines = nativeSourceLines[:0]
	for _, session := range sessions {
		line, err := readPersistedNativeRow(ctx, tx, "captain_sessions", session.NativeID)
		if err != nil {
			return nil, err
		}
		nativeSessionLines = append(nativeSessionLines, string(line))
		sourceID := uuid.NewSHA1(legacySourceNamespace, []byte(session.Path))
		sourceLine, err := readPersistedNativeRow(ctx, tx, "captain_session_sources", sourceID)
		if err != nil {
			return nil, err
		}
		nativeSourceLines = append(nativeSourceLines, string(sourceLine))
	}
	nativePromptLines = nativePromptLines[:0]
	for _, prompt := range prompts {
		line, err := readPersistedNativeRow(ctx, tx, "captain_prompt_runs", prompt.NativeID)
		if err != nil {
			return nil, err
		}
		nativePromptLines = append(nativePromptLines, string(line))
	}
	sort.Strings(nativeSessionLines)
	sort.Strings(nativeSourceLines)
	sort.Strings(nativePromptLines)

	nativeSessionStateLines := make([]string, 0, len(nativeSessionLines)+len(nativeSourceLines))
	for _, line := range nativeSessionLines {
		nativeSessionStateLines = append(nativeSessionStateLines, "session\x00"+line)
	}
	for _, line := range nativeSourceLines {
		nativeSessionStateLines = append(nativeSessionStateLines, "source\x00"+line)
	}
	sort.Strings(nativeSessionStateLines)

	completedAt := time.Now().UTC()
	sessionMappings := make([]map[string]any, 0, len(sessions))
	providerFallbacks := 0
	legacyPlans := 0
	for _, session := range sessions {
		if nestedString(session.Data, "provider", "name") == "" {
			providerFallbacks++
		}
		if plan, ok := session.Data["plan"].(map[string]any); ok && len(plan) > 0 {
			legacyPlans++
		}
		mapping := map[string]any{
			"path": session.Path, "legacyId": session.ID, "nativeId": session.NativeID,
			"source": session.Source, "provider": legacyProvider(session), "hostId": "local",
		}
		if session.Parent != nil {
			mapping["parentNativeId"] = session.Parent.NativeID
			mapping["rootNativeId"] = session.Root.NativeID
		}
		sessionMappings = append(sessionMappings, mapping)
	}
	promptMappings := make([]map[string]any, 0, len(prompts))
	for _, prompt := range prompts {
		promptMappings = append(promptMappings, map[string]any{
			"legacySessionId": prompt.SessionID, "legacyRunId": prompt.RunID, "nativePromptRunId": prompt.NativeID,
		})
	}
	archiveSessionColumns, err := relationColumns(ctx, tx, archiveSessionsTable)
	if err != nil {
		return nil, err
	}
	archivePromptColumns := map[string]string{}
	if state.hasPromptArchive {
		archivePromptColumns, err = relationColumns(ctx, tx, archivePromptsTable)
		if err != nil {
			return nil, err
		}
	}
	details, err := json.Marshal(map[string]any{
		"archiveValidation": "source and rollback rows matched before the conflicting legacy table names were removed",
		"identityMapping":   "valid legacy UUIDs are preserved; other IDs use UUIDv5 in Captain's fixed legacy-session namespace",
		"lifecycleMapping":  "ended sessions become succeeded, started sessions without an end become interrupted, otherwise created",
		"mappingLedger": map[string]any{
			"sessions": sessionMappings,
			"prompts":  promptMappings,
		},
		"nativeSessionSources": map[string]any{
			"rows":     len(nativeSourceLines),
			"checksum": checksumStrings(nativeSourceLines),
		},
		"schemaFingerprints": map[string]any{
			"sessions": checksumColumns(archiveSessionColumns),
			"prompts":  checksumColumns(archivePromptColumns),
		},
		"warnings": map[string]any{
			"providerFallbackRows": providerFallbacks,
			"legacyPlanRows":       legacyPlans,
			"legacyPlanHandling":   "preserved losslessly in native session metadata and rollback archive; no synthetic execution plan was fabricated",
			"summaryOnly":          "aggregate cost, usage, files, approvals, and message counts remain in native session metadata and the rollback archive",
		},
		"rollback": map[string]any{
			"sessionsTable": archiveSessionsTable,
			"promptsTable":  optionalArchiveName(state.hasPromptArchive),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal Captain legacy cutover details: %w", err)
	}

	report := &LegacySessionCutoverReport{
		CutoverKey:               legacyCutoverKey,
		LegacySessionsTable:      archiveSessionsTable,
		LegacyPromptsTable:       optionalStringPointer(optionalArchiveName(state.hasPromptArchive)),
		LegacySessionRows:        int64(len(sessions)),
		LegacyPromptRows:         int64(len(prompts)),
		ImportedSessionRows:      int64(len(nativeSessionLines)),
		ImportedPromptRunRows:    int64(len(nativePromptLines)),
		LegacySessionsChecksum:   checksumJSONRows(sessionRaw),
		LegacyPromptsChecksum:    optionalChecksum(state.hasPromptArchive, promptRaw),
		NativeSessionsChecksum:   checksumStrings(nativeSessionStateLines),
		NativePromptRunsChecksum: optionalChecksum(state.hasPromptArchive, stringsToRaw(nativePromptLines)),
		Details:                  details,
		StartedAt:                startedAt,
		CompletedAt:              completedAt,
		UpdatedAt:                completedAt,
	}

	existing, exists, err := readCutoverReport(ctx, tx)
	if err != nil {
		return nil, err
	}
	if exists {
		if !sameCutoverValidation(existing, report) {
			return nil, errors.New("Captain legacy cutover validation no longer matches the durable completed report")
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit repeated Captain legacy session validation: %w", err)
		}
		return existing, nil
	}
	if err := insertCutoverReport(ctx, tx, report); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Captain legacy session backfill: %w", err)
	}
	return report, nil
}

func upsertAndValidateNativeSessionSource(ctx context.Context, tx *sql.Tx, row *legacySessionRow) (string, error) {
	sourceID := uuid.NewSHA1(legacySourceNamespace, []byte(row.Path))
	observedSize := jsonInt64(row.Data, "size")
	byteOffset := observedSize
	parserVersion := jsonInt64(row.Data, "summary_version")
	if parserVersion < 1 {
		parserVersion = 1
	}
	var observedModTime any
	if modUnix := jsonInt64(row.Data, "mod_unix"); modUnix > 0 {
		observedModTime = time.Unix(0, modUnix).UTC()
	}
	createdAt := firstTime(row.StartedAt, row.UpdatedAt)
	updatedAt := firstTime(row.UpdatedAt, row.EndedAt, row.StartedAt)

	_, err := tx.ExecContext(ctx, `INSERT INTO public.captain_session_sources (
		id, session_id, source_kind, path, source_identity, parser_version,
		byte_offset, observed_size, observed_mod_time, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	ON CONFLICT (id) DO NOTHING`,
		sourceID, row.NativeID, row.Source, row.Path, row.ID, parserVersion,
		byteOffset, observedSize, observedModTime, createdAt, updatedAt,
	)
	if err != nil {
		return "", fmt.Errorf("backfill native Captain session source %q: %w", row.Path, err)
	}

	actual, err := validatePersistedNativeRow(ctx, tx, "captain_session_sources", sourceID, map[string]any{
		"id": sourceID, "session_id": row.NativeID, "source_kind": row.Source,
		"path": row.Path, "source_identity": row.ID, "parser_version": parserVersion,
		"byte_offset": byteOffset, "observed_size": observedSize,
		"observed_mod_time": observedModTime, "last_event_key": nil,
		"created_at": createdAt, "updated_at": updatedAt,
	})
	if err != nil {
		return "", fmt.Errorf("native Captain session source %s conflicts with legacy path %q: %w", sourceID, row.Path, err)
	}
	return actual, nil
}

func upsertAndValidateNativeSession(ctx context.Context, tx *sql.Tx, row *legacySessionRow) (string, error) {
	provider := legacyProvider(row)

	var parentID, rootID any
	if row.Parent != nil {
		parentID = row.Parent.NativeID
		rootID = row.Root.NativeID
	}

	lifecycle := "created"
	if row.EndedAt != nil {
		lifecycle = "succeeded"
	} else if row.StartedAt != nil {
		lifecycle = "interrupted"
	}
	observedAt := firstTime(row.UpdatedAt, row.EndedAt, row.StartedAt)
	createdAt := firstTime(row.StartedAt, row.UpdatedAt)
	updatedAt := firstTime(row.UpdatedAt, row.EndedAt, row.StartedAt)

	git := row.Data["git"]
	if _, ok := git.(map[string]any); !ok {
		git = map[string]any{}
	}
	gitJSON, err := json.Marshal(git)
	if err != nil {
		return "", fmt.Errorf("marshal legacy git for session %q: %w", row.ID, err)
	}
	metadata := map[string]any{
		"legacy_cache":   row.Data,
		"legacy_cutover": map[string]any{"key": legacyCutoverKey, "archive_table": archiveSessionsTable},
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal legacy metadata for session %q: %w", row.ID, err)
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO public.captain_sessions (
		id, provider_session_id, source, provider, host_id,
		parent_session_id, root_session_id, path, project, cwd, title,
		initial_prompt, slug, agent_type, description, cli_version,
		lifecycle_status, activity_state, health_state, state_reason,
		state_version, state_observed_at, git, metadata, started_at,
		ended_at, last_activity_at, created_at, updated_at
	) VALUES (
		$1, $2, $3, $4, 'local',
		$5, $6, $7, $8, $9, $10,
		$11, $12, $13, $14, $15,
		$16, 'idle', 'healthy', $17,
		0, $18, $19::jsonb, $20::jsonb, $21,
		$22, $23, $24, $25
	) ON CONFLICT (id) DO NOTHING`,
		row.NativeID, row.ID, row.Source, provider,
		parentID, rootID, row.Path, nullableJSONText(row.Data, "project"), nullableJSONText(row.Data, "cwd"), nullableJSONText(row.Data, "title"),
		nullableJSONText(row.Data, "initial_prompt"), nullableJSONText(row.Data, "slug"), nullableJSONText(row.Data, "agent_type"), nullableJSONText(row.Data, "agent_desc"), nestedNullableText(row.Data, "provider", "version"),
		lifecycle, legacyCutoverStateNote, observedAt, string(gitJSON), string(metadataJSON), row.StartedAt,
		row.EndedAt, observedAt, createdAt, updatedAt,
	)
	if err != nil {
		return "", fmt.Errorf("backfill native Captain session %q: %w", row.ID, err)
	}

	actual, err := validatePersistedNativeRow(ctx, tx, "captain_sessions", row.NativeID, map[string]any{
		"id": row.NativeID, "provider_session_id": row.ID, "source": row.Source,
		"provider": provider, "host_id": "local", "parent_session_id": parentID,
		"root_session_id": rootID, "path": row.Path,
		"project": nullableJSONText(row.Data, "project"), "cwd": nullableJSONText(row.Data, "cwd"),
		"title": nullableJSONText(row.Data, "title"), "initial_prompt": nullableJSONText(row.Data, "initial_prompt"),
		"slug": nullableJSONText(row.Data, "slug"), "agent_type": nullableJSONText(row.Data, "agent_type"),
		"description":      nullableJSONText(row.Data, "agent_desc"),
		"cli_version":      nestedNullableText(row.Data, "provider", "version"),
		"lifecycle_status": lifecycle, "activity_state": "idle", "health_state": "healthy",
		"state_reason": legacyCutoverStateNote, "state_version": int64(0), "state_observed_at": observedAt,
		"git": git, "metadata": metadata, "started_at": row.StartedAt, "ended_at": row.EndedAt,
		"last_activity_at": observedAt, "created_at": createdAt, "updated_at": updatedAt,
	})
	if err != nil {
		return "", fmt.Errorf("native Captain session %s conflicts with legacy identity %q: %w", row.NativeID, row.ID, err)
	}
	return actual, nil
}

func upsertAndValidateNativePromptRun(ctx context.Context, tx *sql.Tx, row *legacyPromptRow, sessions []*legacySessionRow) (string, error) {
	byID := make(map[string]*legacySessionRow, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	session := byID[row.SessionID]
	rootID := session.NativeID
	if session.Root != nil {
		rootID = session.Root.NativeID
	}

	rendered := row.Data["realized"]
	if _, ok := rendered.(map[string]any); !ok {
		rendered = map[string]any{}
	}
	renderedMap := rendered.(map[string]any)
	renderedMap["x-legacy-cache"] = map[string]any{
		"runId": row.RunID, "model": jsonString(row.Data, "model"), "backend": jsonString(row.Data, "backend"),
	}
	renderedJSON, err := json.Marshal(renderedMap)
	if err != nil {
		return "", fmt.Errorf("marshal legacy realized prompt for session %q: %w", row.SessionID, err)
	}
	createdAt := jsonTime(row.Data, "created_at")
	if createdAt == nil {
		createdAt = firstTimePointer(session.UpdatedAt, session.EndedAt, session.StartedAt)
	}
	if createdAt == nil {
		now := time.Now().UTC()
		createdAt = &now
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO public.captain_prompt_runs (
		id, session_id, root_session_id, origin, rendered_spec, prompt_markdown,
		phase, state, current_iteration, queued_at, started_at, finished_at, created_at, updated_at
	) VALUES (
		$1, $2, $3, 'legacy-session-cache', $4::jsonb, $5,
		'finished', 'succeeded', 0, $6, $6, $6, $6, $6
	) ON CONFLICT (id) DO NOTHING`,
		row.NativeID, session.NativeID, rootID, string(renderedJSON), promptMarkdown(renderedMap), *createdAt,
	)
	if err != nil {
		return "", fmt.Errorf("backfill native Captain prompt run %q: %w", row.RunID, err)
	}

	actual, err := validatePersistedNativeRow(ctx, tx, "captain_prompt_runs", row.NativeID, map[string]any{
		"id": row.NativeID, "session_id": session.NativeID, "root_session_id": rootID,
		"batch_id": nil, "parent_run_id": nil, "input_plan_id": nil, "input_plan_revision_id": nil,
		"origin": "legacy-session-cache", "spec_profile": nil, "admission_key": nil,
		"rendered_spec": renderedMap, "prompt_markdown": promptMarkdown(renderedMap),
		"verification_markdown": nil, "phase": "finished", "state": "succeeded",
		"current_iteration": 0, "result_text": nil, "result_json": nil, "error": nil,
		"version": int64(0), "queued_at": createdAt, "started_at": createdAt,
		"finished_at": createdAt, "created_at": createdAt, "updated_at": createdAt,
	})
	if err != nil {
		return "", fmt.Errorf("native Captain prompt run %s conflicts with legacy run %q: %w", row.NativeID, row.RunID, err)
	}
	return actual, nil
}

func setLegacyBackfillEmitTriggers(ctx context.Context, tx *sql.Tx, enabled bool) error {
	value := "on"
	if enabled {
		value = "off"
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('captain.suppress_session_change', $1, true)`, value); err != nil {
		return fmt.Errorf("configure Captain legacy backfill emit suppression: %w", err)
	}
	return nil
}

func validatePersistedNativeRow(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	id uuid.UUID,
	expected map[string]any,
) (string, error) {
	if !allowedNativeCutoverRelation(table) {
		return "", fmt.Errorf("unsupported native Captain cutover relation %q", table)
	}
	raw, err := readPersistedNativeRow(ctx, tx, table, id)
	if err != nil {
		return "", err
	}
	actual, err := decodeJSONMap(raw)
	if err != nil {
		return "", fmt.Errorf("decode persisted public.%s row %s: %w", table, id, err)
	}
	actual = normalizePersistedRow(actual)
	expected = normalizePersistedRow(expected)

	keys := make(map[string]struct{}, len(actual)+len(expected))
	for key := range actual {
		keys[key] = struct{}{}
	}
	for key := range expected {
		keys[key] = struct{}{}
	}
	var mismatches []string
	for key := range keys {
		actualValue, actualExists := actual[key]
		expectedValue, expectedExists := expected[key]
		if !actualExists || !expectedExists || canonicalJSONValue(actualValue) != canonicalJSONValue(expectedValue) {
			mismatches = append(mismatches, key)
		}
	}
	if len(mismatches) != 0 {
		sort.Strings(mismatches)
		return "", fmt.Errorf("persisted columns differ from archive-derived values: %s", strings.Join(mismatches, ", "))
	}
	return string(raw), nil
}

func readPersistedNativeRow(ctx context.Context, tx *sql.Tx, table string, id uuid.UUID) ([]byte, error) {
	if !allowedNativeCutoverRelation(table) {
		return nil, fmt.Errorf("unsupported native Captain cutover relation %q", table)
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT to_jsonb(row_value)::text FROM public.%s AS row_value WHERE id = $1`, table,
	), id).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read persisted public.%s row %s: %w", table, id, err)
	}
	return raw, nil
}

func allowedNativeCutoverRelation(table string) bool {
	switch table {
	case "captain_sessions", "captain_session_sources", "captain_prompt_runs":
		return true
	default:
		return false
	}
}

func normalizePersistedRow(row map[string]any) map[string]any {
	normalized := make(map[string]any, len(row))
	for key, value := range row {
		normalized[key] = normalizePersistedValue(key, value)
	}
	return normalized
}

func normalizePersistedValue(key string, value any) any {
	switch typed := value.(type) {
	case uuid.UUID:
		return typed.String()
	case *uuid.UUID:
		if typed == nil {
			return nil
		}
		return typed.String()
	case time.Time:
		return canonicalCutoverTimestamp(typed)
	case *time.Time:
		if typed == nil {
			return nil
		}
		return canonicalCutoverTimestamp(*typed)
	case string:
		if isCutoverTimestampColumn(key) {
			if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
				return canonicalCutoverTimestamp(parsed)
			}
		}
	}
	return value
}

func canonicalCutoverTimestamp(value time.Time) string {
	// PostgreSQL timestamptz stores microseconds by truncating sub-microsecond
	// precision. Mirror that behavior when comparing archive-derived values to
	// rows read back from PostgreSQL; rounding can advance the expected value by
	// one microsecond and reject an otherwise exact persisted row.
	return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

func isCutoverTimestampColumn(column string) bool {
	switch column {
	case "state_observed_at", "started_at", "ended_at", "last_activity_at", "created_at", "updated_at",
		"observed_mod_time", "queued_at", "finished_at":
		return true
	default:
		return false
	}
}

func canonicalJSONValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<unencodable:%T:%v>", value, err)
	}
	return string(encoded)
}

func loadLegacySessions(ctx context.Context, query queryer, table string) ([]*legacySessionRow, []json.RawMessage, error) {
	raw, err := loadJSONRows(ctx, query, table, "path")
	if err != nil {
		return nil, nil, err
	}
	rows := make([]*legacySessionRow, 0, len(raw))
	for _, value := range raw {
		data, err := decodeJSONMap(value)
		if err != nil {
			return nil, nil, fmt.Errorf("decode public.%s row: %w", table, err)
		}
		rows = append(rows, &legacySessionRow{
			Raw: value, Data: data, Path: jsonString(data, "path"), ID: jsonString(data, "id"),
			ParentID: jsonString(data, "parent_id"), Source: jsonString(data, "source"),
			StartedAt: jsonTime(data, "started_at"), EndedAt: jsonTime(data, "ended_at"), UpdatedAt: jsonTime(data, "updated_at"),
		})
	}
	return rows, raw, nil
}

func loadLegacyPromptsIfPresent(ctx context.Context, query queryer, exists bool, table string) ([]*legacyPromptRow, []json.RawMessage, error) {
	if !exists {
		return nil, nil, nil
	}
	raw, err := loadJSONRows(ctx, query, table, "session_id")
	if err != nil {
		return nil, nil, err
	}
	rows := make([]*legacyPromptRow, 0, len(raw))
	for _, value := range raw {
		data, err := decodeJSONMap(value)
		if err != nil {
			return nil, nil, fmt.Errorf("decode public.%s row: %w", table, err)
		}
		rows = append(rows, &legacyPromptRow{
			Raw: value, Data: data, SessionID: jsonString(data, "session_id"), RunID: jsonString(data, "run_id"),
		})
	}
	return rows, raw, nil
}

func validateLegacyDataset(sessions []*legacySessionRow, prompts []*legacyPromptRow) error {
	byID := make(map[string]*legacySessionRow, len(sessions))
	byNativeID := make(map[uuid.UUID]string, len(sessions))
	paths := make(map[string]struct{}, len(sessions))
	for _, row := range sessions {
		if row.Path == "" || row.ID == "" || row.Source == "" {
			return errors.New("each legacy session requires non-empty path, id, and source")
		}
		if _, exists := paths[row.Path]; exists {
			return fmt.Errorf("duplicate legacy session path %q", row.Path)
		}
		paths[row.Path] = struct{}{}
		if _, exists := byID[row.ID]; exists {
			return fmt.Errorf("duplicate legacy session id %q", row.ID)
		}
		byID[row.ID] = row
		row.NativeID = legacyNativeSessionID(row.ID)
		if previous, exists := byNativeID[row.NativeID]; exists {
			return fmt.Errorf("legacy session UUID mapping collision between %q and %q", previous, row.ID)
		}
		byNativeID[row.NativeID] = row.ID
		if row.StartedAt != nil && row.EndedAt != nil && row.EndedAt.Before(*row.StartedAt) {
			return fmt.Errorf("legacy session %q ends before it starts", row.ID)
		}
	}
	for _, row := range sessions {
		if row.ParentID == "" {
			row.Root = row
			continue
		}
		parent := byID[row.ParentID]
		if parent == nil {
			return fmt.Errorf("legacy session %q references missing parent %q", row.ID, row.ParentID)
		}
		if parent.Source != row.Source {
			return fmt.Errorf("legacy session %q source %q differs from parent %q source %q", row.ID, row.Source, parent.ID, parent.Source)
		}
		row.Parent = parent
	}

	visiting := map[string]bool{}
	visited := map[string]bool{}
	var resolve func(*legacySessionRow) error
	resolve = func(row *legacySessionRow) error {
		if visited[row.ID] {
			return nil
		}
		if visiting[row.ID] {
			return fmt.Errorf("legacy session parent cycle includes %q", row.ID)
		}
		visiting[row.ID] = true
		if row.Parent == nil {
			row.Root, row.Depth = row, 0
		} else {
			if err := resolve(row.Parent); err != nil {
				return err
			}
			row.Root, row.Depth = row.Parent.Root, row.Parent.Depth+1
		}
		visiting[row.ID] = false
		visited[row.ID] = true
		return nil
	}
	for _, row := range sessions {
		if err := resolve(row); err != nil {
			return err
		}
	}

	promptIDs := make(map[uuid.UUID]string, len(prompts))
	for _, row := range prompts {
		if row.SessionID == "" {
			return errors.New("legacy prompt row has an empty session_id")
		}
		if byID[row.SessionID] == nil {
			return fmt.Errorf("legacy prompt for session %q has no archived session", row.SessionID)
		}
		row.NativeID = legacyNativePromptID(row.SessionID, row.RunID)
		if previous, exists := promptIDs[row.NativeID]; exists {
			return fmt.Errorf("legacy prompt run identity collision between %q and %q", previous, row.RunID)
		}
		promptIDs[row.NativeID] = row.RunID
	}
	return nil
}

func legacyNativeSessionID(id string) uuid.UUID {
	if parsed, err := uuid.Parse(strings.TrimSpace(id)); err == nil {
		return parsed
	}
	return uuid.NewSHA1(legacySessionNamespace, []byte(strings.TrimSpace(id)))
}

func legacyNativePromptID(sessionID, runID string) uuid.UUID {
	if parsed, err := uuid.Parse(strings.TrimSpace(runID)); err == nil {
		return parsed
	}
	return uuid.NewSHA1(legacyPromptNamespace, []byte(strings.TrimSpace(sessionID)+"\x00"+strings.TrimSpace(runID)))
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadJSONRows(ctx context.Context, query queryer, table, orderColumn string) ([]json.RawMessage, error) {
	if !allowedLegacyRelation(table) || (orderColumn != "path" && orderColumn != "session_id") {
		return nil, fmt.Errorf("unsupported Captain legacy relation %q", table)
	}
	rows, err := query.QueryContext(ctx, fmt.Sprintf(`SELECT to_jsonb(t)::text
		FROM public.%s t ORDER BY %s`, table, orderColumn))
	if err != nil {
		return nil, fmt.Errorf("read public.%s: %w", table, err)
	}
	defer rows.Close()
	var result []json.RawMessage
	for rows.Next() {
		var value []byte
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan public.%s row: %w", table, err)
		}
		result = append(result, append(json.RawMessage(nil), value...))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read public.%s rows: %w", table, err)
	}
	return result, nil
}

func relationExists(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) (bool, error) {
	if !allowedLegacyRelation(table) {
		return false, fmt.Errorf("unsupported Captain relation %q", table)
	}
	var exists bool
	if err := query.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect public.%s: %w", table, err)
	}
	return exists, nil
}

func relationColumns(ctx context.Context, query queryer, table string) (map[string]string, error) {
	if !allowedLegacyRelation(table) {
		return nil, fmt.Errorf("unsupported Captain relation %q", table)
	}
	rows, err := query.QueryContext(ctx, `SELECT column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect public.%s columns: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]string{}
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return nil, fmt.Errorf("scan public.%s columns: %w", table, err)
		}
		columns[name] = dataType
	}
	return columns, rows.Err()
}

func allowedLegacyRelation(table string) bool {
	switch table {
	case legacySessionsTable, legacyPromptsTable, archiveSessionsTable, archivePromptsTable:
		return true
	default:
		return false
	}
}

func storedCutoverExists(ctx context.Context, db *sql.DB) (bool, error) {
	_, exists, err := readCutoverReport(ctx, db)
	return exists, err
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readCutoverReport(ctx context.Context, query rowQueryer) (*LegacySessionCutoverReport, bool, error) {
	var report LegacySessionCutoverReport
	var promptsTable, promptsChecksum, nativePromptsChecksum sql.NullString
	var details []byte
	err := query.QueryRowContext(ctx, `SELECT
		cutover_key, legacy_sessions_table, legacy_prompts_table,
		legacy_session_rows, legacy_prompt_rows, imported_session_rows, imported_prompt_run_rows,
		legacy_sessions_checksum, legacy_prompts_checksum, native_sessions_checksum, native_prompt_runs_checksum,
		details, started_at, completed_at, updated_at
		FROM public.captain_legacy_session_cutovers WHERE cutover_key = $1`, legacyCutoverKey).Scan(
		&report.CutoverKey, &report.LegacySessionsTable, &promptsTable,
		&report.LegacySessionRows, &report.LegacyPromptRows, &report.ImportedSessionRows, &report.ImportedPromptRunRows,
		&report.LegacySessionsChecksum, &promptsChecksum, &report.NativeSessionsChecksum, &nativePromptsChecksum,
		&details, &report.StartedAt, &report.CompletedAt, &report.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read Captain legacy cutover report: %w", err)
	}
	report.LegacyPromptsTable = nullableStringPointer(promptsTable)
	report.LegacyPromptsChecksum = nullableStringPointer(promptsChecksum)
	report.NativePromptRunsChecksum = nullableStringPointer(nativePromptsChecksum)
	report.Details = append(json.RawMessage(nil), details...)
	report.StartedAt = report.StartedAt.UTC()
	report.CompletedAt = report.CompletedAt.UTC()
	report.UpdatedAt = report.UpdatedAt.UTC()
	return &report, true, nil
}

func sameCutoverValidation(left, right *LegacySessionCutoverReport) bool {
	if left == nil || right == nil {
		return false
	}
	return left.CutoverKey == right.CutoverKey &&
		left.LegacySessionsTable == right.LegacySessionsTable &&
		equalOptionalString(left.LegacyPromptsTable, right.LegacyPromptsTable) &&
		left.LegacySessionRows == right.LegacySessionRows &&
		left.LegacyPromptRows == right.LegacyPromptRows &&
		left.ImportedSessionRows == right.ImportedSessionRows &&
		left.ImportedPromptRunRows == right.ImportedPromptRunRows &&
		left.LegacySessionsChecksum == right.LegacySessionsChecksum &&
		equalOptionalString(left.LegacyPromptsChecksum, right.LegacyPromptsChecksum) &&
		left.NativeSessionsChecksum == right.NativeSessionsChecksum &&
		equalOptionalString(left.NativePromptRunsChecksum, right.NativePromptRunsChecksum) &&
		equalJSON(left.Details, right.Details)
}

func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(strings.NewReader(string(left)))
	leftDecoder.UseNumber()
	if err := leftDecoder.Decode(&leftValue); err != nil {
		return false
	}
	rightDecoder := json.NewDecoder(strings.NewReader(string(right)))
	rightDecoder.UseNumber()
	if err := rightDecoder.Decode(&rightValue); err != nil {
		return false
	}
	leftCanonical, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightValue)
	return err == nil && string(leftCanonical) == string(rightCanonical)
}

func insertCutoverReport(ctx context.Context, tx *sql.Tx, report *LegacySessionCutoverReport) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO public.captain_legacy_session_cutovers (
		cutover_key, legacy_sessions_table, legacy_prompts_table,
		legacy_session_rows, legacy_prompt_rows, imported_session_rows, imported_prompt_run_rows,
		legacy_sessions_checksum, legacy_prompts_checksum, native_sessions_checksum, native_prompt_runs_checksum,
		details, started_at, completed_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15)`,
		report.CutoverKey, report.LegacySessionsTable, report.LegacyPromptsTable,
		report.LegacySessionRows, report.LegacyPromptRows, report.ImportedSessionRows, report.ImportedPromptRunRows,
		report.LegacySessionsChecksum, report.LegacyPromptsChecksum, report.NativeSessionsChecksum, report.NativePromptRunsChecksum,
		string(report.Details), report.StartedAt, report.CompletedAt, report.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("store Captain legacy cutover report: %w", err)
	}
	return nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func checksumJSONRows(rows []json.RawMessage) string {
	values := make([]string, len(rows))
	for i := range rows {
		values[i] = string(rows[i])
	}
	return checksumStrings(values)
}

func checksumStrings(values []string) string {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(value))
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func checksumColumns(columns map[string]string) string {
	values := make([]string, 0, len(columns))
	for name, dataType := range columns {
		values = append(values, name+"\x00"+dataType)
	}
	sort.Strings(values)
	return checksumStrings(values)
}

func stringsToRaw(values []string) []json.RawMessage {
	result := make([]json.RawMessage, len(values))
	for i := range values {
		result[i] = json.RawMessage(values[i])
	}
	return result
}

func optionalChecksum(present bool, rows []json.RawMessage) *string {
	if !present {
		return nil
	}
	value := checksumJSONRows(rows)
	return &value
}

func optionalArchiveName(present bool) string {
	if present {
		return archivePromptsTable
	}
	return ""
}

func optionalStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func jsonString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

func jsonInt64(data map[string]any, key string) int64 {
	switch value := data[key].(type) {
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case float64:
		return int64(value)
	case int64:
		return value
	default:
		return 0
	}
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func nestedString(data map[string]any, outer, inner string) string {
	nested, _ := data[outer].(map[string]any)
	return jsonString(nested, inner)
}

func legacyProvider(row *legacySessionRow) string {
	if provider := nestedString(row.Data, "provider", "name"); provider != "" {
		return provider
	}
	switch row.Source {
	case "codex":
		return "openai"
	case "claude":
		return "anthropic"
	default:
		return row.Source
	}
}

func nullableJSONText(data map[string]any, key string) any {
	if value := jsonString(data, key); value != "" {
		return value
	}
	return nil
}

func nestedNullableText(data map[string]any, outer, inner string) any {
	if value := nestedString(data, outer, inner); value != "" {
		return value
	}
	return nil
}

func jsonTime(data map[string]any, key string) *time.Time {
	value := jsonString(data, key)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func firstTime(values ...*time.Time) time.Time {
	if value := firstTimePointer(values...); value != nil {
		return *value
	}
	return time.Now().UTC()
}

func firstTimePointer(values ...*time.Time) *time.Time {
	for _, value := range values {
		if value != nil {
			copy := value.UTC()
			return &copy
		}
	}
	return nil
}

func promptMarkdown(rendered map[string]any) any {
	if input, ok := rendered["input"].(map[string]any); ok {
		if prompt, ok := input["prompt"].(map[string]any); ok {
			if value := jsonString(prompt, "user"); value != "" {
				return value
			}
		}
	}
	if value := jsonString(rendered, "user"); value != "" {
		return value
	}
	return nil
}

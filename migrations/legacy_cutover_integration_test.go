package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyWithLegacySessionCutoverPreservesAndBackfillsHistoricalCache(t *testing.T) {
	dsn := legacyCutoverTestDSN(t)
	ctx := t.Context()
	db, err := commonsdb.NewDB(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	require.NoError(t, createHistoricalLegacySessionCache(ctx, db))
	require.NoError(t, createUnrelatedSentinel(ctx, db))

	legacySessions := readJSONRows(t, ctx, db, legacySessionsTable, "path")
	legacyPrompts := readJSONRows(t, ctx, db, legacyPromptsTable, "session_id")
	legacySessionColumns := readColumnSignatures(t, ctx, db, legacySessionsTable)
	legacyPromptColumns := readColumnSignatures(t, ctx, db, legacyPromptsTable)
	assert.Equal(t, []string{"path"}, readPrimaryKeyColumns(t, ctx, db, legacySessionsTable))
	assert.Equal(t, []string{"session_id"}, readPrimaryKeyColumns(t, ctx, db, legacyPromptsTable))

	first, err := ApplyWithLegacySessionCutover(ctx, dsn)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, legacyCutoverKey, first.CutoverKey)
	assert.Equal(t, archiveSessionsTable, first.LegacySessionsTable)
	require.NotNil(t, first.LegacyPromptsTable)
	assert.Equal(t, archivePromptsTable, *first.LegacyPromptsTable)
	assert.EqualValues(t, 2, first.LegacySessionRows)
	assert.EqualValues(t, 1, first.LegacyPromptRows)
	assert.Equal(t, first.LegacySessionRows, first.ImportedSessionRows)
	assert.Equal(t, first.LegacyPromptRows, first.ImportedPromptRunRows)
	assert.Equal(t, checksumStrings(legacySessions), first.LegacySessionsChecksum)
	promptChecksum := checksumStrings(legacyPrompts)
	require.NotNil(t, first.LegacyPromptsChecksum)
	assert.Equal(t, promptChecksum, *first.LegacyPromptsChecksum)
	assert.NotEmpty(t, first.NativeSessionsChecksum)
	require.NotNil(t, first.NativePromptRunsChecksum)
	assert.NotEmpty(t, *first.NativePromptRunsChecksum)
	actualNativeSessions := readJSONRows(t, ctx, db, "captain_sessions", "id")
	actualNativeSources := readJSONRows(t, ctx, db, "captain_session_sources", "id")
	actualNativeSessionState := make([]string, 0, len(actualNativeSessions)+len(actualNativeSources))
	for _, row := range actualNativeSessions {
		actualNativeSessionState = append(actualNativeSessionState, "session\x00"+row)
	}
	for _, row := range actualNativeSources {
		actualNativeSessionState = append(actualNativeSessionState, "source\x00"+row)
	}
	sort.Strings(actualNativeSessionState)
	assert.Equal(t, checksumStrings(actualNativeSessionState), first.NativeSessionsChecksum)
	actualNativePrompts := readJSONRows(t, ctx, db, "captain_prompt_runs", "id")
	sort.Strings(actualNativePrompts)
	assert.Equal(t, checksumStrings(actualNativePrompts), *first.NativePromptRunsChecksum)

	details, err := decodeJSONMap(first.Details)
	require.NoError(t, err)
	mappingLedger, ok := details["mappingLedger"].(map[string]any)
	require.True(t, ok)
	sessionMappings, ok := mappingLedger["sessions"].([]any)
	require.True(t, ok)
	require.Len(t, sessionMappings, 2)
	promptMappings, ok := mappingLedger["prompts"].([]any)
	require.True(t, ok)
	require.Len(t, promptMappings, 1)
	rootMapping, ok := sessionMappings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "71d6e735-e02c-46ef-a1bd-e4aff85a27fe", rootMapping["legacyId"])
	assert.Equal(t, "71d6e735-e02c-46ef-a1bd-e4aff85a27fe", rootMapping["nativeId"])
	childMapping, ok := sessionMappings[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "opaque-child-session", childMapping["legacyId"])
	assert.Equal(t, legacyNativeSessionID("opaque-child-session").String(), childMapping["nativeId"])
	assert.Equal(t, "71d6e735-e02c-46ef-a1bd-e4aff85a27fe", childMapping["parentNativeId"])
	assert.Equal(t, "71d6e735-e02c-46ef-a1bd-e4aff85a27fe", childMapping["rootNativeId"])
	promptMapping, ok := promptMappings[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, legacyNativePromptID("opaque-child-session", "opaque-prompt-run").String(), promptMapping["nativePromptRunId"])

	schemaFingerprints, ok := details["schemaFingerprints"].(map[string]any)
	require.True(t, ok)
	archiveSessionColumnTypes, err := relationColumns(ctx, db, archiveSessionsTable)
	require.NoError(t, err)
	archivePromptColumnTypes, err := relationColumns(ctx, db, archivePromptsTable)
	require.NoError(t, err)
	assert.Equal(t, checksumColumns(archiveSessionColumnTypes), schemaFingerprints["sessions"])
	assert.Equal(t, checksumColumns(archivePromptColumnTypes), schemaFingerprints["prompts"])
	warnings, ok := details["warnings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "1", fmt.Sprint(warnings["providerFallbackRows"]))
	assert.Equal(t, "0", fmt.Sprint(warnings["legacyPlanRows"]))
	assert.NotEmpty(t, warnings["legacyPlanHandling"])
	assert.NotEmpty(t, warnings["summaryOnly"])
	nativeSessionSources, ok := details["nativeSessionSources"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "2", fmt.Sprint(nativeSessionSources["rows"]))
	sort.Strings(actualNativeSources)
	assert.Equal(t, checksumStrings(actualNativeSources), nativeSessionSources["checksum"])
	var durableDetails []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT details
		FROM public.captain_legacy_session_cutovers WHERE cutover_key = $1`, legacyCutoverKey).Scan(&durableDetails))
	assert.JSONEq(t, string(first.Details), string(durableDetails))

	assert.False(t, relationExistsForTest(t, ctx, db, legacyPromptsTable))
	assert.Equal(t, legacySessions, readJSONRows(t, ctx, db, archiveSessionsTable, "path"))
	assert.Equal(t, legacyPrompts, readJSONRows(t, ctx, db, archivePromptsTable, "session_id"))
	assert.Equal(t, legacySessionColumns, readColumnSignatures(t, ctx, db, archiveSessionsTable))
	assert.Equal(t, legacyPromptColumns, readColumnSignatures(t, ctx, db, archivePromptsTable))
	assert.Equal(t, []string{"path"}, readPrimaryKeyColumns(t, ctx, db, archiveSessionsTable))
	assert.Equal(t, []string{"session_id"}, readPrimaryKeyColumns(t, ctx, db, archivePromptsTable))
	assert.False(t, columnExistsForTest(t, ctx, db, archiveSessionsTable, "summary_version"))
	assert.False(t, columnExistsForTest(t, ctx, db, archiveSessionsTable, "title"))
	assert.False(t, columnExistsForTest(t, ctx, db, archiveSessionsTable, "initial_prompt"))

	rootProviderID := "71d6e735-e02c-46ef-a1bd-e4aff85a27fe"
	rootNativeID := uuid.MustParse(rootProviderID)
	childProviderID := "opaque-child-session"
	childNativeID := legacyNativeSessionID(childProviderID)

	root := readNativeSession(t, ctx, db, rootNativeID)
	assert.Equal(t, rootProviderID, root.ProviderSessionID)
	assert.Equal(t, "codex", root.Source)
	assert.Equal(t, "openai", root.Provider)
	assert.Equal(t, "local", root.HostID)
	assert.Equal(t, "/legacy/root.jsonl", root.Path)
	assert.False(t, root.ParentID.Valid)
	assert.False(t, root.RootID.Valid)
	assert.Equal(t, "succeeded", root.LifecycleStatus)
	assert.Equal(t, legacyCutoverStateNote, root.StateReason)
	assert.Equal(t, rootProviderID, nestedMetadataString(t, root.Metadata, "legacy_cache", "id"))
	assert.Equal(t, "captain-session-cache-v1", nestedMetadataString(t, root.Metadata, "legacy_cutover", "key"))

	child := readNativeSession(t, ctx, db, childNativeID)
	assert.Equal(t, childProviderID, child.ProviderSessionID)
	assert.True(t, child.ParentID.Valid)
	assert.Equal(t, rootNativeID, child.ParentID.UUID)
	assert.True(t, child.RootID.Valid)
	assert.Equal(t, rootNativeID, child.RootID.UUID)
	assert.Equal(t, "openai", child.Provider)
	assert.Equal(t, "database child", nestedMetadataString(t, child.Metadata, "legacy_cache", "agent_desc"))
	assert.Equal(t, "legacy-model", nestedMetadataString(t, child.Metadata, "legacy_cache", "model"))

	sources := readNativeSessionSources(t, ctx, db)
	require.Len(t, sources, 2)
	rootSource := sources["/legacy/root.jsonl"]
	assert.Equal(t, uuid.NewSHA1(legacySourceNamespace, []byte(rootSource.Path)), rootSource.ID)
	assert.Equal(t, rootNativeID, rootSource.SessionID)
	assert.Equal(t, "codex", rootSource.SourceKind)
	assert.Equal(t, rootProviderID, rootSource.SourceIdentity)
	assert.Equal(t, 1, rootSource.ParserVersion)
	assert.Equal(t, int64(101), rootSource.ByteOffset)
	assert.Equal(t, int64(101), rootSource.ObservedSize)
	assert.True(t, time.Unix(0, 1_000_000_501).UTC().Truncate(time.Microsecond).Equal(rootSource.ObservedModTime))
	childSource := sources["/legacy/child.jsonl"]
	assert.Equal(t, uuid.NewSHA1(legacySourceNamespace, []byte(childSource.Path)), childSource.ID)
	assert.Equal(t, childNativeID, childSource.SessionID)
	assert.Equal(t, "codex", childSource.SourceKind)
	assert.Equal(t, childProviderID, childSource.SourceIdentity)
	assert.Equal(t, 1, childSource.ParserVersion)
	assert.Equal(t, int64(202), childSource.ByteOffset)
	assert.Equal(t, int64(202), childSource.ObservedSize)
	assert.True(t, time.Unix(0, 2_000_000_999).UTC().Truncate(time.Microsecond).Equal(childSource.ObservedModTime))

	promptNativeID := legacyNativePromptID(childProviderID, "opaque-prompt-run")
	var prompt struct {
		SessionID      uuid.UUID
		RootSessionID  uuid.UUID
		Origin         sql.NullString
		PromptMarkdown sql.NullString
		Phase          string
		State          string
		RenderedSpec   []byte
	}
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session_id, root_session_id, origin,
		prompt_markdown, phase, state, rendered_spec
		FROM public.captain_prompt_runs WHERE id = $1`, promptNativeID).Scan(
		&prompt.SessionID, &prompt.RootSessionID, &prompt.Origin, &prompt.PromptMarkdown,
		&prompt.Phase, &prompt.State, &prompt.RenderedSpec,
	))
	assert.Equal(t, childNativeID, prompt.SessionID)
	assert.Equal(t, rootNativeID, prompt.RootSessionID)
	assert.Equal(t, "legacy-session-cache", prompt.Origin.String)
	assert.Equal(t, "preserve this realized prompt", prompt.PromptMarkdown.String)
	assert.Equal(t, "finished", prompt.Phase)
	assert.Equal(t, "succeeded", prompt.State)
	var rendered map[string]any
	require.NoError(t, json.Unmarshal(prompt.RenderedSpec, &rendered))
	legacyPromptMetadata, ok := rendered["x-legacy-cache"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "opaque-prompt-run", legacyPromptMetadata["runId"])
	assert.Equal(t, "legacy-model", legacyPromptMetadata["model"])
	assert.Equal(t, "codex_cli", legacyPromptMetadata["backend"])

	second, err := ApplyWithLegacySessionCutover(ctx, dsn)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, first.LegacySessionsChecksum, second.LegacySessionsChecksum)
	assert.Equal(t, first.LegacyPromptsChecksum, second.LegacyPromptsChecksum)
	assert.Equal(t, first.NativeSessionsChecksum, second.NativeSessionsChecksum)
	assert.Equal(t, first.NativePromptRunsChecksum, second.NativePromptRunsChecksum)
	assert.True(t, first.StartedAt.Equal(second.StartedAt), "started_at changed: %s != %s", first.StartedAt, second.StartedAt)
	assert.True(t, first.CompletedAt.Equal(second.CompletedAt), "completed_at changed: %s != %s", first.CompletedAt, second.CompletedAt)
	assert.True(t, first.UpdatedAt.Equal(second.UpdatedAt), "updated_at changed: %s != %s", first.UpdatedAt, second.UpdatedAt)
	assert.JSONEq(t, string(first.Details), string(second.Details))
	require.NoError(t, Apply(ctx, dsn))

	assert.Equal(t, legacySessions, readJSONRows(t, ctx, db, archiveSessionsTable, "path"))
	assert.Equal(t, legacyPrompts, readJSONRows(t, ctx, db, archivePromptsTable, "session_id"))
	var sentinel string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM public.unrelated_gavel_data WHERE id = 1`).Scan(&sentinel))
	assert.Equal(t, "preserve unrelated data", sentinel)
	var sessionCount, promptCount, reportCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM public.captain_sessions`).Scan(&sessionCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM public.captain_prompt_runs`).Scan(&promptCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM public.captain_legacy_session_cutovers WHERE cutover_key = $1`, legacyCutoverKey).Scan(&reportCount))
	assert.Equal(t, 2, sessionCount)
	assert.Equal(t, 1, promptCount)
	assert.Equal(t, 1, reportCount)

	// Simulate a crash-resume database containing a conflicting row that owns
	// the deterministic native ID. The retry must reject every material prompt
	// mismatch instead of accepting the old identity-only columns.
	tamperTx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tamperTx.ExecContext(ctx, `SELECT set_config('captain.suppress_session_change', 'on', true)`)
	require.NoError(t, err)
	_, err = tamperTx.ExecContext(ctx, `UPDATE public.captain_prompt_runs
		SET rendered_spec = '{}'::jsonb WHERE id = $1`, promptNativeID)
	require.NoError(t, err)
	require.NoError(t, tamperTx.Commit())
	_, err = ApplyWithLegacySessionCutover(ctx, dsn)
	require.Error(t, err)
	assert.ErrorContains(t, err, "rendered_spec")
	storedAfterFailure, exists, readErr := readCutoverReport(ctx, db)
	require.NoError(t, readErr)
	require.True(t, exists)
	assert.True(t, second.StartedAt.Equal(storedAfterFailure.StartedAt))
	assert.True(t, second.CompletedAt.Equal(storedAfterFailure.CompletedAt))
	assert.True(t, second.UpdatedAt.Equal(storedAfterFailure.UpdatedAt))
}

func TestValidateLegacyDatasetRejectsUnsafeIdentityGraphs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sessions func() []*legacySessionRow
		prompts  func() []*legacyPromptRow
		want     string
	}{
		{
			name: "duplicate session id",
			sessions: func() []*legacySessionRow {
				return []*legacySessionRow{
					{Path: "/one", ID: "duplicate", Source: "codex"},
					{Path: "/two", ID: "duplicate", Source: "claude"},
				}
			},
			prompts: func() []*legacyPromptRow { return nil },
			want:    `duplicate legacy session id "duplicate"`,
		},
		{
			name: "native UUID mapping collision",
			sessions: func() []*legacySessionRow {
				opaque := "opaque"
				return []*legacySessionRow{
					{Path: "/opaque", ID: opaque, Source: "codex"},
					{Path: "/uuid", ID: legacyNativeSessionID(opaque).String(), Source: "codex"},
				}
			},
			prompts: func() []*legacyPromptRow { return nil },
			want:    "UUID mapping collision",
		},
		{
			name: "orphan parent",
			sessions: func() []*legacySessionRow {
				return []*legacySessionRow{{Path: "/child", ID: "child", ParentID: "missing", Source: "codex"}}
			},
			prompts: func() []*legacyPromptRow { return nil },
			want:    `references missing parent "missing"`,
		},
		{
			name: "parent cycle",
			sessions: func() []*legacySessionRow {
				return []*legacySessionRow{
					{Path: "/one", ID: "one", ParentID: "two", Source: "codex"},
					{Path: "/two", ID: "two", ParentID: "one", Source: "codex"},
				}
			},
			prompts: func() []*legacyPromptRow { return nil },
			want:    "parent cycle",
		},
		{
			name: "cross-source parent",
			sessions: func() []*legacySessionRow {
				return []*legacySessionRow{
					{Path: "/root", ID: "root", Source: "claude"},
					{Path: "/child", ID: "child", ParentID: "root", Source: "codex"},
				}
			},
			prompts: func() []*legacyPromptRow { return nil },
			want:    `source "codex" differs from parent "root" source "claude"`,
		},
		{
			name: "orphan prompt",
			sessions: func() []*legacySessionRow {
				return []*legacySessionRow{{Path: "/root", ID: "root", Source: "codex"}}
			},
			prompts: func() []*legacyPromptRow {
				return []*legacyPromptRow{{SessionID: "missing", RunID: "run"}}
			},
			want: `legacy prompt for session "missing" has no archived session`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateLegacyDataset(tt.sessions(), tt.prompts())
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

type cutoverColumnSignature struct {
	Name       string
	DataType   string
	Nullable   string
	DefaultSQL string
}

type cutoverNativeSession struct {
	ProviderSessionID string
	Source            string
	Provider          string
	HostID            string
	Path              string
	ParentID          uuid.NullUUID
	RootID            uuid.NullUUID
	LifecycleStatus   string
	StateReason       string
	Metadata          []byte
}

type cutoverNativeSessionSource struct {
	ID              uuid.UUID
	SessionID       uuid.UUID
	SourceKind      string
	Path            string
	SourceIdentity  string
	ParserVersion   int
	ByteOffset      int64
	ObservedSize    int64
	ObservedModTime time.Time
}

func legacyCutoverTestDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("CAPTAIN_DB_TEST_DSN"); dsn != "" {
		return dsn
	}
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres legacy cutover tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_legacy_cutover",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	return dsn
}

func createHistoricalLegacySessionCache(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE public.captain_sessions (
		path text PRIMARY KEY,
		id text,
		parent_id text,
		source text,
		is_agent boolean,
		agent_type text,
		agent_desc text,
		mod_unix bigint,
		size bigint,
		project text,
		cwd text,
		model text,
		git jsonb,
		provider jsonb,
		started_at timestamptz,
		ended_at timestamptz,
		cost jsonb,
		usage jsonb,
		files jsonb,
		approvals jsonb,
		tool_calls bigint,
		message_count bigint,
		context_tokens bigint,
		slug text,
		plan_path text,
		plan_slug text,
		plan jsonb,
		updated_at timestamptz
	);
	CREATE INDEX idx_captain_sessions_id ON public.captain_sessions(id);
	CREATE INDEX idx_captain_sessions_parent_id ON public.captain_sessions(parent_id);
	CREATE TABLE public.captain_session_prompts (
		session_id text PRIMARY KEY,
		run_id text,
		model text,
		backend text,
		realized jsonb,
		created_at timestamptz
	)`)
	if err != nil {
		return err
	}

	rootID := "71d6e735-e02c-46ef-a1bd-e4aff85a27fe"
	started := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	ended := started.Add(5 * time.Minute)
	updated := ended.Add(time.Second)
	_, err = db.ExecContext(ctx, `INSERT INTO public.captain_sessions (
		path, id, parent_id, source, is_agent, agent_type, agent_desc,
		mod_unix, size, project, cwd, model, git, provider, started_at, ended_at,
		cost, usage, files, approvals, tool_calls, message_count, context_tokens,
		slug, plan_path, plan_slug, plan, updated_at
	) VALUES
		($1,$2,NULL,'codex',false,'root','root session',1000000501,101,'gavel','/repo','codex-root',
		 '{"branch":"main"}'::jsonb,'{}'::jsonb,$3,$4,
		 '{"inputCost":1.25,"outputCost":0.5}'::jsonb,'{"inputTokens":100,"outputTokens":20}'::jsonb,
		 '{"read":["go.mod"]}'::jsonb,'{"approved":1,"denied":0}'::jsonb,2,4,120,'root-slug',NULL,NULL,NULL,$5),
		($6,$7,$2,'codex',true,'worker','database child',2000000999,202,'gavel','/repo','legacy-model',
		 '{"branch":"feature"}'::jsonb,'{"name":"openai","version":"1.2.3"}'::jsonb,$3,$4,
		 '{"inputCost":0.25,"outputCost":0.1}'::jsonb,'{"inputTokens":30,"outputTokens":10}'::jsonb,
		 '{}'::jsonb,'{"approved":0,"denied":1}'::jsonb,1,2,40,'child-slug',NULL,NULL,NULL,$5)`,
		"/legacy/root.jsonl", rootID, started, ended, updated,
		"/legacy/child.jsonl", "opaque-child-session",
	)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO public.captain_session_prompts
		(session_id, run_id, model, backend, realized, created_at)
		VALUES ($1, 'opaque-prompt-run', 'legacy-model', 'codex_cli',
		 '{"id":"legacy-prompt","name":"Legacy Prompt","input":{"prompt":{"user":"preserve this realized prompt"}}}'::jsonb,
		 $2)`, "opaque-child-session", updated)
	return err
}

func createUnrelatedSentinel(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE public.unrelated_gavel_data (
		id bigint PRIMARY KEY,
		payload text NOT NULL
	);
	INSERT INTO public.unrelated_gavel_data(id, payload) VALUES (1, 'preserve unrelated data')`)
	return err
}

func readJSONRows(t *testing.T, ctx context.Context, db *sql.DB, table, orderColumn string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT to_jsonb(row_value)::text
		FROM public.%s AS row_value ORDER BY %s`, table, orderColumn))
	require.NoError(t, err)
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	return values
}

func readColumnSignatures(t *testing.T, ctx context.Context, db *sql.DB, table string) []cutoverColumnSignature {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable,
		COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	require.NoError(t, err)
	defer rows.Close()
	var signatures []cutoverColumnSignature
	for rows.Next() {
		var signature cutoverColumnSignature
		require.NoError(t, rows.Scan(&signature.Name, &signature.DataType, &signature.Nullable, &signature.DefaultSQL))
		signatures = append(signatures, signature)
	}
	require.NoError(t, rows.Err())
	return signatures
}

func readPrimaryKeyColumns(t *testing.T, ctx context.Context, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT attribute.attname
		FROM pg_catalog.pg_index index
		JOIN pg_catalog.pg_class relation ON relation.oid = index.indrelid
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = relation.relnamespace
		JOIN LATERAL unnest(index.indkey) WITH ORDINALITY AS key(attnum, ordinal) ON true
		JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid = relation.oid AND attribute.attnum = key.attnum
		WHERE namespace.nspname = 'public' AND relation.relname = $1 AND index.indisprimary
		ORDER BY key.ordinal`, table)
	require.NoError(t, err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	return columns
}

func readNativeSession(t *testing.T, ctx context.Context, db *sql.DB, id uuid.UUID) cutoverNativeSession {
	t.Helper()
	var session cutoverNativeSession
	require.NoError(t, db.QueryRowContext(ctx, `SELECT provider_session_id, source, provider, host_id,
		path, parent_session_id, root_session_id, lifecycle_status, state_reason, metadata
		FROM public.captain_sessions WHERE id = $1`, id).Scan(
		&session.ProviderSessionID, &session.Source, &session.Provider, &session.HostID,
		&session.Path, &session.ParentID, &session.RootID, &session.LifecycleStatus,
		&session.StateReason, &session.Metadata,
	))
	return session
}

func readNativeSessionSources(t *testing.T, ctx context.Context, db *sql.DB) map[string]cutoverNativeSessionSource {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, source_kind, path, source_identity,
		parser_version, byte_offset, observed_size, observed_mod_time
		FROM public.captain_session_sources ORDER BY path`)
	require.NoError(t, err)
	defer rows.Close()
	sources := map[string]cutoverNativeSessionSource{}
	for rows.Next() {
		var source cutoverNativeSessionSource
		require.NoError(t, rows.Scan(
			&source.ID, &source.SessionID, &source.SourceKind, &source.Path, &source.SourceIdentity,
			&source.ParserVersion, &source.ByteOffset, &source.ObservedSize, &source.ObservedModTime,
		))
		sources[source.Path] = source
	}
	require.NoError(t, rows.Err())
	return sources
}

func nestedMetadataString(t *testing.T, raw []byte, object, key string) string {
	t.Helper()
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(raw, &metadata))
	nested, ok := metadata[object].(map[string]any)
	require.True(t, ok, "metadata object %q: %s", object, raw)
	value, ok := nested[key].(string)
	require.True(t, ok, "metadata string %q.%q: %s", object, key, raw)
	return value
}

func relationExistsForTest(t *testing.T, ctx context.Context, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
	return exists
}

func columnExistsForTest(t *testing.T, ctx context.Context, db *sql.DB, table, column string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
	)`, table, column).Scan(&exists))
	return exists
}

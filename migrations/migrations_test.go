package migrations

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

func TestSchemaBundleContainsGavelIntegrationContract(t *testing.T) {
	t.Parallel()

	expectedFiles := []string{
		"00_types.pg.hcl",
		"01_session_lifecycle_partial.sql",
		"10_sessions.pg.hcl",
		"20_prompt_runs_and_plans.pg.hcl",
		"21_plans.pg.hcl",
		"30_execution.pg.hcl",
		"31_execution_events.pg.hcl",
		"32_execution_approvals.pg.hcl",
		"35_git_agent.pg.hcl",
		"40_artifacts.pg.hcl",
		"41_runtime_profiles.pg.hcl",
		"50_constraints.sql",
		"51_state_triggers.sql",
		"52_session_activity_triggers.sql",
		"60_view_session_overview.sql",
		"61_view_session_transcript.sql",
		"62_view_session_turns.sql",
		"63_view_session_agents.sql",
		"65_view_session_plans.sql",
		"66_view_session_approvals.sql",
		"67_view_session_costs.sql",
		"68_view_session_events.sql",
		"69_view_prompt_run_overview.sql",
		"70_prompt_run_runtime.sql",
		"71_session_storage_params.sql",
		"72_ingest_storage_params.sql",
		"73_normalize_session_cwd.sql",
		"74_turn_request_approval_identity.sql",
		"75_model_call_provider_cost.sql",
		"76_model_call_cost_backfill.sql",
		"77_drop_session_files_view.sql",
	}
	for _, name := range expectedFiles {
		if _, err := fs.Stat(schemaFS, name); err != nil {
			t.Errorf("embedded migration %s: %v", name, err)
		}
	}
	assertContainsAll(t, "00_types.pg.hcl",
		`values = ["created", "running", "succeeded", "partial", "failed", "cancelled", "interrupted"]`,
		`enum "captain_git_agent_task_status"`,
		`enum "captain_git_agent_verdict_status"`,
		`enum "captain_spec_layer_scope"`,
		`values = ["global", "context", "surface", "user"]`,
	)
	assertContainsAll(t, "01_session_lifecycle_partial.sql",
		"-- phase: pre",
		"ADD VALUE IF NOT EXISTS 'partial' BEFORE 'failed'",
	)

	assertContainsAll(t, "10_sessions.pg.hcl",
		`table "captain_sessions"`,
		`column "id"`,
		`column "lifecycle_status"`,
		`column "activity_state"`,
		`column "health_state"`,
		`column "state_version"`,
	)
	assertContainsAll(t, "20_prompt_runs_and_plans.pg.hcl",
		`table "captain_prompt_runs"`,
		`column "session_id"`,
		`column "execution_session_id"`,
		`column "root_session_id"`,
		`column "phase"`,
		`column "state"`,
		`column "runtime"`,
		`table "captain_prompt_run_iterations"`,
		`column "prompt_run_id"`,
	)
	assertContainsAll(t, "21_plans.pg.hcl",
		`table "captain_plans"`,
		`column "approved_revision_id"`,
		`table "captain_plan_revisions"`,
		`column "plan_id"`,
	)
	assertContainsAll(t, "30_execution.pg.hcl",
		`table "captain_turns"`,
		`column "session_id"`,
		`table "captain_model_calls"`,
		`column "turn_id"`,
		`column "prompt_run_id"`,
		`column "iteration_id"`,
	)
	assertContainsAll(t, "31_execution_events.pg.hcl",
		`table "captain_messages"`,
		`table "captain_events"`,
	)
	assertContainsAll(t, "32_execution_approvals.pg.hcl",
		`table "captain_session_mcp_credentials"`,
		`column "secret_hash"`,
		`column "policy"`,
		`table "captain_turn_requests"`,
		`column "credential_id"`,
		`column "state"`,
		`column "version"`,
	)
	assertContainsAll(t, "35_git_agent.pg.hcl",
		`table "captain_git_agent_tasks"`,
		`column "mailbox"`,
		`column "prompt_run_id"`,
		`column "admission_key"`,
		`index "captain_git_agent_tasks_mailbox_task_key"`,
		`table "captain_git_agent_task_attempts"`,
		`column "tier"`,
		`index "captain_git_agent_task_attempts_task_attempt_tier_key"`,
	)
	// Presets and profiles are referenced by name from other profiles, prompt
	// frontmatter, and CLI flags, so the name index is case-insensitive; the
	// preset list is an ordered cross-source reference list, not a join table.
	assertContainsAll(t, "41_runtime_profiles.pg.hcl",
		`table "captain_runtime_presets"`,
		`column "scope"`,
		`type = enum.captain_spec_layer_scope`,
		`index "captain_runtime_presets_name_key"`,
		`expr = "lower(name)"`,
		`check "captain_runtime_presets_spec"`,
		`table "captain_runtime_profiles"`,
		`column "presets"`,
		`default = sql("'[]'::jsonb")`,
		`index "captain_runtime_profiles_name_key"`,
		`check "captain_runtime_profiles_presets"`,
	)
	assertContainsNone(t, "41_runtime_profiles.pg.hcl", `table "captain_runtime_profile_presets"`)
	assertContainsAll(t, "50_constraints.sql",
		"-- phase: post",
		"ALTER CONSTRAINT captain_prompt_runs_input_plan_id_fkey",
		"DEFERRABLE INITIALLY DEFERRED",
	)
	assertContainsAll(t, "51_state_triggers.sql",
		"-- phase: post",
		"CREATE OR REPLACE FUNCTION public.captain_set_session_state()",
		"CREATE TRIGGER captain_sessions_state_before",
		"CREATE TRIGGER captain_sessions_updated_at_before",
		"REVOKE ALL ON FUNCTION public.captain_sync_prompt_run_iteration() FROM PUBLIC;",
	)
	assertContainsAll(t, "52_session_activity_triggers.sql",
		"-- phase: post",
		"DROP TABLE IF EXISTS public.captain_outbox;",
		"DROP FUNCTION IF EXISTS public.captain_emit_session_change();",
		"DROP FUNCTION IF EXISTS public.captain_notify_outbox();",
		"CREATE OR REPLACE FUNCTION public.captain_touch_session_activity()",
		"CREATE TRIGGER captain_messages_activity_after",
		"REVOKE ALL ON FUNCTION public.captain_touch_session_activity() FROM PUBLIC;",
		// last_activity_at is agent-work only, and the write is monotonic. An
		// allowlist keeps a table that lacks an activity column (whose activity_at
		// falls through to updated_at = now) from permanently poisoning it.
		"SET last_activity_at = GREATEST(last_activity_at, agent_activity_at)",
		"AND (last_activity_at IS NULL OR last_activity_at < agent_activity_at)",
		"IF agent_activity_at IS NOT NULL AND TG_OP <> 'DELETE' AND TG_TABLE_NAME IN (",
	)
	// captain_sessions is heartbeat-updated, so it needs in-page room for HOT
	// updates and a far tighter autovacuum trigger than the defaults. Atlas OSS
	// cannot express storage parameters, so post-phase SQL owns them and the
	// HCL table definition must stay free of them.
	assertContainsAll(t, "71_session_storage_params.sql",
		"-- phase: post",
		"ALTER TABLE public.captain_sessions SET (",
		"fillfactor = 70",
		"autovacuum_vacuum_scale_factor = 0.02",
	)
	assertContainsNone(t, "10_sessions.pg.hcl", "fillfactor", "autovacuum_")
	// captain_messages keeps the dense default fillfactor because convergence
	// updates are exceptional and guarded. Its insert-driven vacuum keeps the
	// visibility map -- and therefore index-only scans -- intact. Turns and
	// model calls still need in-page room for genuinely changing aggregates.
	assertContainsAll(t, "72_ingest_storage_params.sql",
		"-- phase: post",
		"ALTER TABLE public.captain_messages SET (",
		"ALTER TABLE public.captain_turns SET (",
		"ALTER TABLE public.captain_model_calls SET (",
		"autovacuum_vacuum_insert_scale_factor = 0.02",
	)
	assertContainsNone(t, "72_ingest_storage_params.sql",
		// Exceptional convergence updates do not justify reserved in-page space.
		"ALTER TABLE public.captain_messages SET (\n  fillfactor",
	)
	assertContainsNone(t, "30_execution.pg.hcl", "fillfactor", "autovacuum_")
	assertContainsNone(t, "31_execution_events.pg.hcl", "fillfactor", "autovacuum_")
	assertContainsNone(t, "32_execution_approvals.pg.hcl", "fillfactor", "autovacuum_")
	assertContainsAll(t, "74_turn_request_approval_identity.sql",
		"-- phase: post",
		"ambiguous legacy tool approval identity",
		"DROP CONSTRAINT IF EXISTS captain_turn_requests_tool_approval_identity",
		"VALIDATE CONSTRAINT captain_turn_requests_tool_approval_identity",
	)
	for _, name := range expectedFiles {
		assertContainsNone(t, name,
			`table "captain_outbox"`,
			"INSERT INTO public.captain_outbox",
			"CREATE OR REPLACE FUNCTION public.captain_notify_outbox()",
			"CREATE TRIGGER captain_outbox_notify_after",
			"pg_notify('captain_outbox'",
		)
	}
	for file, view := range map[string]string{
		"60_view_session_overview.sql":    "captain_session_overview",
		"61_view_session_transcript.sql":  "captain_session_transcript",
		"62_view_session_turns.sql":       "captain_session_turns",
		"63_view_session_agents.sql":      "captain_session_agents",
		"65_view_session_plans.sql":       "captain_session_plans",
		"66_view_session_approvals.sql":   "captain_session_approvals",
		"67_view_session_costs.sql":       "captain_session_costs",
		"68_view_session_events.sql":      "captain_session_events",
		"69_view_prompt_run_overview.sql": "captain_prompt_run_overview",
	} {
		assertContainsAll(t, file,
			"-- phase: post",
			"CREATE OR REPLACE VIEW public."+view,
			"COMMENT ON VIEW public."+view,
		)
	}
	assertContainsAll(t, "70_prompt_run_runtime.sql",
		"-- phase: post",
		"UPDATE public.captain_prompt_runs",
		"NEW.runtime",
		"REVOKE ALL ON FUNCTION public.captain_set_prompt_run_state() FROM PUBLIC;",
	)
}

// Hash-gated run-once scripts keep steady-state applies free of DDL; views are
// restored via commons-db view-dependency invalidation, so no script may opt
// back into re-running on every apply.
func TestSchemaBundleHasNoAlwaysRunScripts(t *testing.T) {
	t.Parallel()

	entries, err := fs.Glob(schemaFS, "*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded SQL migrations found")
	}
	for _, name := range entries {
		data, err := fs.ReadFile(schemaFS, name)
		if err != nil {
			t.Fatalf("read embedded migration %s: %v", name, err)
		}
		if strings.Contains(string(data), "-- runs: always") {
			t.Errorf("%s declares '-- runs: always'; scripts must be hash-gated run-once", name)
		}
	}
}

func TestApplyRejectsEmptyConnection(t *testing.T) {
	t.Parallel()
	if err := Apply(t.Context(), "  "); err == nil {
		t.Fatal("Apply unexpectedly accepted an empty connection string")
	}
}

func TestApplyHoldsMigrationLockAcrossMigration(t *testing.T) {
	t.Parallel()

	var events []string
	err := apply(t.Context(), applyRequest{Connection: "postgres://captain", Schema: DefaultSchema}, applyDependencies{
		acquireLock: func(context.Context, applyRequest) (migrationLockHandle, error) {
			events = append(events, "lock")
			return &recordingMigrationLock{events: &events}, nil
		},
		migrate: func(context.Context, applyRequest) error {
			events = append(events, "migrate")
			return nil
		},
		verify: func(context.Context, applyRequest) error {
			events = append(events, "verify")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertEventsEqual(t, events, []string{"lock", "migrate", "verify", "unlock"})
}

func TestApplyReleasesMigrationLockOnVerificationFailure(t *testing.T) {
	t.Parallel()

	var events []string
	verificationErr := errors.New("constraint drifted")
	err := apply(t.Context(), applyRequest{Connection: "postgres://captain", Schema: DefaultSchema}, applyDependencies{
		acquireLock: func(context.Context, applyRequest) (migrationLockHandle, error) {
			events = append(events, "lock")
			return &recordingMigrationLock{events: &events}, nil
		},
		migrate: func(context.Context, applyRequest) error {
			events = append(events, "migrate")
			return nil
		},
		verify: func(context.Context, applyRequest) error {
			events = append(events, "verify")
			return verificationErr
		},
	})
	if !errors.Is(err, verificationErr) {
		t.Fatalf("apply error = %v, want errors.Is(_, %v)", err, verificationErr)
	}
	assertEventsEqual(t, events, []string{"lock", "migrate", "verify", "unlock"})
}

func TestApplyReleasesMigrationLockOnMigrationFailure(t *testing.T) {
	t.Parallel()

	var events []string
	migrationErr := errors.New("atlas failed")
	err := apply(t.Context(), applyRequest{Connection: "postgres://captain", Schema: DefaultSchema}, applyDependencies{
		acquireLock: func(context.Context, applyRequest) (migrationLockHandle, error) {
			events = append(events, "lock")
			return &recordingMigrationLock{events: &events}, nil
		},
		migrate: func(context.Context, applyRequest) error {
			events = append(events, "migrate")
			return migrationErr
		},
	})
	if !errors.Is(err, migrationErr) {
		t.Fatalf("apply error = %v, want errors.Is(_, %v)", err, migrationErr)
	}
	assertEventsEqual(t, events, []string{"lock", "migrate", "unlock"})
}

func TestApplyReportsLockAcquisitionAndReleaseErrors(t *testing.T) {
	t.Parallel()

	t.Run("acquire", func(t *testing.T) {
		t.Parallel()
		wantErr := errors.New("lock unavailable")
		err := apply(t.Context(), applyRequest{Connection: "postgres://captain", Schema: DefaultSchema}, applyDependencies{
			acquireLock: func(context.Context, applyRequest) (migrationLockHandle, error) {
				return nil, wantErr
			},
		})
		if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "acquire Captain migration lock") {
			t.Fatalf("apply error = %v, want acquisition error", err)
		}
	})

	t.Run("release joined with migration error", func(t *testing.T) {
		t.Parallel()
		migrationErr := errors.New("migration failed")
		releaseErr := errors.New("unlock failed")
		err := apply(t.Context(), applyRequest{Connection: "postgres://captain", Schema: DefaultSchema}, applyDependencies{
			acquireLock: func(context.Context, applyRequest) (migrationLockHandle, error) {
				return &recordingMigrationLock{err: releaseErr}, nil
			},
			migrate: func(context.Context, applyRequest) error { return migrationErr },
		})
		if !errors.Is(err, migrationErr) || !errors.Is(err, releaseErr) {
			t.Fatalf("apply error = %v, want joined migration and release errors", err)
		}
	})
}

func TestCaptainMigrationLockKeyIsStable(t *testing.T) {
	t.Parallel()
	if captainMigrationLockNamespace != 0x43415054 || captainMigrationLockKey != 0x4d494752 {
		t.Fatalf("Captain migration lock key changed: namespace=%#x key=%#x",
			captainMigrationLockNamespace, captainMigrationLockKey)
	}
}

type recordingMigrationLock struct {
	events *[]string
	err    error
}

func (lock *recordingMigrationLock) Close() error {
	if lock.events != nil {
		*lock.events = append(*lock.events, "unlock")
	}
	return lock.err
}

func assertEventsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func assertContainsAll(t *testing.T, name string, expected ...string) {
	t.Helper()
	data, err := fs.ReadFile(schemaFS, name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	content := string(data)
	for _, value := range expected {
		if !strings.Contains(content, value) {
			t.Errorf("%s does not contain %q", name, value)
		}
	}
}

func assertContainsNone(t *testing.T, name string, unexpected ...string) {
	t.Helper()
	data, err := fs.ReadFile(schemaFS, name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	content := string(data)
	for _, value := range unexpected {
		if strings.Contains(content, value) {
			t.Errorf("%s unexpectedly contains %q", name, value)
		}
	}
}

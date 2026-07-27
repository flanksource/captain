-- phase: post

-- Remove the retired delivery queue and every producer attached by earlier
-- Captain migrations. These statements also make upgrades reclaim existing
-- outbox storage instead of merely omitting the table from fresh installs.
DROP TABLE IF EXISTS public.captain_outbox;

DROP TRIGGER IF EXISTS captain_sessions_emit_after ON public.captain_sessions;
DROP TRIGGER IF EXISTS captain_session_processes_emit_after ON public.captain_session_processes;
DROP TRIGGER IF EXISTS captain_prompt_runs_emit_after ON public.captain_prompt_runs;
DROP TRIGGER IF EXISTS captain_prompt_run_iterations_emit_after ON public.captain_prompt_run_iterations;
DROP TRIGGER IF EXISTS captain_plans_emit_after ON public.captain_plans;
DROP TRIGGER IF EXISTS captain_plan_revisions_emit_after ON public.captain_plan_revisions;
DROP TRIGGER IF EXISTS captain_turns_emit_after ON public.captain_turns;
DROP TRIGGER IF EXISTS captain_model_calls_emit_after ON public.captain_model_calls;
DROP TRIGGER IF EXISTS captain_messages_emit_after ON public.captain_messages;
DROP TRIGGER IF EXISTS captain_events_emit_after ON public.captain_events;
DROP TRIGGER IF EXISTS captain_turn_requests_emit_after ON public.captain_turn_requests;
DROP TRIGGER IF EXISTS captain_artifacts_emit_after ON public.captain_artifacts;
DROP TRIGGER IF EXISTS captain_session_sources_emit_after ON public.captain_session_sources;

DROP FUNCTION IF EXISTS public.captain_emit_session_change();
DROP FUNCTION IF EXISTS public.captain_notify_outbox();

CREATE OR REPLACE FUNCTION public.captain_touch_session_activity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  row_data jsonb := to_jsonb(NEW);
  session_id_value uuid;
  agent_activity_at timestamptz;
BEGIN
  -- Historical backfills validate and checksum the final rows themselves.
  -- Their archive-derived activity timestamps must remain deterministic.
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    RETURN NEW;
  END IF;

  IF pg_trigger_depth() > 1 THEN
    RETURN NEW;
  END IF;

  CASE TG_TABLE_NAME
    WHEN 'captain_prompt_run_iterations' THEN
      SELECT r.session_id
        INTO session_id_value
        FROM public.captain_prompt_runs r
       WHERE r.id = NULLIF(row_data ->> 'prompt_run_id', '')::uuid;
    WHEN 'captain_model_calls' THEN
      SELECT t.session_id
        INTO session_id_value
        FROM public.captain_turns t
       WHERE t.id = NULLIF(row_data ->> 'turn_id', '')::uuid;
    ELSE
      session_id_value := NULLIF(row_data ->> 'session_id', '')::uuid;
  END CASE;

  -- Only timestamps that represent agent work can advance last_activity_at.
  -- Write-time fallbacks would make replayed historical sessions look active
  -- and the monotonic projection could never correct them.
  agent_activity_at := COALESCE(
    NULLIF(row_data ->> 'occurred_at', '')::timestamptz,
    NULLIF(row_data ->> 'state_observed_at', '')::timestamptz,
    NULLIF(row_data ->> 'started_at', '')::timestamptz,
    NULLIF(row_data ->> 'resolved_at', '')::timestamptz
  );

  -- Keep this as an allowlist: host telemetry, ingest bookkeeping, and any new
  -- table without an explicit activity contract must not affect idle checks.
  IF agent_activity_at IS NOT NULL AND TG_OP <> 'DELETE' AND TG_TABLE_NAME IN (
    'captain_messages',
    'captain_turns',
    'captain_turn_requests',
    'captain_model_calls',
    'captain_events',
    'captain_prompt_runs',
    'captain_prompt_run_iterations',
    'captain_artifacts'
  ) THEN
    UPDATE public.captain_sessions
       SET last_activity_at = GREATEST(last_activity_at, agent_activity_at)
     WHERE id = session_id_value
       AND (last_activity_at IS NULL OR last_activity_at < agent_activity_at);
  END IF;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS captain_prompt_runs_activity_after ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_activity_after
AFTER INSERT OR UPDATE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_activity_after ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_activity_after
AFTER INSERT OR UPDATE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_turns_activity_after ON public.captain_turns;
CREATE TRIGGER captain_turns_activity_after
AFTER INSERT OR UPDATE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_model_calls_activity_after ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_activity_after
AFTER INSERT OR UPDATE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_messages_activity_after ON public.captain_messages;
CREATE TRIGGER captain_messages_activity_after
AFTER INSERT OR UPDATE ON public.captain_messages
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_events_activity_after ON public.captain_events;
CREATE TRIGGER captain_events_activity_after
AFTER INSERT OR UPDATE ON public.captain_events
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_turn_requests_activity_after ON public.captain_turn_requests;
CREATE TRIGGER captain_turn_requests_activity_after
AFTER INSERT OR UPDATE ON public.captain_turn_requests
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

DROP TRIGGER IF EXISTS captain_artifacts_activity_after ON public.captain_artifacts;
CREATE TRIGGER captain_artifacts_activity_after
AFTER INSERT OR UPDATE ON public.captain_artifacts
FOR EACH ROW EXECUTE FUNCTION public.captain_touch_session_activity();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_touch_session_activity() FROM PUBLIC;

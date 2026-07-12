-- phase: post
-- runs: always

-- This file is applied after the Atlas HCL realm by commons-db/migrate. Keep
-- every definition repeat-safe: the script deliberately runs on every apply so
-- views and triggers are restored after table reconciliation.

-- Paired provenance foreign keys form intentional cycles across runs, plans,
-- iterations, calls and events. Deferring their NO ACTION checks allows a
-- complete session aggregate to cascade in one statement while a standalone
-- deletion of referenced provenance still fails at transaction commit. Atlas
-- OSS does not model this PostgreSQL constraint property, so the post-HCL SQL
-- phase owns it.
ALTER TABLE public.captain_prompt_runs
  ALTER CONSTRAINT captain_prompt_runs_input_plan_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_prompt_runs
  ALTER CONSTRAINT captain_prompt_runs_input_plan_revision_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_source_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_source_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_approved_revision_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_model_calls
  ALTER CONSTRAINT captain_model_calls_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_model_calls
  ALTER CONSTRAINT captain_model_calls_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_events
  ALTER CONSTRAINT captain_events_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_events
  ALTER CONSTRAINT captain_events_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;

CREATE OR REPLACE FUNCTION public.captain_set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_session_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.lifecycle_status,
      NEW.activity_state,
      NEW.health_state,
      NEW.state_reason
    ) IS DISTINCT FROM ROW(
      OLD.lifecycle_status,
      OLD.activity_state,
      OLD.health_state,
      OLD.state_reason
    ) THEN
      NEW.state_version := OLD.state_version + 1;
      NEW.state_observed_at := observed_at;
    ELSE
      NEW.state_version := OLD.state_version;
      NEW.state_observed_at := OLD.state_observed_at;
    END IF;
  END IF;

  IF NEW.lifecycle_status = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.lifecycle_status IN ('succeeded', 'failed', 'cancelled', 'interrupted') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;

  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_prompt_run_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.phase,
      NEW.state,
      NEW.current_iteration,
      NEW.result_text,
      NEW.result_json,
      NEW.error
    ) IS DISTINCT FROM ROW(
      OLD.phase,
      OLD.state,
      OLD.current_iteration,
      OLD.result_text,
      OLD.result_json,
      OLD.error
    ) THEN
      NEW.version := OLD.version + 1;
    ELSE
      NEW.version := OLD.version;
    END IF;
  END IF;

  IF NEW.state IN ('running', 'waiting') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.finished_at := NULL;
  ELSIF NEW.state IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.finished_at, NEW.created_at, observed_at);
    NEW.finished_at := COALESCE(NEW.finished_at, observed_at);
  END IF;

  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_prompt_iteration_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.state = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.finished_at := NULL;
  ELSIF NEW.state IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.finished_at, NEW.created_at, observed_at);
    NEW.finished_at := COALESCE(NEW.finished_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_turn_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.status = 'open' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.status IN ('ended', 'error', 'interrupted') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_model_call_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.status = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.status IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_turn_request_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.state,
      NEW.response,
      NEW.resolved_by,
      NEW.reason
    ) IS DISTINCT FROM ROW(
      OLD.state,
      OLD.response,
      OLD.resolved_by,
      OLD.reason
    ) THEN
      NEW.version := OLD.version + 1;
    ELSE
      NEW.version := OLD.version;
    END IF;
  END IF;

  IF NEW.state = 'pending' THEN
    NEW.resolved_at := NULL;
  ELSE
    NEW.resolved_at := COALESCE(NEW.resolved_at, clock_timestamp());
    IF NEW.resolved_at < NEW.created_at THEN
      NEW.created_at := NEW.resolved_at;
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_plan_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.approval_state IN ('approved', 'rejected') THEN
      NEW.approval_created_at := COALESCE(NEW.approval_created_at, clock_timestamp());
    ELSIF NEW.approval_state = 'revision_requested' THEN
      NEW.feedback_at := COALESCE(NEW.feedback_at, clock_timestamp());
    END IF;
  ELSIF NEW.approval_state IS DISTINCT FROM OLD.approval_state THEN
    IF NEW.approval_state IN ('approved', 'rejected') THEN
      NEW.approval_created_at := COALESCE(NEW.approval_created_at, clock_timestamp());
    ELSIF NEW.approval_state = 'revision_requested' THEN
      NEW.feedback_at := COALESCE(NEW.feedback_at, clock_timestamp());
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_sync_prompt_run_iteration()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  UPDATE public.captain_prompt_runs
     SET current_iteration = GREATEST(current_iteration, NEW.iteration)
   WHERE id = NEW.prompt_run_id
     AND current_iteration < NEW.iteration;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_emit_session_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  row_data jsonb;
  session_id_value uuid;
  record_id_value uuid;
  activity_at timestamptz := clock_timestamp();
BEGIN
  -- Nested updates are maintenance performed by another trigger. Cascading
  -- deletes are represented by the originating session mutation instead of a
  -- separate outbox row for every child.
  IF pg_trigger_depth() > 1 THEN
    IF TG_OP = 'DELETE' THEN
      RETURN OLD;
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' THEN
    row_data := to_jsonb(OLD);
  ELSE
    row_data := to_jsonb(NEW);
  END IF;

  record_id_value := NULLIF(row_data ->> 'id', '')::uuid;

  CASE TG_TABLE_NAME
    WHEN 'captain_sessions' THEN
      session_id_value := record_id_value;
    WHEN 'captain_prompt_run_iterations' THEN
      SELECT r.session_id
        INTO session_id_value
        FROM public.captain_prompt_runs r
       WHERE r.id = NULLIF(row_data ->> 'prompt_run_id', '')::uuid;
    WHEN 'captain_plan_revisions' THEN
      SELECT p.source_session_id
        INTO session_id_value
        FROM public.captain_plans p
       WHERE p.id = NULLIF(row_data ->> 'plan_id', '')::uuid;
    WHEN 'captain_model_calls' THEN
      SELECT t.session_id
        INTO session_id_value
        FROM public.captain_turns t
       WHERE t.id = NULLIF(row_data ->> 'turn_id', '')::uuid;
    ELSE
      session_id_value := NULLIF(
        COALESCE(row_data ->> 'session_id', row_data ->> 'source_session_id'),
        ''
      )::uuid;
  END CASE;

  -- A direct delete of an already-detached child can legitimately have no
  -- surviving aggregate. There is nothing useful to publish in that case.
  IF session_id_value IS NULL THEN
    IF TG_OP = 'DELETE' THEN
      RETURN OLD;
    END IF;
    RETURN NEW;
  END IF;

  -- Referential actions do not reliably increase pg_trigger_depth(). Suppress
  -- their child rows by observing that the owning aggregate has already gone.
  -- A directly deleted child session still publishes while its root exists.
  IF TG_OP = 'DELETE' THEN
    IF TG_TABLE_NAME = 'captain_sessions' THEN
      IF NULLIF(row_data ->> 'root_session_id', '') IS NOT NULL
         AND NOT EXISTS (
           SELECT 1
           FROM public.captain_sessions root
           WHERE root.id = NULLIF(row_data ->> 'root_session_id', '')::uuid
         ) THEN
        RETURN OLD;
      END IF;
    ELSIF NOT EXISTS (
      SELECT 1
      FROM public.captain_sessions aggregate
      WHERE aggregate.id = session_id_value
    ) THEN
      RETURN OLD;
    END IF;
  END IF;

  activity_at := COALESCE(
    NULLIF(row_data ->> 'occurred_at', '')::timestamptz,
    NULLIF(row_data ->> 'state_observed_at', '')::timestamptz,
    NULLIF(row_data ->> 'started_at', '')::timestamptz,
    NULLIF(row_data ->> 'resolved_at', '')::timestamptz,
    NULLIF(row_data ->> 'updated_at', '')::timestamptz,
    NULLIF(row_data ->> 'recorded_at', '')::timestamptz,
    NULLIF(row_data ->> 'created_at', '')::timestamptz,
    clock_timestamp()
  );

  IF TG_OP <> 'DELETE' AND TG_TABLE_NAME <> 'captain_sessions' THEN
    UPDATE public.captain_sessions
       SET last_activity_at = CASE
         WHEN last_activity_at IS NULL OR activity_at > last_activity_at THEN activity_at
         ELSE last_activity_at
       END
     WHERE id = session_id_value;
  END IF;

  INSERT INTO public.captain_outbox (
    topic,
    aggregate_type,
    aggregate_id,
    event_type,
    payload
  ) VALUES (
    'captain.session.changed',
    'session',
    session_id_value,
    TG_TABLE_NAME || '.' || lower(TG_OP),
    jsonb_strip_nulls(jsonb_build_object(
      'table', TG_TABLE_NAME,
      'operation', lower(TG_OP),
      'record_id', record_id_value,
      'session_id', session_id_value,
      'turn_id', CASE
        WHEN TG_TABLE_NAME = 'captain_turns' THEN record_id_value
        ELSE NULLIF(row_data ->> 'turn_id', '')::uuid
      END,
      'prompt_run_id', CASE
        WHEN TG_TABLE_NAME = 'captain_prompt_runs' THEN record_id_value
        ELSE NULLIF(row_data ->> 'prompt_run_id', '')::uuid
      END,
      'iteration_id', CASE
        WHEN TG_TABLE_NAME = 'captain_prompt_run_iterations' THEN record_id_value
        ELSE NULLIF(row_data ->> 'iteration_id', '')::uuid
      END,
      'plan_id', CASE
        WHEN TG_TABLE_NAME = 'captain_plans' THEN record_id_value
        ELSE NULLIF(row_data ->> 'plan_id', '')::uuid
      END,
      'model_call_id', CASE
        WHEN TG_TABLE_NAME = 'captain_model_calls' THEN record_id_value
        ELSE NULLIF(row_data ->> 'model_call_id', '')::uuid
      END,
      'state', row_data ->> 'state',
      'status', COALESCE(row_data ->> 'status', row_data ->> 'lifecycle_status'),
      'phase', row_data ->> 'phase',
      'occurred_at', activity_at
    ))
  );

  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_notify_outbox()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  PERFORM pg_notify('captain_outbox', NEW.sequence::text);
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS captain_sessions_state_before ON public.captain_sessions;
CREATE TRIGGER captain_sessions_state_before
BEFORE INSERT OR UPDATE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_set_session_state();

DROP TRIGGER IF EXISTS captain_prompt_runs_state_before ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_state_before
BEFORE INSERT OR UPDATE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_set_prompt_run_state();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_state_before ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_state_before
BEFORE INSERT OR UPDATE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_set_prompt_iteration_state();

DROP TRIGGER IF EXISTS captain_turns_state_before ON public.captain_turns;
CREATE TRIGGER captain_turns_state_before
BEFORE INSERT OR UPDATE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_set_turn_state();

DROP TRIGGER IF EXISTS captain_model_calls_state_before ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_state_before
BEFORE INSERT OR UPDATE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_set_model_call_state();

DROP TRIGGER IF EXISTS captain_turn_requests_state_before ON public.captain_turn_requests;
CREATE TRIGGER captain_turn_requests_state_before
BEFORE INSERT OR UPDATE ON public.captain_turn_requests
FOR EACH ROW EXECUTE FUNCTION public.captain_set_turn_request_state();

DROP TRIGGER IF EXISTS captain_plans_state_before ON public.captain_plans;
CREATE TRIGGER captain_plans_state_before
BEFORE INSERT OR UPDATE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_set_plan_state();

DROP TRIGGER IF EXISTS captain_sessions_updated_at_before ON public.captain_sessions;
CREATE TRIGGER captain_sessions_updated_at_before
BEFORE UPDATE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_session_processes_updated_at_before ON public.captain_session_processes;
CREATE TRIGGER captain_session_processes_updated_at_before
BEFORE UPDATE ON public.captain_session_processes
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_runs_updated_at_before ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_updated_at_before
BEFORE UPDATE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_updated_at_before ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_updated_at_before
BEFORE UPDATE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_plans_updated_at_before ON public.captain_plans;
CREATE TRIGGER captain_plans_updated_at_before
BEFORE UPDATE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_turns_updated_at_before ON public.captain_turns;
CREATE TRIGGER captain_turns_updated_at_before
BEFORE UPDATE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_model_calls_updated_at_before ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_updated_at_before
BEFORE UPDATE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_session_sources_updated_at_before ON public.captain_session_sources;
CREATE TRIGGER captain_session_sources_updated_at_before
BEFORE UPDATE ON public.captain_session_sources
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_sync_after ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_sync_after
AFTER INSERT OR UPDATE OF iteration, prompt_run_id ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_sync_prompt_run_iteration();

DROP TRIGGER IF EXISTS captain_sessions_emit_after ON public.captain_sessions;
CREATE TRIGGER captain_sessions_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_session_processes_emit_after ON public.captain_session_processes;
CREATE TRIGGER captain_session_processes_emit_after
AFTER INSERT OR DELETE OR UPDATE OF
  status,
  command,
  cwd,
  surface,
  lease_owner,
  lease_token,
  lease_expires_at,
  ended_at
ON public.captain_session_processes
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_prompt_runs_emit_after ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_emit_after ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_plans_emit_after ON public.captain_plans;
CREATE TRIGGER captain_plans_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_plan_revisions_emit_after ON public.captain_plan_revisions;
CREATE TRIGGER captain_plan_revisions_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_plan_revisions
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_turns_emit_after ON public.captain_turns;
CREATE TRIGGER captain_turns_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_model_calls_emit_after ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_messages_emit_after ON public.captain_messages;
CREATE TRIGGER captain_messages_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_messages
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_events_emit_after ON public.captain_events;
CREATE TRIGGER captain_events_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_events
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_turn_requests_emit_after ON public.captain_turn_requests;
CREATE TRIGGER captain_turn_requests_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_turn_requests
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_artifacts_emit_after ON public.captain_artifacts;
CREATE TRIGGER captain_artifacts_emit_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_artifacts
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_session_sources_emit_after ON public.captain_session_sources;
CREATE TRIGGER captain_session_sources_emit_after
AFTER INSERT OR DELETE OR UPDATE OF path, source_identity, parser_version
ON public.captain_session_sources
FOR EACH ROW EXECUTE FUNCTION public.captain_emit_session_change();

DROP TRIGGER IF EXISTS captain_outbox_notify_after ON public.captain_outbox;
CREATE TRIGGER captain_outbox_notify_after
AFTER INSERT ON public.captain_outbox
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_outbox();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_set_updated_at() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_session_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_prompt_run_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_prompt_iteration_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_turn_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_model_call_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_turn_request_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_plan_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_sync_prompt_run_iteration() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_emit_session_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_outbox() FROM PUBLIC;

CREATE OR REPLACE VIEW public.captain_session_overview
WITH (security_barrier = true)
AS
SELECT
  s.id,
  s.provider_session_id,
  s.source,
  s.provider,
  s.host_id,
  s.parent_session_id,
  s.root_session_id,
  COALESCE(s.root_session_id, s.id) AS thread_id,
  s.path,
  COALESCE(source.path, s.path) AS history_file,
  source.source_kind,
  source.parser_version,
  s.project,
  s.cwd,
  s.title,
  s.initial_prompt,
  s.slug,
  s.agent_type,
  s.description,
  s.cli_version,
  s.lifecycle_status,
  s.activity_state,
  s.health_state,
  s.state_reason,
  s.state_version,
  s.state_observed_at,
  s.git,
  s.metadata,
  s.started_at,
  s.ended_at,
  s.last_activity_at,
  s.created_at,
  s.updated_at,
  CASE
    WHEN s.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(s.ended_at, clock_timestamp()) - s.started_at)
  END AS duration_seconds,
  process.id AS process_id,
  process.pid,
  process.status AS process_status,
  process.command,
  process.cwd AS process_cwd,
  process.surface,
  process.process_started_at,
  process.last_heartbeat_at,
  process.lease_owner,
  process.lease_expires_at,
  process.ended_at AS process_ended_at,
  process.id IS NOT NULL AND process.ended_at IS NULL AS process_active,
  COALESCE(message_stats.message_count, 0) AS message_count,
  COALESCE(message_stats.tool_call_count, 0) AS tool_call_count,
  COALESCE(event_stats.event_count, 0) AS event_count,
  COALESCE(call_stats.turn_count, 0) AS turn_count,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  COALESCE(agent_stats.agent_count, 1) AS agent_count,
  COALESCE(prompt_stats.prompt_run_count, 0) AS prompt_run_count,
  COALESCE(plan_stats.plan_count, 0) AS plan_count,
  COALESCE(request_stats.pending_request_count, 0) AS pending_request_count,
  COALESCE(request_stats.approved_request_count, 0) AS approved_request_count,
  COALESCE(request_stats.denied_request_count, 0) AS denied_request_count,
  COALESCE(file_stats.file_read_count, 0) AS file_read_count,
  COALESCE(file_stats.file_written_count, 0) AS file_written_count,
  latest_call.model,
  latest_call.backend,
  latest_call.effort,
  latest_call.context_tokens,
  latest_call.context_window_tokens,
  CASE
    WHEN latest_call.context_window_tokens > 0 THEN
      GREATEST(
        0,
        LEAST(
          100,
          round(
            (1 - latest_call.context_tokens::numeric / latest_call.context_window_tokens::numeric) * 100
          )::integer
        )
      )
    ELSE NULL
  END AS context_free_percent,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd
FROM public.captain_sessions s
LEFT JOIN LATERAL (
  SELECT p.*
  FROM public.captain_session_processes p
  WHERE p.session_id = s.id
    AND p.ended_at IS NULL
  ORDER BY COALESCE(p.last_heartbeat_at, p.process_started_at) DESC, p.id DESC
  LIMIT 1
) process ON true
LEFT JOIN LATERAL (
  SELECT src.path, src.source_kind, src.parser_version
  FROM public.captain_session_sources src
  WHERE src.session_id = s.id
  ORDER BY src.updated_at DESC, src.id DESC
  LIMIT 1
) source ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS message_count,
    COALESCE(sum((
      SELECT count(*)
      FROM jsonb_array_elements(
        CASE
          WHEN jsonb_typeof(m.parts) = 'array' THEN m.parts
          ELSE '[]'::jsonb
        END
      ) part
      WHERE (
        part ->> 'type' = 'dynamic-tool'
        OR part ->> 'type' LIKE 'tool-%'
      )
        AND COALESCE(part ->> 'toolName', '') <> ''
    )), 0)::bigint AS tool_call_count
  FROM public.captain_messages m
  WHERE m.session_id = s.id
) message_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS event_count
  FROM public.captain_events e
  WHERE e.session_id = s.id
) event_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(DISTINCT t.id)::bigint AS turn_count,
    count(c.id)::bigint AS model_call_count,
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      c.input_cost
      + c.output_cost
      + c.reasoning_cost
      + c.cache_read_cost
      + c.cache_write_cost
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_turns t
  LEFT JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT
    c.model,
    c.backend,
    c.effort,
    c.context_tokens,
    c.context_window_tokens
  FROM public.captain_turns t
  JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
  ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
  LIMIT 1
) latest_call ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint + 1 AS agent_count
  FROM public.captain_sessions child
  WHERE child.root_session_id = s.id
) agent_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS prompt_run_count
  FROM public.captain_prompt_runs r
  WHERE r.session_id = s.id
) prompt_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS plan_count
  FROM public.captain_plans p
  WHERE p.source_session_id = s.id
) plan_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*) FILTER (WHERE r.state = 'pending')::bigint AS pending_request_count,
    count(*) FILTER (WHERE r.state IN ('approved', 'answered'))::bigint AS approved_request_count,
    count(*) FILTER (WHERE r.state = 'denied')::bigint AS denied_request_count
  FROM public.captain_turn_requests r
  WHERE r.session_id = s.id
) request_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*) FILTER (WHERE a.kind = 'file.read')::bigint AS file_read_count,
    count(*) FILTER (WHERE a.kind IN ('file.write', 'file.edit', 'file.delete'))::bigint AS file_written_count
  FROM public.captain_artifacts a
  WHERE a.session_id = s.id
    AND a.kind LIKE 'file.%'
) file_stats ON true;

COMMENT ON VIEW public.captain_session_overview IS
  'One row per session for PostgREST list, metadata, health, live-process, usage and cost surfaces.';

CREATE OR REPLACE VIEW public.captain_session_transcript
WITH (security_barrier = true)
AS
SELECT
  m.id,
  m.session_id,
  m.turn_id,
  m.model_call_id,
  m.provider_message_id,
  m.sequence,
  m.role,
  m.parts,
  m.raw,
  m.schema_version,
  m.occurred_at,
  m.recorded_at,
  c.model,
  c.backend,
  c.effort,
  c.status AS model_call_status
FROM public.captain_messages m
LEFT JOIN public.captain_model_calls c ON c.id = m.model_call_id;

COMMENT ON VIEW public.captain_session_transcript IS
  'Message rows for the SessionInspector transcript tab; order by sequence through PostgREST.';

CREATE OR REPLACE VIEW public.captain_session_turns
WITH (security_barrier = true)
AS
SELECT
  t.id,
  t.session_id,
  t.provider_turn_id,
  t.turn_index,
  t.description,
  t.status,
  t.stop_reason,
  t.error,
  t.started_at,
  t.ended_at,
  t.created_at,
  t.updated_at,
  CASE
    WHEN t.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(t.ended_at, clock_timestamp()) - t.started_at)
  END AS duration_seconds,
  latest_call.model,
  latest_call.backend,
  latest_call.effort,
  latest_call.context_tokens,
  latest_call.context_window_tokens,
  CASE
    WHEN latest_call.context_window_tokens > 0 THEN
      GREATEST(
        0,
        LEAST(
          100,
          round(
            (1 - latest_call.context_tokens::numeric / latest_call.context_window_tokens::numeric) * 100
          )::integer
        )
      )
    ELSE NULL
  END AS context_free_percent,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd,
  COALESCE(message_stats.message_count, 0) AS message_count,
  COALESCE(message_stats.message_ids, ARRAY[]::uuid[]) AS message_ids,
  COALESCE(event_stats.event_count, 0) AS event_count,
  COALESCE(event_stats.event_ids, ARRAY[]::uuid[]) AS event_ids
FROM public.captain_turns t
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS model_call_count,
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      c.input_cost
      + c.output_cost
      + c.reasoning_cost
      + c.cache_read_cost
      + c.cache_write_cost
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_model_calls c
  WHERE c.turn_id = t.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT c.model, c.backend, c.effort, c.context_tokens, c.context_window_tokens
  FROM public.captain_model_calls c
  WHERE c.turn_id = t.id
  ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
  LIMIT 1
) latest_call ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS message_count,
    array_agg(m.id ORDER BY m.sequence) AS message_ids
  FROM public.captain_messages m
  WHERE m.turn_id = t.id
) message_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS event_count,
    array_agg(e.id ORDER BY COALESCE(e.occurred_at, e.recorded_at), e.id) AS event_ids
  FROM public.captain_events e
  WHERE e.turn_id = t.id
) event_stats ON true;

COMMENT ON VIEW public.captain_session_turns IS
  'Per-turn usage, cost, context and message/event attribution for the SessionInspector turns tab.';

CREATE OR REPLACE VIEW public.captain_session_agents
WITH (security_barrier = true)
AS
SELECT
  s.id,
  s.id AS session_id,
  s.parent_session_id,
  s.root_session_id,
  COALESCE(s.root_session_id, s.id) AS thread_id,
  s.parent_session_id IS NULL AS is_root,
  s.agent_type,
  s.description,
  s.path AS history_file,
  s.source,
  s.provider,
  s.lifecycle_status,
  s.activity_state,
  s.health_state,
  s.started_at,
  s.ended_at,
  COALESCE(child_stats.child_count, 0) AS child_count,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd
FROM public.captain_sessions s
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS child_count
  FROM public.captain_sessions child
  WHERE child.parent_session_id = s.id
) child_stats ON true
LEFT JOIN LATERAL (
  SELECT
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      c.input_cost
      + c.output_cost
      + c.reasoning_cost
      + c.cache_read_cost
      + c.cache_write_cost
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_turns t
  JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
) call_stats ON true;

COMMENT ON VIEW public.captain_session_agents IS
  'Session hierarchy rows with usage and cost for the SessionInspector agents tab.';

CREATE OR REPLACE VIEW public.captain_session_files
WITH (security_barrier = true)
AS
SELECT
  a.id,
  a.session_id,
  a.turn_id,
  a.model_call_id,
  a.prompt_run_id,
  a.kind,
  split_part(a.kind, '.', 2) AS operation,
  a.path,
  a.digest,
  a.content_type,
  a.metadata,
  a.occurred_at,
  a.created_at
FROM public.captain_artifacts a
WHERE a.kind LIKE 'file.%'
  AND a.path IS NOT NULL;

COMMENT ON VIEW public.captain_session_files IS
  'Normalized file.* artifact rows for the SessionInspector files tab.';

CREATE OR REPLACE VIEW public.captain_session_plans
WITH (security_barrier = true)
AS
SELECT
  p.id,
  p.source_session_id AS session_id,
  p.source_prompt_run_id,
  p.source_iteration_id,
  p.source_turn_id,
  p.title,
  p.slug,
  p.path,
  p.variant,
  p.spec_profile,
  p.approval_state,
  p.approved_revision_id,
  p.approval_comment,
  p.approved_by,
  p.approval_created_at,
  p.feedback_at,
  p.created_at,
  p.updated_at,
  latest_revision.id AS latest_revision_id,
  latest_revision.revision AS latest_revision,
  latest_revision.plan_markdown AS latest_plan_markdown,
  latest_revision.content_hash AS latest_content_hash,
  latest_revision.feedback AS latest_feedback,
  latest_revision.created_by AS latest_created_by,
  latest_revision.created_at AS latest_created_at,
  approved_revision.revision AS approved_revision,
  approved_revision.plan_markdown AS approved_plan_markdown,
  approved_revision.content_hash AS approved_content_hash,
  COALESCE(approved_revision.id, latest_revision.id) AS current_revision_id,
  COALESCE(approved_revision.revision, latest_revision.revision) AS current_revision,
  COALESCE(approved_revision.plan_markdown, latest_revision.plan_markdown) AS plan_markdown,
  COALESCE(approved_revision.content_hash, latest_revision.content_hash) AS content_hash
FROM public.captain_plans p
LEFT JOIN LATERAL (
  SELECT r.*
  FROM public.captain_plan_revisions r
  WHERE r.plan_id = p.id
  ORDER BY r.revision DESC, r.created_at DESC, r.id DESC
  LIMIT 1
) latest_revision ON true
LEFT JOIN public.captain_plan_revisions approved_revision
  ON approved_revision.id = p.approved_revision_id;

COMMENT ON VIEW public.captain_session_plans IS
  'Plan rows with latest and approved immutable revisions for the SessionInspector plan tab.';

CREATE OR REPLACE VIEW public.captain_session_approvals
WITH (security_barrier = true)
AS
SELECT
  r.id,
  r.session_id,
  r.turn_id,
  r.prompt_run_id,
  r.plan_id,
  r.model_call_id,
  r.tool_call_id,
  r.kind,
  r.state,
  r.request,
  r.response,
  r.idempotency_key,
  r.requested_by,
  r.resolved_by,
  r.reason,
  r.version,
  r.expires_at,
  r.created_at,
  r.resolved_at
FROM public.captain_turn_requests r
WHERE r.kind IN ('tool_approval', 'plan_exit_approval');

COMMENT ON VIEW public.captain_session_approvals IS
  'Tool and plan-exit approval rows for the SessionInspector approvals tab.';

CREATE OR REPLACE VIEW public.captain_session_costs
WITH (security_barrier = true)
AS
SELECT
  concat_ws(
    ':',
    t.session_id::text,
    c.model,
    c.backend,
    COALESCE(c.effort, 'default'),
    upper(c.currency)
  ) AS id,
  t.session_id,
  c.model,
  c.backend,
  c.effort,
  upper(c.currency) AS currency,
  count(*)::bigint AS model_call_count,
  sum(c.input_tokens)::bigint AS input_tokens,
  sum(c.output_tokens)::bigint AS output_tokens,
  sum(c.reasoning_tokens)::bigint AS reasoning_tokens,
  sum(c.cache_read_tokens)::bigint AS cache_read_tokens,
  sum(c.cache_write_tokens)::bigint AS cache_write_tokens,
  (
    sum(c.input_tokens)
    + sum(c.output_tokens)
    + sum(c.cache_read_tokens)
    + sum(c.cache_write_tokens)
  )::bigint AS total_tokens,
  sum(c.input_cost) AS input_cost,
  sum(c.output_cost) AS output_cost,
  sum(c.reasoning_cost) AS reasoning_cost,
  sum(c.cache_read_cost) AS cache_read_cost,
  sum(c.cache_write_cost) AS cache_write_cost,
  sum(
    c.input_cost
    + c.output_cost
    + c.reasoning_cost
    + c.cache_read_cost
    + c.cache_write_cost
  ) AS total_cost,
  min(c.started_at) AS first_call_at,
  max(c.ended_at) AS last_call_at
FROM public.captain_turns t
JOIN public.captain_model_calls c ON c.turn_id = t.id
GROUP BY
  t.session_id,
  c.model,
  c.backend,
  c.effort,
  upper(c.currency);

COMMENT ON VIEW public.captain_session_costs IS
  'Per-session model/backend/effort token and cost totals for the SessionInspector costs tab.';

CREATE OR REPLACE VIEW public.captain_session_events
WITH (security_barrier = true)
AS
SELECT
  e.id,
  e.session_id,
  e.turn_id,
  e.prompt_run_id,
  e.iteration_id,
  e.model_call_id,
  e.parent_event_id,
  e.event_key,
  e.stream,
  e.sequence,
  e.kind,
  e.scope,
  e.payload,
  e.schema_version,
  e.occurred_at,
  e.recorded_at
FROM public.captain_events e;

COMMENT ON VIEW public.captain_session_events IS
  'Durable session and turn event rows for SessionInspector metadata and raw-event surfaces.';

CREATE OR REPLACE VIEW public.captain_prompt_run_overview
WITH (security_barrier = true)
AS
SELECT
  r.id,
  r.session_id,
  r.root_session_id,
  r.batch_id,
  r.parent_run_id,
  r.input_plan_id,
  r.input_plan_revision_id,
  r.origin,
  r.spec_profile,
  r.admission_key,
  r.rendered_spec,
  r.prompt_markdown,
  r.verification_markdown,
  r.phase,
  r.state,
  r.current_iteration,
  r.result_text,
  r.result_json,
  r.error,
  r.version,
  r.queued_at,
  r.started_at,
  r.finished_at,
  r.created_at,
  r.updated_at,
  CASE
    WHEN r.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(r.finished_at, clock_timestamp()) - r.started_at)
  END AS duration_seconds,
  COALESCE(iteration_stats.iteration_count, 0) AS iteration_count,
  COALESCE(iteration_stats.succeeded_iteration_count, 0) AS succeeded_iteration_count,
  COALESCE(iteration_stats.failed_iteration_count, 0) AS failed_iteration_count,
  latest_iteration.id AS latest_iteration_id,
  latest_iteration.iteration AS latest_iteration,
  latest_iteration.state AS latest_iteration_state,
  latest_iteration.feedback AS latest_iteration_feedback,
  latest_iteration.verification_result AS latest_verification_result,
  latest_iteration.error AS latest_iteration_error,
  latest_iteration.started_at AS latest_iteration_started_at,
  latest_iteration.finished_at AS latest_iteration_finished_at,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd,
  COALESCE(plan_stats.plan_count, 0) AS plan_count,
  plan_stats.latest_plan_id,
  plan_stats.latest_plan_approval_state,
  plan_stats.latest_plan_revision
FROM public.captain_prompt_runs r
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS iteration_count,
    count(*) FILTER (WHERE i.state = 'succeeded')::bigint AS succeeded_iteration_count,
    count(*) FILTER (WHERE i.state = 'failed')::bigint AS failed_iteration_count
  FROM public.captain_prompt_run_iterations i
  WHERE i.prompt_run_id = r.id
) iteration_stats ON true
LEFT JOIN LATERAL (
  SELECT i.*
  FROM public.captain_prompt_run_iterations i
  WHERE i.prompt_run_id = r.id
  ORDER BY i.iteration DESC, i.created_at DESC, i.id DESC
  LIMIT 1
) latest_iteration ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS model_call_count,
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      c.input_cost
      + c.output_cost
      + c.reasoning_cost
      + c.cache_read_cost
      + c.cache_write_cost
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_model_calls c
  WHERE c.prompt_run_id = r.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS plan_count,
    (array_agg(p.id ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_id,
    (array_agg(p.approval_state ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_approval_state,
    (array_agg(pr.revision ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_revision
  FROM public.captain_plans p
  LEFT JOIN LATERAL (
    SELECT revision.revision
    FROM public.captain_plan_revisions revision
    WHERE revision.plan_id = p.id
    ORDER BY revision.revision DESC, revision.created_at DESC, revision.id DESC
    LIMIT 1
  ) pr ON true
  WHERE p.source_prompt_run_id = r.id
) plan_stats ON true;

COMMENT ON VIEW public.captain_prompt_run_overview IS
  'Prompt-run control-plane state with iteration, plan, usage, cost and verification summaries.';

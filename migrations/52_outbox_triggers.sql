-- phase: post

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
  -- Explicit historical backfills validate and checksum the final rows
  -- themselves. Suppress activity/outbox projection for those transaction-local
  -- writes so imported timestamps remain archive-derived and deterministic.
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    IF TG_OP = 'DELETE' THEN
      RETURN OLD;
    END IF;
    RETURN NEW;
  END IF;

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
REVOKE ALL ON FUNCTION public.captain_emit_session_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_outbox() FROM PUBLIC;

-- phase: post
-- dependsOn: 52_session_activity_triggers.sql

-- Live session followers LISTEN on captain_session_change instead of polling.
-- The payload is only the session UUID: PostgreSQL folds identical
-- channel+payload notifications within one transaction, so a 10,000-message
-- ingest transaction wakes each follower once, not once per row. Nothing is
-- delivered for a rolled-back transaction.
--
-- This redefines 52's activity function rather than adding a second per-row
-- trigger to the ingest hot path. 52 is a dependency, so a later edit to it
-- re-runs this script and the notify survives.
CREATE OR REPLACE FUNCTION public.captain_touch_session_activity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
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

  -- Only timestamps that represent agent work can advance last_activity_at.
  -- Write-time fallbacks would make replayed historical sessions look active
  -- and the monotonic projection could never correct them.
  CASE TG_TABLE_NAME
    WHEN 'captain_prompt_run_iterations' THEN
      SELECT r.session_id
        INTO session_id_value
        FROM public.captain_prompt_runs r
       WHERE r.id = NEW.prompt_run_id;
      agent_activity_at := NEW.started_at;
    WHEN 'captain_model_calls' THEN
      SELECT t.session_id
        INTO session_id_value
        FROM public.captain_turns t
       WHERE t.id = NEW.turn_id;
      agent_activity_at := NEW.started_at;
    WHEN 'captain_messages' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.occurred_at;
    WHEN 'captain_turns' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.started_at;
    WHEN 'captain_turn_requests' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.resolved_at;
    WHEN 'captain_events' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.occurred_at;
    WHEN 'captain_prompt_runs' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.started_at;
    WHEN 'captain_artifacts' THEN
      session_id_value := NEW.session_id;
      agent_activity_at := NEW.occurred_at;
  END CASE;

  -- Above the activity guard: a message enriched in place (tool output landing
  -- on an UPDATE with an unchanged occurred_at) changes what a follower shows
  -- without advancing last_activity_at.
  IF session_id_value IS NOT NULL THEN
    PERFORM pg_notify('captain_session_change', session_id_value::text);
  END IF;

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

-- A session row notifies itself when its state_version advances (lifecycle,
-- activity and health changes, per 51; heartbeats and activity touches do not
-- advance it and stay silent) or its metadata changes (ingest projects the
-- plan, todos and changed files there, sometimes with no new message to wake a
-- follower; an unchanged merge compares equal and stays silent), and notifies
-- its parent when it is inserted as
-- a child, so a follower of the parent re-resolves its thread and finds a
-- transcript row or sub-agent created after it started following.
CREATE OR REPLACE FUNCTION public.captain_notify_session_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    RETURN NEW;
  END IF;
  IF TG_OP = 'INSERT' THEN
    PERFORM pg_notify('captain_session_change', NEW.parent_session_id::text);
  ELSE
    PERFORM pg_notify('captain_session_change', NEW.id::text);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_notify_plan_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    RETURN NEW;
  END IF;
  PERFORM pg_notify('captain_session_change', NEW.source_session_id::text);
  RETURN NEW;
END;
$$;

-- Appending a revision is how a plan's content changes, and it writes only
-- captain_plan_revisions: the plan row is locked, never updated.
CREATE OR REPLACE FUNCTION public.captain_notify_plan_revision_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  session_id_value uuid;
BEGIN
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    RETURN NEW;
  END IF;
  SELECT p.source_session_id
    INTO STRICT session_id_value
    FROM public.captain_plans p
   WHERE p.id = NEW.plan_id;
  PERFORM pg_notify('captain_session_change', session_id_value::text);
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS captain_sessions_change_notify_after ON public.captain_sessions;
CREATE TRIGGER captain_sessions_change_notify_after
AFTER UPDATE ON public.captain_sessions
FOR EACH ROW
WHEN (NEW.state_version IS DISTINCT FROM OLD.state_version OR NEW.metadata IS DISTINCT FROM OLD.metadata)
EXECUTE FUNCTION public.captain_notify_session_change();

DROP TRIGGER IF EXISTS captain_sessions_child_notify_after ON public.captain_sessions;
CREATE TRIGGER captain_sessions_child_notify_after
AFTER INSERT ON public.captain_sessions
FOR EACH ROW
WHEN (NEW.parent_session_id IS NOT NULL)
EXECUTE FUNCTION public.captain_notify_session_change();

DROP TRIGGER IF EXISTS captain_plans_change_notify_after ON public.captain_plans;
CREATE TRIGGER captain_plans_change_notify_after
AFTER INSERT OR UPDATE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_plan_change();

DROP TRIGGER IF EXISTS captain_plan_revisions_change_notify_after ON public.captain_plan_revisions;
CREATE TRIGGER captain_plan_revisions_change_notify_after
AFTER INSERT ON public.captain_plan_revisions
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_plan_revision_change();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_touch_session_activity() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_session_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_plan_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_plan_revision_change() FROM PUBLIC;

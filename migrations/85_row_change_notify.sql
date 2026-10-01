-- phase: post

-- captain_row_change: the host-facing change feed for the rows a host projects
-- Captain execution from. Hosts LISTEN here (database.ListenRowChanges) instead
-- of installing their own triggers on Captain's tables.
--
-- Contract. Every committed INSERT, UPDATE and DELETE of a row in
-- captain_sessions, captain_prompt_runs, captain_turn_requests and
-- captain_prompt_run_iterations -- including a row removed by a cascade --
-- sends one JSON object:
--
--   table          the Captain table name
--   op             INSERT | UPDATE | DELETE
--   id             the row's id
--   sessionId      captain_sessions: id; captain_prompt_runs and
--                  captain_turn_requests: session_id; absent otherwise
--   rootSessionId  captain_sessions: root_session_id, or id for a root;
--                  captain_prompt_runs: root_session_id; absent otherwise
--   promptRunId    captain_prompt_runs: id; captain_turn_requests (when set)
--                  and captain_prompt_run_iterations: prompt_run_id
--
-- The payload is identity only, never a timestamp or state, so PostgreSQL
-- folds the identical notifications one transaction sends for the same row
-- into one: a listener re-reads the row it is told about. An UPDATE that moves
-- a row between sessions or runs notifies the old and the new identity. A
-- rolled-back transaction delivers nothing, and nothing is delivered while no
-- LISTEN is active, so a listener re-reads everything it projects whenever its
-- LISTEN is (re)established. TRUNCATE is not notified.
--
-- captain.suppress_session_change does not silence this channel: it keeps
-- historical backfills from waking transcript followers, whereas a host's
-- projection of a backfilled run must still see it.
CREATE OR REPLACE FUNCTION public.captain_notify_row_identity(
  p_table text,
  p_op text,
  p_id uuid,
  p_session_id uuid,
  p_root_session_id uuid,
  p_prompt_run_id uuid
)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT pg_notify('captain_row_change', jsonb_strip_nulls(jsonb_build_object(
    'table', p_table,
    'op', p_op,
    'id', p_id,
    'sessionId', p_session_id,
    'rootSessionId', p_root_session_id,
    'promptRunId', p_prompt_run_id
  ))::text);
$$;

-- Each table's identity is read in its own branch, as 83 does, so a column
-- reference is only ever planned against the row type that has it.
CREATE OR REPLACE FUNCTION public.captain_notify_row_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  CASE TG_TABLE_NAME
    WHEN 'captain_sessions' THEN
      IF TG_OP <> 'INSERT' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          OLD.id, OLD.id, COALESCE(OLD.root_session_id, OLD.id), NULL);
      END IF;
      IF TG_OP <> 'DELETE' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          NEW.id, NEW.id, COALESCE(NEW.root_session_id, NEW.id), NULL);
      END IF;
    WHEN 'captain_prompt_runs' THEN
      IF TG_OP <> 'INSERT' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          OLD.id, OLD.session_id, OLD.root_session_id, OLD.id);
      END IF;
      IF TG_OP <> 'DELETE' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          NEW.id, NEW.session_id, NEW.root_session_id, NEW.id);
      END IF;
    WHEN 'captain_turn_requests' THEN
      IF TG_OP <> 'INSERT' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          OLD.id, OLD.session_id, NULL, OLD.prompt_run_id);
      END IF;
      IF TG_OP <> 'DELETE' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          NEW.id, NEW.session_id, NULL, NEW.prompt_run_id);
      END IF;
    WHEN 'captain_prompt_run_iterations' THEN
      IF TG_OP <> 'INSERT' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          OLD.id, NULL, NULL, OLD.prompt_run_id);
      END IF;
      IF TG_OP <> 'DELETE' THEN
        PERFORM public.captain_notify_row_identity(TG_TABLE_NAME, TG_OP,
          NEW.id, NULL, NULL, NEW.prompt_run_id);
      END IF;
  END CASE;
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS captain_sessions_row_change_notify_after ON public.captain_sessions;
CREATE TRIGGER captain_sessions_row_change_notify_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_row_change();

DROP TRIGGER IF EXISTS captain_prompt_runs_row_change_notify_after ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_row_change_notify_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_row_change();

DROP TRIGGER IF EXISTS captain_turn_requests_row_change_notify_after ON public.captain_turn_requests;
CREATE TRIGGER captain_turn_requests_row_change_notify_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_turn_requests
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_row_change();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_row_change_notify_after ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_row_change_notify_after
AFTER INSERT OR UPDATE OR DELETE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_notify_row_change();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_notify_row_identity(text, text, uuid, uuid, uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_row_change() FROM PUBLIC;

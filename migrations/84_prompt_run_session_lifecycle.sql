-- phase: post
-- dependsOn: 51_state_triggers.sql, 83_session_change_notify.sql

-- A prompt run's state is the lifecycle of the sessions it runs in: the
-- admission session (session_id) and the provider transcript it executed in
-- (execution_session_id). Before this script only Captain's own Go paths wrote
-- that lifecycle, so a run driven by any other producer -- Gavel's todo
-- runtime updates captain_prompt_runs and nothing else -- left both sessions
-- `created` forever, and a follower waiting for a terminal lifecycle never
-- closed. Projecting it here is the one seam every producer passes through.
--
-- Mapping: pending leaves the sessions alone (a queued run has touched
-- nothing); running and waiting are `running`; succeeded, failed and cancelled
-- map to the lifecycle of the same name. A terminal projection stamps ended_at
-- from the run's finished_at, not the clock, so a replayed or backfilled run
-- stays deterministic; it also idles the activity and records the run's error
-- as the state reason.
--
-- Only the latest started run of a session projects onto it: runs that share
-- one execution session (resumed attempts) are ordered by created_at, and an
-- older run's late write never overrides the newer run. A newer run that is
-- still pending has not started, so it does not hide the older run's outcome.
--
-- A terminal projection never overwrites `partial` or `interrupted`: those are
-- verdicts a Go writer derives from more than one run's state (a batch root)
-- or from how the execution ended (a chat interrupt), which the run alone does
-- not carry. A new run going `running` still reopens such a session.
--
-- The UPDATE fires only when the lifecycle differs, so 51 advances
-- state_version once and 83 notifies each session once per change.
--
-- No pg_trigger_depth guard: the projection writes captain_sessions, whose
-- triggers never write captain_prompt_runs, so it cannot recurse; and 51's
-- captain_sync_prompt_run_iteration writes only current_iteration, which is
-- not in this trigger's column list. A depth guard would instead silently skip
-- a state change some future trigger makes on a run's behalf.
CREATE OR REPLACE FUNCTION public.captain_project_prompt_run_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  lifecycle public.captain_session_lifecycle_status;
  terminal boolean;
  target uuid;
BEGIN
  CASE NEW.state
    WHEN 'pending' THEN
      RETURN NULL;
    WHEN 'running', 'waiting' THEN
      lifecycle := 'running';
    ELSE
      lifecycle := NEW.state::text::public.captain_session_lifecycle_status;
  END CASE;
  terminal := lifecycle <> 'running';

  FOREACH target IN ARRAY ARRAY[NEW.session_id, NEW.execution_session_id] LOOP
    CONTINUE WHEN target IS NULL;
    CONTINUE WHEN EXISTS (
      SELECT 1
        FROM public.captain_prompt_runs newer
       WHERE (newer.session_id = target OR newer.execution_session_id = target)
         AND newer.state <> 'pending'
         AND (newer.created_at, newer.id) > (NEW.created_at, NEW.id)
    );

    UPDATE public.captain_sessions s
       SET lifecycle_status = lifecycle,
           activity_state = CASE WHEN terminal THEN 'idle' ELSE s.activity_state END,
           state_reason = CASE WHEN terminal THEN NULLIF(btrim(NEW.error), '') END,
           started_at = COALESCE(s.started_at, NEW.started_at),
           ended_at = CASE
             WHEN terminal THEN GREATEST(NEW.finished_at, COALESCE(s.started_at, NEW.started_at))
           END
     WHERE s.id = target
       AND s.lifecycle_status IS DISTINCT FROM lifecycle
       AND NOT (terminal AND s.lifecycle_status IN ('partial', 'interrupted'));
  END LOOP;

  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS captain_prompt_runs_session_lifecycle_after ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_session_lifecycle_after
AFTER INSERT OR UPDATE OF state, session_id, execution_session_id ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_project_prompt_run_lifecycle();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_project_prompt_run_lifecycle() FROM PUBLIC;

-- One-time repair of the sessions runs finished in before the trigger existed:
-- each session still `created` or `running` whose latest started run (the
-- trigger's ordering) is terminal takes that run's outcome, exactly as the
-- trigger would have projected it. A session whose latest run is still running
-- is left alone -- the trigger projects its next change. Idempotent: a repaired
-- session is terminal and no longer matches.
WITH run_sessions AS (
  SELECT r.session_id AS target, r.id, r.state, r.error, r.created_at, r.started_at, r.finished_at
    FROM public.captain_prompt_runs r
   WHERE r.state <> 'pending'
  UNION
  SELECT r.execution_session_id, r.id, r.state, r.error, r.created_at, r.started_at, r.finished_at
    FROM public.captain_prompt_runs r
   WHERE r.state <> 'pending'
     AND r.execution_session_id IS NOT NULL
), latest AS (
  SELECT DISTINCT ON (target) *
    FROM run_sessions
   ORDER BY target, created_at DESC, id DESC
)
UPDATE public.captain_sessions s
   SET lifecycle_status = latest.state::text::public.captain_session_lifecycle_status,
       activity_state = 'idle',
       state_reason = NULLIF(btrim(latest.error), ''),
       started_at = COALESCE(s.started_at, latest.started_at),
       ended_at = GREATEST(latest.finished_at, COALESCE(s.started_at, latest.started_at))
  FROM latest
 WHERE s.id = latest.target
   AND latest.state IN ('succeeded', 'failed', 'cancelled')
   AND s.lifecycle_status IN ('created', 'running');

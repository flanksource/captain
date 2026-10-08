-- phase: pre

-- The schema phase now reconciles CHECK expressions it finds edited in place
-- (commons-db migrate), so on a database still holding the legacy caller-tool
-- approval identity it replaces captain_turn_requests_tool_approval_identity
-- with the shape in 32_execution_approvals.pg.hcl before any post-phase script
-- runs. That shape requires a caller-tool approval (credential_id set) to name
-- its turn and model call, and legacy rows do not: the replacement fails with a
-- check violation and blocks every Apply.
--
-- 74_turn_request_approval_identity.sql backfills those rows, but in the post
-- phase -- after the replacement has already failed. This script runs the same
-- backfill, and the same ambiguity guard, ahead of the schema phase, so the
-- replacement validates against rows that already satisfy it and an
-- unresolvable row is reported by id rather than as a bare check violation.
--
-- Idempotent: once every caller-tool approval names its turn and model call,
-- each statement matches nothing. A fresh database has no table yet.

DO $$
DECLARE
  invalid_ids text;
BEGIN
  IF to_regclass('public.captain_turn_requests') IS NULL THEN
    RETURN;
  END IF;

  WITH candidates AS (
    SELECT
      request.id AS request_id,
      model_call.id AS model_call_id,
      model_call.turn_id,
      count(*) OVER (PARTITION BY request.id) AS candidate_count
    FROM public.captain_turn_requests request
    JOIN public.captain_model_calls model_call
      ON model_call.prompt_run_id = request.prompt_run_id
     AND (request.model_call_id IS NULL OR request.model_call_id = model_call.id)
     AND (request.turn_id IS NULL OR request.turn_id = model_call.turn_id)
    JOIN public.captain_turns turn
      ON turn.id = model_call.turn_id
     AND turn.session_id = request.session_id
    WHERE request.kind = 'tool_approval'
      AND request.credential_id IS NOT NULL
      AND (request.turn_id IS NULL OR request.model_call_id IS NULL)
  ), unique_candidates AS (
    SELECT request_id, model_call_id, turn_id
    FROM candidates
    WHERE candidate_count = 1
  )
  UPDATE public.captain_turn_requests request
  SET
    turn_id = COALESCE(request.turn_id, candidate.turn_id),
    model_call_id = COALESCE(request.model_call_id, candidate.model_call_id)
  FROM unique_candidates candidate
  WHERE request.id = candidate.request_id;

  SELECT string_agg(request.id::text, ', ' ORDER BY request.id)
  INTO invalid_ids
  FROM public.captain_turn_requests request
  WHERE request.kind = 'tool_approval'
    AND request.credential_id IS NOT NULL
    AND (request.turn_id IS NULL OR request.model_call_id IS NULL);

  IF invalid_ids IS NOT NULL THEN
    RAISE EXCEPTION 'ambiguous legacy tool approval identity for request(s): %', invalid_ids
      USING ERRCODE = 'check_violation';
  END IF;
END
$$;

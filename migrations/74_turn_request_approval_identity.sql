-- phase: post

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

DO $$
DECLARE
  invalid_ids text;
BEGIN
  SELECT string_agg(request.id::text, ', ' ORDER BY request.id)
  INTO invalid_ids
  FROM public.captain_turn_requests request
  WHERE request.kind = 'tool_approval'
    AND (
      request.prompt_run_id IS NULL
      OR request.turn_id IS NULL
      OR request.model_call_id IS NULL
      OR request.tool_call_id IS NULL
      OR NOT EXISTS (
        SELECT 1
        FROM public.captain_model_calls model_call
        JOIN public.captain_turns turn ON turn.id = model_call.turn_id
        WHERE model_call.id = request.model_call_id
          AND model_call.prompt_run_id = request.prompt_run_id
          AND model_call.turn_id = request.turn_id
          AND turn.session_id = request.session_id
      )
    );

  IF invalid_ids IS NOT NULL THEN
    RAISE EXCEPTION 'ambiguous legacy tool approval identity for request(s): %', invalid_ids
      USING ERRCODE = 'check_violation';
  END IF;
END
$$;

ALTER TABLE public.captain_turn_requests
  DROP CONSTRAINT IF EXISTS captain_turn_requests_tool_approval_identity;

ALTER TABLE public.captain_turn_requests
  ADD CONSTRAINT captain_turn_requests_tool_approval_identity
  CHECK (
    kind <> 'tool_approval'
    OR (
      prompt_run_id IS NOT NULL
      AND turn_id IS NOT NULL
      AND model_call_id IS NOT NULL
      AND tool_call_id IS NOT NULL
    )
  ) NOT VALID;

ALTER TABLE public.captain_turn_requests
  VALIDATE CONSTRAINT captain_turn_requests_tool_approval_identity;

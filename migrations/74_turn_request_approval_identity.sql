-- phase: post

-- RETIRED: this script's strict four-column identity is superseded by
-- 81_turn_request_provider_approval_identity.sql, and what remains here is the
-- legacy caller-tool backfill it was written for.
--
-- A script re-runs whenever its content hash changes or its ledger row is
-- dropped (a restore, a rebuilt environment, an edit to this file). In its
-- original form the re-run raised on any tool_approval row with a NULL turn_id
-- -- which is exactly the shape a credential-less provider approval has, and
-- exactly what 81 legitimises. One stored provider approval was therefore
-- enough to block every later Apply, and with it startup.
--
-- Two changes retire it. The backfill and its ambiguity guard are scoped to
-- caller-tool rows (credential_id IS NOT NULL), so a re-run is a no-op for
-- provider rows. And the constraint it installs is now character-for-character
-- the one 81 installs, because a re-run of this script alone -- 81's hash is
-- unchanged, so 81 does not follow -- would otherwise leave the database
-- holding a superseded constraint that rejects every provider approval. The
-- final shape has to be reachable from either script on its own.

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

DO $$
DECLARE
  invalid_ids text;
BEGIN
  SELECT string_agg(request.id::text, ', ' ORDER BY request.id)
  INTO invalid_ids
  FROM public.captain_turn_requests request
  WHERE request.kind = 'tool_approval'
    AND request.credential_id IS NOT NULL
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
      AND tool_call_id IS NOT NULL
      AND (
        credential_id IS NULL
        OR (turn_id IS NOT NULL AND model_call_id IS NOT NULL)
      )
    )
  ) NOT VALID;

ALTER TABLE public.captain_turn_requests
  VALIDATE CONSTRAINT captain_turn_requests_tool_approval_identity;

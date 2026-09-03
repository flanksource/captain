-- phase: post

-- A caller-tool approval is raised inside an aichat turn, so it always has a
-- captain_turns row and a captain_model_calls row to hang off. A provider
-- approval raised by `captain prompt run` -- or by an external host driving a
-- streaming provider -- has a session and a prompt run and nothing else: those
-- providers never open a turn or a model call. Demanding all four columns is
-- what kept the durable approval broker private to the aichat execution path.
--
-- Identity is therefore conditional on the credential. The caller-tool path
-- (credential_id IS NOT NULL) keeps the full four-column identity that
-- 74_turn_request_approval_identity.sql backfilled and validated. The
-- credential-less provider path is identified by (prompt_run_id, tool_call_id)
-- alone, which is exactly what its "provider:<prompt run>:<tool call>"
-- idempotency key already keys on.
--
-- 74 is retired to a caller-tool-only backfill and now installs this exact
-- constraint too. That duplication is deliberate: a script re-runs when its
-- content hash changes or its ledger row is dropped, and either script can
-- re-run without the other, so the final shape has to be what both of them
-- leave behind. Keep the two CHECK bodies identical.

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

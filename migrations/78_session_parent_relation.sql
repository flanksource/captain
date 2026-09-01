-- phase: post

UPDATE public.captain_sessions
SET parent_relation = 'agent'
WHERE parent_session_id IS NOT NULL
  AND parent_relation IS NULL;

ALTER TABLE public.captain_sessions
  DROP CONSTRAINT IF EXISTS captain_sessions_parent_relation_valid;

ALTER TABLE public.captain_sessions
  ADD CONSTRAINT captain_sessions_parent_relation_valid
  CHECK (
    (parent_session_id IS NULL AND parent_relation IS NULL)
    OR (parent_session_id IS NOT NULL AND parent_relation IN ('agent', 'transcript'))
  ) NOT VALID;

ALTER TABLE public.captain_sessions
  VALIDATE CONSTRAINT captain_sessions_parent_relation_valid;

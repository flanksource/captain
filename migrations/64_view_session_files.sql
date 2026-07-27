-- phase: post

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

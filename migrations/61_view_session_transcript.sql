-- phase: post

CREATE OR REPLACE VIEW public.captain_session_transcript
WITH (security_barrier = true)
AS
SELECT
  m.id,
  m.session_id,
  m.turn_id,
  m.model_call_id,
  m.provider_message_id,
  m.sequence,
  m.role,
  m.parts,
  m.raw,
  m.schema_version,
  m.occurred_at,
  m.recorded_at,
  c.model,
  c.backend,
  c.effort,
  c.status AS model_call_status,
  -- Appended after initial release: CREATE OR REPLACE VIEW only allows adding
  -- columns at the end, so later additions must stay below this line.
  m.source_line
FROM public.captain_messages m
LEFT JOIN public.captain_model_calls c ON c.id = m.model_call_id;

COMMENT ON VIEW public.captain_session_transcript IS
  'Message rows for the SessionInspector transcript tab; order by sequence through PostgREST.';

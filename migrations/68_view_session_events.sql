-- phase: post

CREATE OR REPLACE VIEW public.captain_session_events
WITH (security_barrier = true)
AS
SELECT
  e.id,
  e.session_id,
  e.turn_id,
  e.prompt_run_id,
  e.iteration_id,
  e.model_call_id,
  e.parent_event_id,
  e.event_key,
  e.stream,
  e.sequence,
  e.kind,
  e.scope,
  e.payload,
  e.schema_version,
  e.occurred_at,
  e.recorded_at
FROM public.captain_events e;

COMMENT ON VIEW public.captain_session_events IS
  'Durable session and turn event rows for SessionInspector metadata and raw-event surfaces.';

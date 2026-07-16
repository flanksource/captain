-- phase: post

CREATE OR REPLACE VIEW public.captain_session_approvals
WITH (security_barrier = true)
AS
SELECT
  r.id,
  r.session_id,
  r.turn_id,
  r.prompt_run_id,
  r.plan_id,
  r.model_call_id,
  r.tool_call_id,
  r.kind,
  r.state,
  r.request,
  r.response,
  r.idempotency_key,
  r.requested_by,
  r.resolved_by,
  r.reason,
  r.version,
  r.expires_at,
  r.created_at,
  r.resolved_at
FROM public.captain_turn_requests r
WHERE r.kind IN ('tool_approval', 'plan_exit_approval');

COMMENT ON VIEW public.captain_session_approvals IS
  'Tool and plan-exit approval rows for the SessionInspector approvals tab.';

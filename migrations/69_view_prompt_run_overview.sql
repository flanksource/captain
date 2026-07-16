-- phase: post

CREATE OR REPLACE VIEW public.captain_prompt_run_overview
WITH (security_barrier = true)
AS
SELECT
  r.id,
  r.session_id,
  r.root_session_id,
  r.batch_id,
  r.parent_run_id,
  r.input_plan_id,
  r.input_plan_revision_id,
  r.origin,
  r.spec_profile,
  r.admission_key,
  r.rendered_spec,
  r.prompt_markdown,
  r.verification_markdown,
  r.phase,
  r.state,
  r.current_iteration,
  r.result_text,
  r.result_json,
  r.error,
  r.version,
  r.queued_at,
  r.started_at,
  r.finished_at,
  r.created_at,
  r.updated_at,
  CASE
    WHEN r.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(r.finished_at, clock_timestamp()) - r.started_at)
  END AS duration_seconds,
  COALESCE(iteration_stats.iteration_count, 0) AS iteration_count,
  COALESCE(iteration_stats.succeeded_iteration_count, 0) AS succeeded_iteration_count,
  COALESCE(iteration_stats.failed_iteration_count, 0) AS failed_iteration_count,
  latest_iteration.id AS latest_iteration_id,
  latest_iteration.iteration AS latest_iteration,
  latest_iteration.state AS latest_iteration_state,
  latest_iteration.feedback AS latest_iteration_feedback,
  latest_iteration.verification_result AS latest_verification_result,
  latest_iteration.error AS latest_iteration_error,
  latest_iteration.started_at AS latest_iteration_started_at,
  latest_iteration.finished_at AS latest_iteration_finished_at,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd,
  COALESCE(plan_stats.plan_count, 0) AS plan_count,
  plan_stats.latest_plan_id,
  plan_stats.latest_plan_approval_state,
  plan_stats.latest_plan_revision,
  r.execution_session_id
FROM public.captain_prompt_runs r
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS iteration_count,
    count(*) FILTER (WHERE i.state = 'succeeded')::bigint AS succeeded_iteration_count,
    count(*) FILTER (WHERE i.state = 'failed')::bigint AS failed_iteration_count
  FROM public.captain_prompt_run_iterations i
  WHERE i.prompt_run_id = r.id
) iteration_stats ON true
LEFT JOIN LATERAL (
  SELECT i.*
  FROM public.captain_prompt_run_iterations i
  WHERE i.prompt_run_id = r.id
  ORDER BY i.iteration DESC, i.created_at DESC, i.id DESC
  LIMIT 1
) latest_iteration ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS model_call_count,
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      c.input_cost
      + c.output_cost
      + c.reasoning_cost
      + c.cache_read_cost
      + c.cache_write_cost
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_model_calls c
  WHERE c.prompt_run_id = r.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS plan_count,
    (array_agg(p.id ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_id,
    (array_agg(p.approval_state ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_approval_state,
    (array_agg(pr.revision ORDER BY p.created_at DESC, p.id DESC))[1] AS latest_plan_revision
  FROM public.captain_plans p
  LEFT JOIN LATERAL (
    SELECT revision.revision
    FROM public.captain_plan_revisions revision
    WHERE revision.plan_id = p.id
    ORDER BY revision.revision DESC, revision.created_at DESC, revision.id DESC
    LIMIT 1
  ) pr ON true
  WHERE p.source_prompt_run_id = r.id
) plan_stats ON true;

COMMENT ON VIEW public.captain_prompt_run_overview IS
  'Prompt-run control-plane state with iteration, plan, usage, cost and verification summaries.';

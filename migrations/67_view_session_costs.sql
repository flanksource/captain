-- phase: post

CREATE OR REPLACE VIEW public.captain_session_costs
WITH (security_barrier = true)
AS
SELECT
  concat_ws(
    ':',
    t.session_id::text,
    c.model,
    c.backend,
    COALESCE(c.effort, 'default'),
    upper(c.currency)
  ) AS id,
  t.session_id,
  c.model,
  c.backend,
  c.effort,
  upper(c.currency) AS currency,
  count(*)::bigint AS model_call_count,
  sum(c.input_tokens)::bigint AS input_tokens,
  sum(c.output_tokens)::bigint AS output_tokens,
  sum(c.reasoning_tokens)::bigint AS reasoning_tokens,
  sum(c.cache_read_tokens)::bigint AS cache_read_tokens,
  sum(c.cache_write_tokens)::bigint AS cache_write_tokens,
  (
    sum(c.input_tokens)
    + sum(c.output_tokens)
    + sum(c.cache_read_tokens)
    + sum(c.cache_write_tokens)
  )::bigint AS total_tokens,
  sum(c.input_cost) AS input_cost,
  sum(c.output_cost) AS output_cost,
  sum(c.reasoning_cost) AS reasoning_cost,
  sum(c.cache_read_cost) AS cache_read_cost,
  sum(c.cache_write_cost) AS cache_write_cost,
  sum(
    c.input_cost
    + c.output_cost
    + c.reasoning_cost
    + c.cache_read_cost
    + c.cache_write_cost
  ) AS total_cost,
  min(c.started_at) AS first_call_at,
  max(c.ended_at) AS last_call_at
FROM public.captain_turns t
JOIN public.captain_model_calls c ON c.turn_id = t.id
GROUP BY
  t.session_id,
  c.model,
  c.backend,
  c.effort,
  upper(c.currency);

COMMENT ON VIEW public.captain_session_costs IS
  'Per-session model/backend/effort token and cost totals for the SessionInspector costs tab.';

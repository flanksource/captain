-- phase: post

CREATE OR REPLACE VIEW public.captain_session_turns
WITH (security_barrier = true)
AS
SELECT
  t.id,
  t.session_id,
  t.provider_turn_id,
  t.turn_index,
  t.description,
  t.status,
  t.stop_reason,
  t.error,
  t.started_at,
  t.ended_at,
  t.created_at,
  t.updated_at,
  CASE
    WHEN t.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(t.ended_at, clock_timestamp()) - t.started_at)
  END AS duration_seconds,
  latest_call.model,
  latest_call.backend,
  latest_call.effort,
  latest_call.context_tokens,
  latest_call.context_window_tokens,
  CASE
    WHEN latest_call.context_window_tokens > 0 THEN
      GREATEST(
        0,
        LEAST(
          100,
          round(
            (1 - latest_call.context_tokens::numeric / latest_call.context_window_tokens::numeric) * 100
          )::integer
        )
      )
    ELSE NULL
  END AS context_free_percent,
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
  COALESCE(message_stats.message_count, 0) AS message_count,
  COALESCE(message_stats.message_ids, ARRAY[]::uuid[]) AS message_ids,
  COALESCE(event_stats.event_count, 0) AS event_count,
  COALESCE(event_stats.event_ids, ARRAY[]::uuid[]) AS event_ids
FROM public.captain_turns t
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
  WHERE c.turn_id = t.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT c.model, c.backend, c.effort, c.context_tokens, c.context_window_tokens
  FROM public.captain_model_calls c
  WHERE c.turn_id = t.id
  ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
  LIMIT 1
) latest_call ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS message_count,
    array_agg(m.id ORDER BY m.sequence) AS message_ids
  FROM public.captain_messages m
  WHERE m.turn_id = t.id
) message_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS event_count,
    array_agg(e.id ORDER BY COALESCE(e.occurred_at, e.recorded_at), e.id) AS event_ids
  FROM public.captain_events e
  WHERE e.turn_id = t.id
) event_stats ON true;

COMMENT ON VIEW public.captain_session_turns IS
  'Per-turn usage, cost, context and message/event attribution for the SessionInspector turns tab.';

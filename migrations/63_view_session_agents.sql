-- phase: post

CREATE OR REPLACE VIEW public.captain_session_agents
WITH (security_barrier = true)
AS
SELECT
  s.id,
  s.id AS session_id,
  s.parent_session_id,
  s.root_session_id,
  COALESCE(s.root_session_id, s.id) AS thread_id,
  s.parent_session_id IS NULL AS is_root,
  s.agent_type,
  s.description,
  s.path AS history_file,
  s.source,
  s.provider,
  s.lifecycle_status,
  s.activity_state,
  s.health_state,
  s.started_at,
  s.ended_at,
  COALESCE(child_stats.child_count, 0) AS child_count,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd
FROM public.captain_sessions s
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS child_count
  FROM public.captain_sessions child
  WHERE child.parent_session_id = s.id
) child_stats ON true
LEFT JOIN LATERAL (
  SELECT
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.reasoning_tokens), 0)::bigint AS reasoning_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(
      CASE
        WHEN c.provider_cost_usd > 0 THEN c.provider_cost_usd
        ELSE c.input_cost
          + c.output_cost
          + c.reasoning_cost
          + c.cache_read_cost
          + c.cache_write_cost
      END
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM public.captain_turns t
  JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
) call_stats ON true;

COMMENT ON VIEW public.captain_session_agents IS
  'Session hierarchy rows with usage and cost for the SessionInspector agents tab.';

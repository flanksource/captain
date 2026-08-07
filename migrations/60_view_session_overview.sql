-- phase: post

CREATE OR REPLACE VIEW public.captain_session_overview
WITH (security_barrier = true)
AS
SELECT
  s.id,
  s.provider_session_id,
  s.source,
  s.provider,
  s.host_id,
  s.parent_session_id,
  s.root_session_id,
  COALESCE(s.root_session_id, s.id) AS thread_id,
  s.path,
  COALESCE(source.path, s.path) AS history_file,
  source.source_kind,
  source.parser_version,
  s.project,
  s.cwd,
  s.title,
  s.initial_prompt,
  s.slug,
  s.agent_type,
  s.description,
  s.cli_version,
  s.lifecycle_status,
  s.activity_state,
  s.health_state,
  s.state_reason,
  s.state_version,
  s.state_observed_at,
  s.git,
  s.metadata,
  s.started_at,
  s.ended_at,
  s.last_activity_at,
  s.created_at,
  s.updated_at,
  CASE
    WHEN s.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(s.ended_at, clock_timestamp()) - s.started_at)
  END AS duration_seconds,
  process.id AS process_id,
  process.pid,
  process.status AS process_status,
  process.command,
  process.cwd AS process_cwd,
  process.surface,
  process.process_started_at,
  process.last_heartbeat_at,
  process.lease_owner,
  process.lease_expires_at,
  process.ended_at AS process_ended_at,
  process.id IS NOT NULL AND process.ended_at IS NULL AS process_active,
  COALESCE(message_stats.message_count, 0) AS message_count,
  COALESCE(message_stats.tool_call_count, 0) AS tool_call_count,
  COALESCE(event_stats.event_count, 0) AS event_count,
  COALESCE(call_stats.turn_count, 0) AS turn_count,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  COALESCE(agent_stats.agent_count, 1) AS agent_count,
  COALESCE(prompt_stats.prompt_run_count, 0) AS prompt_run_count,
  COALESCE(plan_stats.plan_count, 0) AS plan_count,
  COALESCE(request_stats.pending_request_count, 0) AS pending_request_count,
  COALESCE(request_stats.approved_request_count, 0) AS approved_request_count,
  COALESCE(request_stats.denied_request_count, 0) AS denied_request_count,
  -- Always 0: nothing writes captain_artifacts. Kept because CREATE OR REPLACE
  -- VIEW cannot drop a column, and commons-db only drops a view when a *table*
  -- diff would break it, so removing these here fails every existing database.
  COALESCE(file_stats.file_read_count, 0) AS file_read_count,
  COALESCE(file_stats.file_written_count, 0) AS file_written_count,
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
  -- Appended after initial release: CREATE OR REPLACE VIEW only allows adding
  -- columns at the end, so later additions must stay below this line.
  process.source AS process_source,
  process.cpu_percent,
  process.memory_percent,
  process.memory_rss_bytes,
  process.sampled_at AS process_sampled_at,
  latest_run.execution_mode,
  -- The portion of cost_usd the providers themselves reported. cost_usd falls
  -- back to list-priced buckets per call, so it alone cannot tell a billed
  -- figure from a reconstruction; this can, and callers must not present an
  -- estimate as billed cost. The bucket sums come with it so a caller holding
  -- an api.Cost can fall back the same way cost_usd does.
  COALESCE(call_stats.provider_cost_usd, 0::numeric) AS provider_cost_usd,
  COALESCE(call_stats.input_cost, 0::numeric) AS input_cost,
  COALESCE(call_stats.output_cost, 0::numeric) AS output_cost,
  COALESCE(call_stats.reasoning_cost, 0::numeric) AS reasoning_cost,
  COALESCE(call_stats.cache_read_cost, 0::numeric) AS cache_read_cost,
  COALESCE(call_stats.cache_write_cost, 0::numeric) AS cache_write_cost
FROM public.captain_sessions s
LEFT JOIN LATERAL (
  SELECT p.*
  FROM public.captain_session_processes p
  WHERE p.session_id = s.id
    AND p.ended_at IS NULL
  ORDER BY COALESCE(p.last_heartbeat_at, p.process_started_at) DESC, p.id DESC
  LIMIT 1
) process ON true
LEFT JOIN LATERAL (
  SELECT src.path, src.source_kind, src.parser_version
  FROM public.captain_session_sources src
  WHERE src.session_id = s.id
  ORDER BY src.updated_at DESC, src.id DESC
  LIMIT 1
) source ON true
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS message_count,
    COALESCE(sum((
      SELECT count(*)
      FROM jsonb_array_elements(
        CASE
          WHEN jsonb_typeof(m.parts) = 'array' THEN m.parts
          ELSE '[]'::jsonb
        END
      ) part
      WHERE (
        part ->> 'type' = 'dynamic-tool'
        OR part ->> 'type' LIKE 'tool-%'
      )
        AND COALESCE(part ->> 'toolName', '') <> ''
    )), 0)::bigint AS tool_call_count
  FROM public.captain_messages m
  WHERE m.session_id = s.id
) message_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS event_count
  FROM public.captain_events e
  WHERE e.session_id = s.id
) event_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(DISTINCT t.id)::bigint AS turn_count,
    count(c.id)::bigint AS model_call_count,
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
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd,
    COALESCE(sum(c.provider_cost_usd)
      FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS provider_cost_usd,
    COALESCE(sum(c.input_cost) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS input_cost,
    COALESCE(sum(c.output_cost) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS output_cost,
    COALESCE(sum(c.reasoning_cost) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS reasoning_cost,
    COALESCE(sum(c.cache_read_cost) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cache_read_cost,
    COALESCE(sum(c.cache_write_cost) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cache_write_cost
  FROM public.captain_turns t
  LEFT JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
) call_stats ON true
LEFT JOIN LATERAL (
  SELECT
    c.model,
    c.backend,
    c.effort,
    c.context_tokens,
    c.context_window_tokens
  FROM public.captain_turns t
  JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
  ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
  LIMIT 1
) latest_call ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint + 1 AS agent_count
  FROM public.captain_sessions child
  WHERE child.root_session_id = s.id
) agent_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS prompt_run_count
  FROM public.captain_prompt_runs r
  WHERE r.session_id = s.id
) prompt_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS plan_count
  FROM public.captain_plans p
  WHERE p.source_session_id = s.id
) plan_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*) FILTER (WHERE r.state = 'pending')::bigint AS pending_request_count,
    count(*) FILTER (WHERE r.state IN ('approved', 'answered'))::bigint AS approved_request_count,
    count(*) FILTER (WHERE r.state = 'denied')::bigint AS denied_request_count
  FROM public.captain_turn_requests r
  WHERE r.session_id = s.id
) request_stats ON true
LEFT JOIN LATERAL (
  SELECT
    count(*) FILTER (WHERE a.kind = 'file.read')::bigint AS file_read_count,
    count(*) FILTER (WHERE a.kind IN ('file.write', 'file.edit', 'file.delete'))::bigint AS file_written_count
  FROM public.captain_artifacts a
  WHERE a.session_id = s.id
    AND a.kind LIKE 'file.%'
) file_stats ON true
LEFT JOIN LATERAL (
  SELECT NULLIF(r.runtime ->> 'mode', '') AS execution_mode
  FROM public.captain_prompt_runs r
  WHERE r.session_id = s.id
  ORDER BY COALESCE(r.finished_at, r.started_at, r.created_at) DESC, r.id DESC
  LIMIT 1
) latest_run ON true;

COMMENT ON VIEW public.captain_session_overview IS
  'One row per session for PostgREST list, metadata, health, live-process, usage and cost surfaces.';

-- phase: post
-- dependsOn: 60_view_session_overview.sql, 90_row_hooks.sql

-- captain_session_overview used to aggregate every source table on each read:
-- the session list alone spent 60% of all database time doing it. The rows now
-- live in captain_session_overview_rows, recomputed at commit for every session
-- a transaction touched (91 queues and flushes them), and the view is a thin
-- projection over that table.
--
-- The table is derived data. It is UNLOGGED (no WAL; crash recovery truncates
-- it) and absent from the HCL, so Atlas never alters it and commons-db filters
-- its DropTable. captain_session_overview_compute is the single definition of
-- every column: rebuild writes it from there and nothing else writes the
-- table, and the tests compare the two.

-- One predicate for "this message part is a tool call", shared by the compute
-- view and the message triggers.
CREATE OR REPLACE FUNCTION public.captain_message_tool_call_count(parts jsonb)
RETURNS bigint
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
SET search_path = pg_catalog
AS $$
  SELECT count(*)
  FROM jsonb_array_elements(CASE WHEN jsonb_typeof(parts) = 'array' THEN parts ELSE '[]'::jsonb END) part
  WHERE (part ->> 'type' = 'dynamic-tool' OR part ->> 'type' LIKE 'tool-%')
    AND COALESCE(part ->> 'toolName', '') <> ''
$$;

-- Columns chosen by "latest"/"current" rules rather than sums, joined into
-- captain_session_overview_compute below.
CREATE OR REPLACE VIEW public.captain_session_overview_selections AS
SELECT
  s.id,
  COALESCE(source.path, s.path) AS history_file,
  source.source_kind,
  source.parser_version,
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
  process.source AS process_source,
  process.cpu_percent,
  process.memory_percent,
  process.memory_rss_bytes,
  process.sampled_at AS process_sampled_at,
  COALESCE(agent_stats.agent_count, 1) AS agent_count,
  latest_call.model,
  latest_call.provider AS model_provider,
  latest_call.mode AS model_mode,
  latest_call.effort,
  CASE WHEN latest_call.context_free_percent IS NOT NULL THEN latest_call.context_tokens END AS context_tokens,
  CASE WHEN latest_call.context_free_percent IS NOT NULL THEN latest_call.context_window_tokens END AS context_window_tokens,
  latest_call.context_free_percent,
  latest_run.execution_mode,
  -- The session list hides inactive empty sessions; a registered transcript
  -- with a path is content.
  EXISTS (
    SELECT 1 FROM public.captain_session_sources src
    WHERE src.session_id = s.id AND NULLIF(btrim(src.path), '') IS NOT NULL
  ) AS source_path_present
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
  SELECT c.model, c.provider, c.mode, c.effort, c.context_tokens, c.context_window_tokens, c.context_free_percent
  FROM public.captain_turns t
  JOIN public.captain_model_calls c ON c.turn_id = t.id
  WHERE t.session_id = s.id
  -- c.id breaks the tie the old view left to scan order, so the incremental
  -- and rebuilt rows always pick the same call.
  ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC, c.id DESC
  LIMIT 1
) latest_call ON true
LEFT JOIN LATERAL (
  WITH RECURSIVE agents AS (
    SELECT child.id
    FROM public.captain_sessions child
    WHERE child.root_session_id = s.id
      AND (
        (child.parent_session_id = s.id AND child.parent_relation = 'agent')
        OR (child.parent_session_id IS NULL AND child.parent_relation IS NULL)
      )
    UNION ALL
    SELECT child.id
    FROM public.captain_sessions child
    JOIN agents parent ON child.parent_session_id = parent.id
    WHERE child.parent_relation = 'agent'
  )
  SELECT count(*)::bigint + 1 AS agent_count
  FROM agents
) agent_stats ON true
LEFT JOIN LATERAL (
  SELECT NULLIF(r.runtime ->> 'mode', '') AS execution_mode
  FROM public.captain_prompt_runs r
  WHERE r.session_id = s.id
  ORDER BY COALESCE(r.finished_at, r.started_at, r.created_at) DESC, r.id DESC
  LIMIT 1
) latest_run ON true;

COMMENT ON VIEW public.captain_session_overview_selections IS
  'Selection columns of captain_session_overview_compute; recomputed per session by the overview triggers.';

-- The full overview row, minus the clock-dependent duration_seconds. Its column
-- order is captain_session_overview_rows' leading columns.
CREATE OR REPLACE VIEW public.captain_session_overview_compute AS
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
  sel.history_file,
  sel.source_kind,
  sel.parser_version,
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
  sel.process_id,
  sel.pid,
  sel.process_status,
  sel.command,
  sel.process_cwd,
  sel.surface,
  sel.process_started_at,
  sel.last_heartbeat_at,
  sel.lease_owner,
  sel.lease_expires_at,
  sel.process_ended_at,
  sel.process_active,
  COALESCE(message_stats.message_count, 0) AS message_count,
  COALESCE(message_stats.tool_call_count, 0) AS tool_call_count,
  COALESCE(event_stats.event_count, 0) AS event_count,
  COALESCE(call_stats.turn_count, 0) AS turn_count,
  COALESCE(call_stats.model_call_count, 0) AS model_call_count,
  sel.agent_count,
  COALESCE(prompt_stats.prompt_run_count, 0) AS prompt_run_count,
  COALESCE(plan_stats.plan_count, 0) AS plan_count,
  COALESCE(request_stats.pending_request_count, 0) AS pending_request_count,
  COALESCE(request_stats.approved_request_count, 0) AS approved_request_count,
  COALESCE(request_stats.denied_request_count, 0) AS denied_request_count,
  COALESCE(file_stats.file_read_count, 0) AS file_read_count,
  COALESCE(file_stats.file_written_count, 0) AS file_written_count,
  sel.model,
  sel.model_provider,
  sel.model_mode,
  sel.effort,
  sel.context_tokens,
  sel.context_window_tokens,
  sel.context_free_percent,
  COALESCE(call_stats.input_tokens, 0) AS input_tokens,
  COALESCE(call_stats.output_tokens, 0) AS output_tokens,
  COALESCE(call_stats.reasoning_tokens, 0) AS reasoning_tokens,
  COALESCE(call_stats.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(call_stats.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.reasoning_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0) AS total_tokens,
  COALESCE(call_stats.cost_usd, 0::numeric) AS cost_usd,
  sel.process_source,
  sel.cpu_percent,
  sel.memory_percent,
  sel.memory_rss_bytes,
  sel.process_sampled_at,
  sel.execution_mode,
  COALESCE(call_stats.provider_cost_usd, 0::numeric) AS provider_cost_usd,
  COALESCE(call_stats.input_cost, 0::numeric) AS input_cost,
  COALESCE(call_stats.output_cost, 0::numeric) AS output_cost,
  COALESCE(call_stats.reasoning_cost, 0::numeric) AS reasoning_cost,
  COALESCE(call_stats.cache_read_cost, 0::numeric) AS cache_read_cost,
  COALESCE(call_stats.cache_write_cost, 0::numeric) AS cache_write_cost,
  s.claude_cli_cost_usd,
  s.claude_cli_cost_observed_at,
  s.parent_relation,
  -- Not part of the view: the session list's inactive-empty filter.
  sel.source_path_present,
  COALESCE(execution_stats.execution_run_count, 0) AS execution_run_count
FROM public.captain_sessions s
JOIN public.captain_session_overview_selections sel ON sel.id = s.id
LEFT JOIN LATERAL (
  SELECT
    count(*)::bigint AS message_count,
    COALESCE(sum(public.captain_message_tool_call_count(m.parts)), 0)::bigint AS tool_call_count
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
        ELSE c.input_cost + c.output_cost + c.reasoning_cost + c.cache_read_cost + c.cache_write_cost
      END
    ) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd,
    COALESCE(sum(c.provider_cost_usd) FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS provider_cost_usd,
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
  SELECT count(*)::bigint AS prompt_run_count
  FROM public.captain_prompt_runs r
  WHERE r.session_id = s.id
) prompt_stats ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS execution_run_count
  FROM public.captain_prompt_runs r
  WHERE r.execution_session_id = s.id
) execution_stats ON true
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
) file_stats ON true;

COMMENT ON VIEW public.captain_session_overview_compute IS
  'Authoritative aggregate definition of captain_session_overview_rows; read only by rebuilds and tests.';

-- Created once. A re-run (60 or this file changed) keeps the rows: a change to
-- what a column means must ship its own migration that rebuilds them.
DO $$
BEGIN
  IF to_regclass('public.captain_session_overview_rows') IS NOT NULL THEN
    RETURN;
  END IF;
  CREATE UNLOGGED TABLE public.captain_session_overview_rows AS
    SELECT * FROM public.captain_session_overview_compute WITH NO DATA;
  ALTER TABLE public.captain_session_overview_rows
    ADD CONSTRAINT captain_session_overview_rows_pkey PRIMARY KEY (id),
    ADD CONSTRAINT captain_session_overview_rows_id_fkey FOREIGN KEY (id)
      REFERENCES public.captain_sessions (id) ON DELETE CASCADE,
    ADD COLUMN activity_at timestamptz
      GENERATED ALWAYS AS (COALESCE(last_activity_at, started_at, created_at)) STORED,
    ADD COLUMN search_text text;
END;
$$;

COMMENT ON TABLE public.captain_session_overview_rows IS
  'Trigger-maintained captain_session_overview_compute rows. UNLOGGED: captain_session_overview_ensure() refills it after crash recovery.';

CREATE INDEX IF NOT EXISTS captain_session_overview_rows_activity_idx
  ON public.captain_session_overview_rows (activity_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_source_activity_idx
  ON public.captain_session_overview_rows (source, activity_at DESC);
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_project_activity_idx
  ON public.captain_session_overview_rows (project, activity_at DESC);
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_cwd_idx
  ON public.captain_session_overview_rows (cwd text_pattern_ops);
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_live_idx
  ON public.captain_session_overview_rows (activity_at DESC, id DESC)
  WHERE process_active;
-- Identity and thread lookups read the overview rows, not captain_sessions, so
-- they need the same provider-id and parent indexes the base table carries.
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_provider_session_id_idx
  ON public.captain_session_overview_rows (provider_session_id text_pattern_ops)
  WHERE provider_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS captain_session_overview_rows_parent_session_id_idx
  ON public.captain_session_overview_rows (parent_session_id, parent_relation)
  WHERE parent_session_id IS NOT NULL;

-- Captain never installs extensions. The list's substring search uses a
-- trigram index only where an operator already installed pg_trgm.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extname = 'pg_trgm') THEN
    EXECUTE 'CREATE INDEX IF NOT EXISTS captain_session_overview_rows_search_idx
      ON public.captain_session_overview_rows USING gin (search_text gin_trgm_ops)';
  END IF;
END;
$$;

-- The session list's free-text search matches against these columns.
CREATE OR REPLACE FUNCTION public.captain_session_overview_rows_search_text()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  NEW.search_text := concat_ws(' ', NEW.id::text, NEW.provider_session_id, NEW.source, NEW.provider,
    NEW.project, NEW.cwd, NEW.title, NEW.initial_prompt, NEW.slug, NEW.cli_version,
    NEW.git ->> 'branch', NEW.model, NEW.model_provider, NEW.model_mode, NEW.effort, NEW.pid::text,
    NEW.process_status, NEW.process_cwd, NEW.command);
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS captain_session_overview_rows_search_insert ON public.captain_session_overview_rows;
CREATE TRIGGER captain_session_overview_rows_search_insert
BEFORE INSERT ON public.captain_session_overview_rows
FOR EACH ROW EXECUTE FUNCTION public.captain_session_overview_rows_search_text();

DROP TRIGGER IF EXISTS captain_session_overview_rows_search_update ON public.captain_session_overview_rows;
CREATE TRIGGER captain_session_overview_rows_search_update
BEFORE UPDATE ON public.captain_session_overview_rows
FOR EACH ROW
WHEN ((OLD.provider_session_id, OLD.source, OLD.provider, OLD.project, OLD.cwd, OLD.title,
       OLD.initial_prompt, OLD.slug, OLD.cli_version, OLD.git ->> 'branch', OLD.model,
       OLD.model_provider, OLD.model_mode, OLD.effort, OLD.pid, OLD.process_status,
       OLD.process_cwd, OLD.command)
  IS DISTINCT FROM
      (NEW.provider_session_id, NEW.source, NEW.provider, NEW.project, NEW.cwd, NEW.title,
       NEW.initial_prompt, NEW.slug, NEW.cli_version, NEW.git ->> 'branch', NEW.model,
       NEW.model_provider, NEW.model_mode, NEW.effort, NEW.pid, NEW.process_status,
       NEW.process_cwd, NEW.command))
EXECUTE FUNCTION public.captain_session_overview_rows_search_text();

-- Recomputes the listed sessions (every session when ids is NULL) from
-- captain_session_overview_compute and returns how many rows it wrote.
--
-- Rows are first ensured to exist, then locked in id order, and only then
-- computed: under READ COMMITTED the compute statement's snapshot is taken
-- after any concurrent rebuild holding a row lock has committed, so the last
-- rebuild to commit saw every committed write. A session deleted meanwhile has
-- no row to fill and is skipped.
CREATE OR REPLACE FUNCTION public.captain_session_overview_rebuild(ids uuid[] DEFAULT NULL)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  target uuid[];
  rebuilt bigint;
BEGIN
  IF ids IS NULL THEN
    target := ARRAY(SELECT s.id FROM public.captain_sessions s ORDER BY s.id);
  ELSE
    target := ARRAY(SELECT DISTINCT i FROM unnest(ids) i WHERE i IS NOT NULL ORDER BY i);
  END IF;

  INSERT INTO public.captain_session_overview_rows (id)
  SELECT s.id FROM public.captain_sessions s WHERE s.id = ANY (target) ORDER BY s.id
  ON CONFLICT (id) DO NOTHING;

  PERFORM 1 FROM public.captain_session_overview_rows o
  WHERE o.id = ANY (target) ORDER BY o.id FOR UPDATE;

  UPDATE public.captain_session_overview_rows o
     SET (id, provider_session_id, source, provider, host_id, parent_session_id, root_session_id,
          thread_id, path, history_file, source_kind, parser_version, project, cwd, title,
          initial_prompt, slug, agent_type, description, cli_version, lifecycle_status,
          activity_state, health_state, state_reason, state_version, state_observed_at, git,
          metadata, started_at, ended_at, last_activity_at, created_at, updated_at, process_id,
          pid, process_status, command, process_cwd, surface, process_started_at,
          last_heartbeat_at, lease_owner, lease_expires_at, process_ended_at, process_active,
          message_count, tool_call_count, event_count, turn_count, model_call_count, agent_count,
          prompt_run_count, plan_count, pending_request_count, approved_request_count,
          denied_request_count, file_read_count, file_written_count, model, model_provider,
          model_mode, effort, context_tokens, context_window_tokens, context_free_percent,
          input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens,
          total_tokens, cost_usd, process_source, cpu_percent, memory_percent, memory_rss_bytes,
          process_sampled_at, execution_mode, provider_cost_usd, input_cost, output_cost,
          reasoning_cost, cache_read_cost, cache_write_cost, claude_cli_cost_usd,
          claude_cli_cost_observed_at, parent_relation, source_path_present, execution_run_count)
       = ROW(x.*)
    FROM public.captain_session_overview_compute x
   WHERE x.id = o.id
     AND o.id = ANY (target)
     -- Filtering the view side too pushes the ids into the view's
     -- captain_sessions scan; with only the overview side filtered the planner
     -- aggregated every session (measured: >11 min for a 500-id batch on 24k
     -- sessions, against 5.7 s with this predicate).
     AND x.id = ANY (target);
  GET DIAGNOSTICS rebuilt = ROW_COUNT;
  RETURN rebuilt;
END;
$$;

-- The rows a statement changed: its new rows, its old rows, or both for an
-- UPDATE (a row moved between sessions names both). PostgreSQL forbids
-- transition tables on a multi-event trigger, so each table has one trigger
-- per event, all naming them new_rows/old_rows; the queueing trigger (91)
-- interpolates this into EXECUTE, which sees them.
CREATE OR REPLACE FUNCTION public.captain_session_overview_changed(op text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
BEGIN
  CASE op
    WHEN 'INSERT' THEN
      RETURN '(SELECT n.* FROM new_rows n)';
    WHEN 'DELETE' THEN
      RETURN '(SELECT o.* FROM old_rows o)';
    WHEN 'UPDATE' THEN
      RETURN '(SELECT n.* FROM new_rows n UNION ALL SELECT o.* FROM old_rows o)';
    ELSE
      RAISE EXCEPTION 'captain overview cannot queue a % statement', op;
  END CASE;
END;
$$;

-- agent_count follows parent links below a root, so a session that is
-- inserted, deleted or reparented changes the count of every root above it.
-- Returns the seeds, all their ancestors, and those sessions' roots.
CREATE OR REPLACE FUNCTION public.captain_session_overview_lineage(seeds uuid[])
RETURNS uuid[]
LANGUAGE sql
STABLE
SET search_path = pg_catalog, public
AS $$
  WITH RECURSIVE up AS (
    SELECT s.id, s.parent_session_id, s.root_session_id
    FROM public.captain_sessions s
    WHERE s.id = ANY (seeds)
    UNION
    SELECT p.id, p.parent_session_id, p.root_session_id
    FROM public.captain_sessions p
    JOIN up ON p.id = up.parent_session_id
  )
  SELECT ARRAY(SELECT up.id FROM up UNION SELECT up.root_session_id FROM up WHERE up.root_session_id IS NOT NULL)
$$;

-- Maintenance is deferred to commit (91): source tables only queue the
-- sessions they touched. A re-run of this script must not resurrect the
-- immediate, in-transaction maintenance it used to install, which held a
-- session's overview row lock until commit and made unrelated writers queue
-- behind each other.
DO $$
DECLARE
  source_table text;
  event text;
BEGIN
  FOREACH source_table IN ARRAY ARRAY['captain_sessions', 'captain_session_processes',
    'captain_session_sources', 'captain_turns', 'captain_model_calls', 'captain_messages',
    'captain_events', 'captain_prompt_runs', 'captain_plans', 'captain_turn_requests',
    'captain_artifacts']
  LOOP
    FOREACH event IN ARRAY ARRAY['insert', 'update', 'delete'] LOOP
      EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I',
        source_table || '_session_overview_' || event || '_after', source_table);
    END LOOP;
  END LOOP;
END;
$$;

-- CASCADE: only triggers reference these, possibly under names an earlier
-- draft of this script used and the loop above does not know.
DROP FUNCTION IF EXISTS public.captain_session_overview_sessions_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_selections_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_turns_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_model_calls_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_messages_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_events_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_prompt_runs_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_plans_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_turn_requests_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_artifacts_sync() CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_refresh_selections(uuid[]) CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_add(jsonb) CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_sum_deltas(jsonb) CASCADE;
DROP FUNCTION IF EXISTS public.captain_session_overview_claim(uuid[]) CASCADE;

-- Readers of the list and detail surfaces LISTEN on
-- captain_session_overview_change; the payload is the session UUID, folded per
-- transaction. Liveness telemetry (heartbeats, leases, CPU/memory samples) and
-- updated_at change constantly without changing what a reader shows, so they
-- do not notify. Backfills that suppress captain_session_change also silence
-- this channel.
--
-- The same change calls the row hooks registered on
-- captain_session_overview_rows (90): a host projecting from the overview is
-- told once the commit-time flush (91) has settled the row, which a hook on
-- captain_sessions itself (a row trigger, so it fires first) is not.
-- Hooks are not suppressed during backfills; a host's projection must still be
-- exact.
CREATE OR REPLACE FUNCTION public.captain_notify_session_overview_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  PERFORM public.captain_call_row_hooks(TG_TABLE_NAME, TG_OP,
    ARRAY[NEW.id, NEW.id, COALESCE(NEW.root_session_id, NEW.id), NULL]);
  IF current_setting('captain.suppress_session_change', true) = 'on' THEN
    RETURN NULL;
  END IF;
  PERFORM pg_notify('captain_session_overview_change', NEW.id::text);
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS captain_session_overview_rows_notify_insert ON public.captain_session_overview_rows;
-- The rebuild inserts an id-only placeholder (created_at is NULL only there)
-- and fills it with an UPDATE in the same transaction; listeners and hooks hear
-- about the filled row, from the update, not about the placeholder.
CREATE TRIGGER captain_session_overview_rows_notify_insert
AFTER INSERT ON public.captain_session_overview_rows
FOR EACH ROW
WHEN (NEW.created_at IS NOT NULL)
EXECUTE FUNCTION public.captain_notify_session_overview_change();

DROP TRIGGER IF EXISTS captain_session_overview_rows_notify_update ON public.captain_session_overview_rows;
CREATE TRIGGER captain_session_overview_rows_notify_update
AFTER UPDATE ON public.captain_session_overview_rows
FOR EACH ROW
WHEN ((OLD.provider_session_id, OLD.source, OLD.provider, OLD.host_id, OLD.parent_session_id,
       OLD.root_session_id, OLD.path, OLD.history_file, OLD.source_kind, OLD.parser_version,
       OLD.project, OLD.cwd, OLD.title, OLD.initial_prompt, OLD.slug, OLD.agent_type,
       OLD.description, OLD.cli_version, OLD.lifecycle_status, OLD.activity_state,
       OLD.health_state, OLD.state_reason, OLD.state_version, OLD.git, OLD.metadata,
       OLD.started_at, OLD.ended_at, OLD.last_activity_at, OLD.process_id, OLD.pid,
       OLD.process_status, OLD.command, OLD.process_cwd, OLD.surface, OLD.process_started_at,
       OLD.lease_owner, OLD.process_ended_at, OLD.process_active, OLD.message_count,
       OLD.tool_call_count, OLD.event_count, OLD.turn_count, OLD.model_call_count, OLD.agent_count,
       OLD.prompt_run_count, OLD.plan_count, OLD.pending_request_count, OLD.approved_request_count,
       OLD.denied_request_count, OLD.file_read_count, OLD.file_written_count, OLD.model,
       OLD.model_provider, OLD.model_mode, OLD.effort, OLD.context_tokens,
       OLD.context_window_tokens, OLD.context_free_percent, OLD.total_tokens, OLD.cost_usd,
       OLD.process_source, OLD.execution_mode, OLD.provider_cost_usd, OLD.claude_cli_cost_usd,
       OLD.parent_relation, OLD.source_path_present, OLD.execution_run_count)
  IS DISTINCT FROM
      (NEW.provider_session_id, NEW.source, NEW.provider, NEW.host_id, NEW.parent_session_id,
       NEW.root_session_id, NEW.path, NEW.history_file, NEW.source_kind, NEW.parser_version,
       NEW.project, NEW.cwd, NEW.title, NEW.initial_prompt, NEW.slug, NEW.agent_type,
       NEW.description, NEW.cli_version, NEW.lifecycle_status, NEW.activity_state,
       NEW.health_state, NEW.state_reason, NEW.state_version, NEW.git, NEW.metadata,
       NEW.started_at, NEW.ended_at, NEW.last_activity_at, NEW.process_id, NEW.pid,
       NEW.process_status, NEW.command, NEW.process_cwd, NEW.surface, NEW.process_started_at,
       NEW.lease_owner, NEW.process_ended_at, NEW.process_active, NEW.message_count,
       NEW.tool_call_count, NEW.event_count, NEW.turn_count, NEW.model_call_count, NEW.agent_count,
       NEW.prompt_run_count, NEW.plan_count, NEW.pending_request_count, NEW.approved_request_count,
       NEW.denied_request_count, NEW.file_read_count, NEW.file_written_count, NEW.model,
       NEW.model_provider, NEW.model_mode, NEW.effort, NEW.context_tokens,
       NEW.context_window_tokens, NEW.context_free_percent, NEW.total_tokens, NEW.cost_usd,
       NEW.process_source, NEW.execution_mode, NEW.provider_cost_usd, NEW.claude_cli_cost_usd,
       NEW.parent_relation, NEW.source_path_present, NEW.execution_run_count))
EXECUTE FUNCTION public.captain_notify_session_overview_change();

-- Crash recovery truncates UNLOGGED tables, and a commit only recomputes the
-- sessions it touched. Rebuilds up to batch_limit sessions (all when NULL) that
-- have no overview row, most recently active first, and reports whether it
-- rebuilt any. Callers repeat until it returns false so no single transaction
-- holds row locks for the whole backfill.
CREATE OR REPLACE FUNCTION public.captain_session_overview_ensure(batch_limit integer DEFAULT NULL)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  missing uuid[];
BEGIN
  IF batch_limit IS NOT NULL AND batch_limit < 1 THEN
    RAISE EXCEPTION 'captain_session_overview_ensure batch_limit must be positive, got %', batch_limit;
  END IF;
  missing := ARRAY(
    SELECT s.id
    FROM public.captain_sessions s
    WHERE NOT EXISTS (SELECT 1 FROM public.captain_session_overview_rows o WHERE o.id = s.id)
    ORDER BY COALESCE(s.last_activity_at, s.started_at, s.created_at) DESC, s.id DESC
    LIMIT batch_limit
  );
  IF cardinality(missing) = 0 THEN
    RETURN false;
  END IF;
  PERFORM public.captain_session_overview_rebuild(missing);
  RETURN true;
END;
$$;

-- Same columns, names, types and order as before, so existing readers are
-- unchanged; only duration_seconds is computed at read time.
CREATE OR REPLACE VIEW public.captain_session_overview
WITH (security_barrier = true)
AS
SELECT
  o.id, o.provider_session_id, o.source, o.provider, o.host_id, o.parent_session_id,
  o.root_session_id, o.thread_id, o.path, o.history_file, o.source_kind, o.parser_version,
  o.project, o.cwd, o.title, o.initial_prompt, o.slug, o.agent_type, o.description,
  o.cli_version, o.lifecycle_status, o.activity_state, o.health_state, o.state_reason,
  o.state_version, o.state_observed_at, o.git, o.metadata, o.started_at, o.ended_at,
  o.last_activity_at, o.created_at, o.updated_at,
  CASE
    WHEN o.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(o.ended_at, clock_timestamp()) - o.started_at)
  END AS duration_seconds,
  o.process_id, o.pid, o.process_status, o.command, o.process_cwd, o.surface,
  o.process_started_at, o.last_heartbeat_at, o.lease_owner, o.lease_expires_at,
  o.process_ended_at, o.process_active, o.message_count, o.tool_call_count, o.event_count,
  o.turn_count, o.model_call_count, o.agent_count, o.prompt_run_count, o.plan_count,
  o.pending_request_count, o.approved_request_count, o.denied_request_count, o.file_read_count,
  o.file_written_count, o.model, o.model_provider, o.model_mode, o.effort, o.context_tokens,
  o.context_window_tokens, o.context_free_percent, o.input_tokens, o.output_tokens,
  o.reasoning_tokens, o.cache_read_tokens, o.cache_write_tokens, o.total_tokens, o.cost_usd,
  o.process_source, o.cpu_percent, o.memory_percent, o.memory_rss_bytes, o.process_sampled_at,
  o.execution_mode, o.provider_cost_usd, o.input_cost, o.output_cost, o.reasoning_cost,
  o.cache_read_cost, o.cache_write_cost, o.claude_cli_cost_usd, o.claude_cli_cost_observed_at,
  o.parent_relation
FROM public.captain_session_overview_rows o;

COMMENT ON VIEW public.captain_session_overview IS
  'One row per session for PostgREST list, metadata, health, live-process, usage and cost surfaces.';

-- Internal functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_session_overview_rows_search_text() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_session_overview_rebuild(uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_session_overview_changed(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_session_overview_lineage(uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_notify_session_overview_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_session_overview_ensure(integer) FROM PUBLIC;

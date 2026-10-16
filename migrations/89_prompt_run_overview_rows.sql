-- phase: post
-- dependsOn: 69_view_prompt_run_overview.sql, 90_row_hooks.sql

-- captain_prompt_run_overview used to evaluate four correlated LATERAL
-- aggregates per run (iterations, the latest iteration, model-call usage and
-- plans) behind a security barrier, so every reader paid for them on every run
-- it touched. The aggregates now live in captain_prompt_run_overview_rows, one
-- row per run recomputed at commit for every run a transaction touched (91
-- queues and flushes them), and the view only adds the values that depend on
-- the clock.
--
-- captain_prompt_run_overview_rebuild() is the single definition of a row:
-- refresh upserts what it returns. Edit the row definition here, never in 69:
-- 69 still creates the aggregate view on a fresh database, and this script
-- replaces it.
--
-- The table is UNLOGGED: it is derived state and a crash truncates it. Until
-- captain_prompt_run_overview_ensure() rebuilds it, every commit that touches
-- a run recomputes that run's missing row from source, so writers keep working
-- and each row they leave behind is exact.
-- Atlas does not model UNLOGGED tables; the migration bundle filters the drop
-- it plans for this undeclared table and never alters it.

CREATE UNLOGGED TABLE IF NOT EXISTS public.captain_prompt_run_overview_rows (
  id uuid NOT NULL PRIMARY KEY
    REFERENCES public.captain_prompt_runs (id) ON DELETE CASCADE,
  session_id uuid NOT NULL,
  root_session_id uuid NOT NULL,
  batch_id uuid,
  parent_run_id uuid,
  input_plan_id uuid,
  input_plan_revision_id uuid,
  origin text,
  spec_profile text,
  admission_key text,
  rendered_spec jsonb NOT NULL,
  prompt_markdown text,
  verification_markdown text,
  phase public.captain_prompt_run_phase NOT NULL,
  state public.captain_prompt_run_state NOT NULL,
  current_iteration integer NOT NULL,
  result_text text,
  result_json jsonb,
  error text,
  version bigint NOT NULL,
  queued_at timestamptz NOT NULL,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  iteration_count bigint NOT NULL,
  succeeded_iteration_count bigint NOT NULL,
  failed_iteration_count bigint NOT NULL,
  latest_iteration_id uuid,
  latest_iteration integer,
  latest_iteration_state public.captain_prompt_run_iteration_state,
  latest_iteration_feedback text,
  latest_verification_result jsonb,
  latest_iteration_error text,
  latest_iteration_started_at timestamptz,
  latest_iteration_finished_at timestamptz,
  model_call_count bigint NOT NULL,
  input_tokens bigint NOT NULL,
  output_tokens bigint NOT NULL,
  reasoning_tokens bigint NOT NULL,
  cache_read_tokens bigint NOT NULL,
  cache_write_tokens bigint NOT NULL,
  total_tokens bigint NOT NULL,
  cost_usd numeric NOT NULL,
  plan_count bigint NOT NULL,
  latest_plan_id uuid,
  latest_plan_approval_state public.captain_plan_approval_state,
  latest_plan_revision integer,
  execution_session_id uuid,
  turn_id uuid,
  runtime jsonb NOT NULL,
  provider_checkpoint_codec text,
  provider_checkpoint_version integer,
  provider_cost_usd numeric NOT NULL,
  input_cost numeric NOT NULL,
  output_cost numeric NOT NULL,
  reasoning_cost numeric NOT NULL,
  cache_read_cost numeric NOT NULL,
  cache_write_cost numeric NOT NULL,
  workspace jsonb,
  -- A verify-only run re-checks a workflow without prompting the agent.
  verify_only boolean NOT NULL,
  plan_mode boolean NOT NULL,
  pending_turn_request_count bigint NOT NULL
);

COMMENT ON TABLE public.captain_prompt_run_overview_rows IS
  'Trigger-maintained, crash-truncated prompt-run overview rows; read through captain_prompt_run_overview and rebuilt by captain_prompt_run_overview_ensure().';

-- ids NULL computes every run. The query is the former body of
-- captain_prompt_run_overview minus duration_seconds, plus the trailing
-- derived columns. It carries no SET clause so the planner can inline it into
-- refresh, and it filters with a plain = ANY so a generic plan still probes the
-- primary key instead of scanning every run.
CREATE OR REPLACE FUNCTION public.captain_prompt_run_overview_rebuild(ids uuid[] DEFAULT NULL)
RETURNS SETOF public.captain_prompt_run_overview_rows
LANGUAGE sql
STABLE
AS $$
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
  COALESCE(iteration_stats.iteration_count, 0),
  COALESCE(iteration_stats.succeeded_iteration_count, 0),
  COALESCE(iteration_stats.failed_iteration_count, 0),
  latest_iteration.id,
  latest_iteration.iteration,
  latest_iteration.state,
  latest_iteration.feedback,
  latest_iteration.verification_result,
  latest_iteration.error,
  latest_iteration.started_at,
  latest_iteration.finished_at,
  COALESCE(call_stats.model_call_count, 0),
  COALESCE(call_stats.input_tokens, 0),
  COALESCE(call_stats.output_tokens, 0),
  COALESCE(call_stats.reasoning_tokens, 0),
  COALESCE(call_stats.cache_read_tokens, 0),
  COALESCE(call_stats.cache_write_tokens, 0),
  COALESCE(call_stats.input_tokens, 0)
    + COALESCE(call_stats.output_tokens, 0)
    + COALESCE(call_stats.reasoning_tokens, 0)
    + COALESCE(call_stats.cache_read_tokens, 0)
    + COALESCE(call_stats.cache_write_tokens, 0),
  COALESCE(call_stats.cost_usd, 0::numeric),
  COALESCE(plan_stats.plan_count, 0),
  plan_stats.latest_plan_id,
  plan_stats.latest_plan_approval_state,
  plan_stats.latest_plan_revision,
  r.execution_session_id,
  r.turn_id,
  r.runtime,
  r.provider_checkpoint_codec,
  r.provider_checkpoint_version,
  COALESCE(call_stats.provider_cost_usd, 0::numeric),
  COALESCE(call_stats.input_cost, 0::numeric),
  COALESCE(call_stats.output_cost, 0::numeric),
  COALESCE(call_stats.reasoning_cost, 0::numeric),
  COALESCE(call_stats.cache_read_cost, 0::numeric),
  COALESCE(call_stats.cache_write_cost, 0::numeric),
  r.workspace,
  r.rendered_spec #> '{workflow,verify}' IS NOT NULL
    AND COALESCE(r.rendered_spec #>> '{prompt,user}', '') = '',
  COALESCE(r.rendered_spec #>> '{permissions,mode}' = 'plan', false),
  (
    SELECT count(*)
    FROM public.captain_turn_requests request
    WHERE request.prompt_run_id = r.id AND request.state = 'pending'
  )
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
) plan_stats ON true
WHERE r.id = ANY(COALESCE(ids, ARRAY(SELECT run.id FROM public.captain_prompt_runs run)))
$$;

-- refresh recomputes the given runs' rows from rebuild, and skips the write
-- (and so the change notification) for a row that is already current. A run
-- that no longer exists has no row: the foreign key's cascade removed it.
--
-- Refreshes of one run serialize on a transaction-scoped advisory lock taken
-- in id order before the compute statement, so under READ COMMITTED the last
-- refresh to commit saw every committed write, including for a row a crash
-- truncated (there is no row to lock yet). The lock is not a base-table row
-- lock, so a writer holding the run row never waits on a refresh and two
-- commit-time flushes cannot deadlock over run rows.
CREATE OR REPLACE FUNCTION public.captain_prompt_run_overview_refresh(p_ids uuid[])
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  ids uuid[];
BEGIN
  ids := ARRAY(SELECT DISTINCT id FROM unnest(p_ids) AS id WHERE id IS NOT NULL ORDER BY id);
  IF cardinality(ids) = 0 THEN
    RETURN;
  END IF;
  -- 0x4f565250 is "OVRP", the prompt-run overview's advisory lock class.
  PERFORM pg_advisory_xact_lock(1331057232, hashtext(id::text))
  FROM unnest(ids) WITH ORDINALITY AS run(id, position)
  ORDER BY position;
  INSERT INTO public.captain_prompt_run_overview_rows AS o
  SELECT * FROM public.captain_prompt_run_overview_rebuild(ids) ORDER BY id
  ON CONFLICT (id) DO UPDATE SET (
    session_id, root_session_id, batch_id, parent_run_id, input_plan_id,
    input_plan_revision_id, origin, spec_profile, admission_key, rendered_spec,
    prompt_markdown, verification_markdown, phase, state, current_iteration,
    result_text, result_json, error, version, queued_at, started_at, finished_at,
    created_at, updated_at, iteration_count, succeeded_iteration_count,
    failed_iteration_count, latest_iteration_id, latest_iteration,
    latest_iteration_state, latest_iteration_feedback, latest_verification_result,
    latest_iteration_error, latest_iteration_started_at, latest_iteration_finished_at,
    model_call_count, input_tokens, output_tokens, reasoning_tokens,
    cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, plan_count,
    latest_plan_id, latest_plan_approval_state, latest_plan_revision,
    execution_session_id, turn_id, runtime, provider_checkpoint_codec,
    provider_checkpoint_version, provider_cost_usd, input_cost, output_cost,
    reasoning_cost, cache_read_cost, cache_write_cost, workspace, verify_only,
    plan_mode, pending_turn_request_count
  ) = (
    EXCLUDED.session_id, EXCLUDED.root_session_id, EXCLUDED.batch_id, EXCLUDED.parent_run_id, EXCLUDED.input_plan_id,
    EXCLUDED.input_plan_revision_id, EXCLUDED.origin, EXCLUDED.spec_profile, EXCLUDED.admission_key, EXCLUDED.rendered_spec,
    EXCLUDED.prompt_markdown, EXCLUDED.verification_markdown, EXCLUDED.phase, EXCLUDED.state, EXCLUDED.current_iteration,
    EXCLUDED.result_text, EXCLUDED.result_json, EXCLUDED.error, EXCLUDED.version, EXCLUDED.queued_at, EXCLUDED.started_at, EXCLUDED.finished_at,
    EXCLUDED.created_at, EXCLUDED.updated_at, EXCLUDED.iteration_count, EXCLUDED.succeeded_iteration_count,
    EXCLUDED.failed_iteration_count, EXCLUDED.latest_iteration_id, EXCLUDED.latest_iteration,
    EXCLUDED.latest_iteration_state, EXCLUDED.latest_iteration_feedback, EXCLUDED.latest_verification_result,
    EXCLUDED.latest_iteration_error, EXCLUDED.latest_iteration_started_at, EXCLUDED.latest_iteration_finished_at,
    EXCLUDED.model_call_count, EXCLUDED.input_tokens, EXCLUDED.output_tokens, EXCLUDED.reasoning_tokens,
    EXCLUDED.cache_read_tokens, EXCLUDED.cache_write_tokens, EXCLUDED.total_tokens, EXCLUDED.cost_usd, EXCLUDED.plan_count,
    EXCLUDED.latest_plan_id, EXCLUDED.latest_plan_approval_state, EXCLUDED.latest_plan_revision,
    EXCLUDED.execution_session_id, EXCLUDED.turn_id, EXCLUDED.runtime, EXCLUDED.provider_checkpoint_codec,
    EXCLUDED.provider_checkpoint_version, EXCLUDED.provider_cost_usd, EXCLUDED.input_cost, EXCLUDED.output_cost,
    EXCLUDED.reasoning_cost, EXCLUDED.cache_read_cost, EXCLUDED.cache_write_cost, EXCLUDED.workspace, EXCLUDED.verify_only,
    EXCLUDED.plan_mode, EXCLUDED.pending_turn_request_count
  )
  WHERE ROW(o.*) IS DISTINCT FROM ROW(EXCLUDED.*);
END;
$$;

-- Maintenance is deferred to commit (91): source tables only queue the runs
-- they touched. A re-run of this script must not resurrect the immediate,
-- in-transaction maintenance it used to install, which held a run's row and
-- overview row locks until commit and deadlocked writers that never waited on
-- each other before.
DROP TRIGGER IF EXISTS captain_prompt_runs_overview_insert_after ON public.captain_prompt_runs;
DROP TRIGGER IF EXISTS captain_prompt_runs_overview_update_after ON public.captain_prompt_runs;
DROP TRIGGER IF EXISTS captain_prompt_run_iterations_overview_insert_after ON public.captain_prompt_run_iterations;
DROP TRIGGER IF EXISTS captain_prompt_run_iterations_overview_update_after ON public.captain_prompt_run_iterations;
DROP TRIGGER IF EXISTS captain_prompt_run_iterations_overview_delete_after ON public.captain_prompt_run_iterations;
DROP TRIGGER IF EXISTS captain_model_calls_overview_insert_after ON public.captain_model_calls;
DROP TRIGGER IF EXISTS captain_model_calls_overview_update_after ON public.captain_model_calls;
DROP TRIGGER IF EXISTS captain_model_calls_overview_delete_after ON public.captain_model_calls;
DROP TRIGGER IF EXISTS captain_plans_overview_insert_after ON public.captain_plans;
DROP TRIGGER IF EXISTS captain_plans_overview_update_after ON public.captain_plans;
DROP TRIGGER IF EXISTS captain_plans_overview_delete_after ON public.captain_plans;
DROP TRIGGER IF EXISTS captain_plan_revisions_overview_insert_after ON public.captain_plan_revisions;
DROP TRIGGER IF EXISTS captain_plan_revisions_overview_update_after ON public.captain_plan_revisions;
DROP TRIGGER IF EXISTS captain_plan_revisions_overview_delete_after ON public.captain_plan_revisions;
DROP TRIGGER IF EXISTS captain_turn_requests_overview_insert_after ON public.captain_turn_requests;
DROP TRIGGER IF EXISTS captain_turn_requests_overview_update_after ON public.captain_turn_requests;
DROP TRIGGER IF EXISTS captain_turn_requests_overview_delete_after ON public.captain_turn_requests;

-- CASCADE: only triggers reference these, possibly under names an earlier
-- draft of this script used.
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_runs() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_iterations() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_plans() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_plan_revisions() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_turn_requests() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_track_model_calls() CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_apply_call_deltas(public.captain_model_calls[], public.captain_model_calls[]) CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_call_deltas(public.captain_model_calls[], public.captain_model_calls[]) CASCADE;
DROP FUNCTION IF EXISTS public.captain_prompt_run_overview_lock(uuid[]) CASCADE;

-- Listeners re-read the run they are told about; the payload is the run id
-- alone, so PostgreSQL folds one transaction's repeated changes to a run into
-- one notification. refresh never rewrites an unchanged row, and the guard
-- keeps any other identical rewrite silent too.
CREATE OR REPLACE FUNCTION public.captain_prompt_run_overview_notify()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  -- Row hooks registered on this table (90) see the run once its overview row
  -- has settled at commit (91), which a hook on captain_prompt_runs (a row
  -- trigger, fired during the statement) does not.
  PERFORM public.captain_call_row_hooks(TG_TABLE_NAME, TG_OP,
    ARRAY[NEW.id, NEW.session_id, NEW.root_session_id, NEW.id]);
  PERFORM pg_notify('captain_prompt_run_overview_change', NEW.id::text);
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS captain_prompt_run_overview_rows_notify_insert_after ON public.captain_prompt_run_overview_rows;
CREATE TRIGGER captain_prompt_run_overview_rows_notify_insert_after
AFTER INSERT ON public.captain_prompt_run_overview_rows
FOR EACH ROW EXECUTE FUNCTION public.captain_prompt_run_overview_notify();

DROP TRIGGER IF EXISTS captain_prompt_run_overview_rows_notify_update_after ON public.captain_prompt_run_overview_rows;
CREATE TRIGGER captain_prompt_run_overview_rows_notify_update_after
AFTER UPDATE ON public.captain_prompt_run_overview_rows
FOR EACH ROW
WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION public.captain_prompt_run_overview_notify();

-- ensure rebuilds the rows a crash truncated: every prompt run without one.
-- Writes since the crash may already have recomputed some runs, so it looks
-- for missing rows rather than an empty table. The lock holds off writers
-- until the rebuild commits, and the check is repeated under it so concurrent
-- callers rebuild once.
CREATE OR REPLACE FUNCTION public.captain_prompt_run_overview_ensure()
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  missing uuid[];
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM public.captain_prompt_runs r
    WHERE NOT EXISTS (SELECT 1 FROM public.captain_prompt_run_overview_rows o WHERE o.id = r.id)
  ) THEN
    RETURN false;
  END IF;
  LOCK TABLE public.captain_prompt_run_overview_rows IN SHARE ROW EXCLUSIVE MODE;
  missing := ARRAY(
    SELECT r.id FROM public.captain_prompt_runs r
    WHERE NOT EXISTS (SELECT 1 FROM public.captain_prompt_run_overview_rows o WHERE o.id = r.id)
    ORDER BY r.id
  );
  IF cardinality(missing) = 0 THEN
    RETURN false;
  END IF;
  INSERT INTO public.captain_prompt_run_overview_rows
  SELECT * FROM public.captain_prompt_run_overview_rebuild(missing) ORDER BY id;
  RETURN true;
END;
$$;

-- Same column contract as 69; only duration_seconds reads the clock.
CREATE OR REPLACE VIEW public.captain_prompt_run_overview
WITH (security_barrier = true)
AS
SELECT
  o.id,
  o.session_id,
  o.root_session_id,
  o.batch_id,
  o.parent_run_id,
  o.input_plan_id,
  o.input_plan_revision_id,
  o.origin,
  o.spec_profile,
  o.admission_key,
  o.rendered_spec,
  o.prompt_markdown,
  o.verification_markdown,
  o.phase,
  o.state,
  o.current_iteration,
  o.result_text,
  o.result_json,
  o.error,
  o.version,
  o.queued_at,
  o.started_at,
  o.finished_at,
  o.created_at,
  o.updated_at,
  CASE
    WHEN o.started_at IS NULL THEN NULL
    ELSE EXTRACT(EPOCH FROM COALESCE(o.finished_at, clock_timestamp()) - o.started_at)
  END AS duration_seconds,
  o.iteration_count,
  o.succeeded_iteration_count,
  o.failed_iteration_count,
  o.latest_iteration_id,
  o.latest_iteration,
  o.latest_iteration_state,
  o.latest_iteration_feedback,
  o.latest_verification_result,
  o.latest_iteration_error,
  o.latest_iteration_started_at,
  o.latest_iteration_finished_at,
  o.model_call_count,
  o.input_tokens,
  o.output_tokens,
  o.reasoning_tokens,
  o.cache_read_tokens,
  o.cache_write_tokens,
  o.total_tokens,
  o.cost_usd,
  o.plan_count,
  o.latest_plan_id,
  o.latest_plan_approval_state,
  o.latest_plan_revision,
  o.execution_session_id,
  o.turn_id,
  o.runtime,
  o.provider_checkpoint_codec,
  o.provider_checkpoint_version,
  o.provider_cost_usd,
  o.input_cost,
  o.output_cost,
  o.reasoning_cost,
  o.cache_read_cost,
  o.cache_write_cost,
  o.workspace
FROM public.captain_prompt_run_overview_rows o;

COMMENT ON VIEW public.captain_prompt_run_overview IS
  'Prompt-run control-plane state with iteration, plan, usage, provider-reported/list-price cost, and verification summaries, and the finished run''s workspace record, read from the trigger-maintained captain_prompt_run_overview_rows.';

-- A first apply populates the table, and a re-run (69 or this script changed)
-- brings every row up to date with the current definition.
SELECT public.captain_prompt_run_overview_refresh(ARRAY(SELECT id FROM public.captain_prompt_runs));

-- Internal functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_prompt_run_overview_rebuild(uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_prompt_run_overview_refresh(uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_prompt_run_overview_notify() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_prompt_run_overview_ensure() FROM PUBLIC;

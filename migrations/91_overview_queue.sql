-- phase: post
-- dependsOn: 88_session_overview_rows.sql, 89_prompt_run_overview_rows.sql

-- The session (88) and prompt-run (89) overview rows are recomputed at commit,
-- not inside the statement that changed their sources. Maintaining them in the
-- writing statement held a run's or session's overview row lock until commit
-- for every transaction that wrote to it, so writers that never waited on each
-- other before queued behind each other, and an approval paused mid-transaction
-- deadlocked a run update that had to finish first.
--
-- Instead every source table queues the sessions and runs a statement touched,
-- keyed on the transaction, and one deferred constraint trigger flushes the
-- queue at commit through the exact recomputes 88 and 89 define. The queueing
-- triggers take no lock on any base-table or overview row, so a writer never
-- waits on another writer's overview maintenance; the overview row locks are
-- taken only at commit, in id order, for as long as the recompute runs.
--
-- Inside an open transaction the overview reflects what had committed when it
-- began; it is exact once the transaction commits. SET CONSTRAINTS ALL
-- IMMEDIATE flushes after each statement instead, and a later write in the
-- same transaction queues and flushes again.
--
-- The queueing triggers live here rather than in 88 and 89 so they can name
-- both recomputes' inputs in one place and depend on both scripts without a
-- cycle; 88 and 89 drop the immediate triggers they used to install by name.

-- Keying on the transaction keeps concurrent transactions from ever touching
-- each other's queue rows, and ON CONFLICT DO NOTHING folds one transaction's
-- repeated writes to a session or run into one entry, so a 10,000-message
-- ingest recomputes its session once. Rows live only for their transaction:
-- the flush deletes them, a rollback discards them, and being UNLOGGED costs
-- nothing because a crash also ends every transaction that wrote one.
CREATE UNLOGGED TABLE IF NOT EXISTS public.captain_overview_pending (
  txid xid8 NOT NULL,
  kind text NOT NULL CONSTRAINT captain_overview_pending_kind CHECK (kind IN ('session', 'prompt_run')),
  id uuid NOT NULL,
  CONSTRAINT captain_overview_pending_pkey PRIMARY KEY (txid, kind, id)
);

COMMENT ON TABLE public.captain_overview_pending IS
  'Sessions and prompt runs the current transaction touched, flushed into their overview rows at commit.';

CREATE OR REPLACE FUNCTION public.captain_overview_enqueue(p_kind text, p_ids uuid[])
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  INSERT INTO public.captain_overview_pending (txid, kind, id)
  SELECT DISTINCT pg_current_xact_id(), p_kind, queued.id
  FROM unnest(p_ids) AS queued(id)
  WHERE queued.id IS NOT NULL
  ON CONFLICT DO NOTHING
$$;

-- Queues what one statement touched. TG_ARGV holds (kind, expression) pairs;
-- each expression reads a changed row as c and names the session or run it
-- feeds, so a table that feeds both overviews, or one overview through two
-- columns, is one trigger. The expressions are this migration's own, never
-- caller input.
CREATE OR REPLACE FUNCTION public.captain_overview_queue()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  changed text := public.captain_session_overview_changed(TG_OP);
  branches text[] := '{}';
BEGIN
  IF TG_NARGS = 0 OR TG_NARGS % 2 <> 0 THEN
    RAISE EXCEPTION 'captain_overview_queue on % needs (kind, expression) pairs, got % arguments',
      TG_TABLE_NAME, TG_NARGS;
  END IF;
  FOR i IN 0 .. TG_NARGS - 1 BY 2 LOOP
    branches := branches || format('SELECT %L::text AS kind, (%s)::uuid AS id FROM %s c',
      TG_ARGV[i], TG_ARGV[i + 1], changed);
  END LOOP;
  EXECUTE format(
    'INSERT INTO public.captain_overview_pending (txid, kind, id)
     SELECT DISTINCT pg_current_xact_id(), q.kind, q.id FROM (%s) q
     WHERE q.id IS NOT NULL
     ON CONFLICT DO NOTHING',
    array_to_string(branches, ' UNION ALL '));
  RETURN NULL;
END;
$$;

-- A session row feeds its own overview, and agent_count follows parent links,
-- so an insert, a delete or a reparent also queues every root above it. Other
-- session updates (activity touches, state) leave the hierarchy alone.
CREATE OR REPLACE FUNCTION public.captain_overview_queue_sessions()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  CASE TG_OP
    WHEN 'INSERT' THEN
      PERFORM public.captain_overview_enqueue('session', ARRAY(SELECT n.id FROM new_rows n)
        || public.captain_session_overview_lineage(
          ARRAY(SELECT unnest(ARRAY[n.parent_session_id, n.root_session_id]) FROM new_rows n)));
    WHEN 'DELETE' THEN
      PERFORM public.captain_overview_enqueue('session', public.captain_session_overview_lineage(
        ARRAY(SELECT unnest(ARRAY[o.parent_session_id, o.root_session_id]) FROM old_rows o)));
    ELSE
      PERFORM public.captain_overview_enqueue('session', ARRAY(SELECT n.id FROM new_rows n)
        || public.captain_session_overview_lineage(ARRAY(
          SELECT unnest(ARRAY[n.parent_session_id, n.root_session_id, o.parent_session_id, o.root_session_id])
          FROM new_rows n
          JOIN old_rows o ON o.id = n.id
          WHERE (n.parent_session_id, n.root_session_id, n.parent_relation)
            IS DISTINCT FROM (o.parent_session_id, o.root_session_id, o.parent_relation))));
  END CASE;
  RETURN NULL;
END;
$$;

-- Runs first, then sessions, each as one id-sorted batch. The first firing in
-- a transaction takes every queued row; the rest find none and return. A
-- session or run deleted in the same transaction has no source row left, and
-- both recomputes skip it.
CREATE OR REPLACE FUNCTION public.captain_overview_flush()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  runs uuid[];
  sessions uuid[];
BEGIN
  WITH taken AS (
    DELETE FROM public.captain_overview_pending p
    WHERE p.txid = pg_current_xact_id()
    RETURNING p.kind, p.id
  )
  SELECT ARRAY(SELECT t.id FROM taken t WHERE t.kind = 'prompt_run' ORDER BY t.id),
         ARRAY(SELECT t.id FROM taken t WHERE t.kind = 'session' ORDER BY t.id)
    INTO runs, sessions;
  IF cardinality(runs) > 0 THEN
    PERFORM public.captain_prompt_run_overview_refresh(runs);
  END IF;
  IF cardinality(sessions) > 0 THEN
    PERFORM public.captain_session_overview_rebuild(sessions);
  END IF;
  RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS captain_overview_pending_flush ON public.captain_overview_pending;
CREATE CONSTRAINT TRIGGER captain_overview_pending_flush
AFTER INSERT ON public.captain_overview_pending
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION public.captain_overview_flush();

-- Statement-level with transition tables, one trigger per event. These run
-- whatever captain.suppress_session_change says: a backfill must still leave
-- both overviews exact.
DO $$
DECLARE
  binding record;
  event text;
  trigger_name text;
  arguments text;
BEGIN
  FOR binding IN
    SELECT * FROM (VALUES
      ('captain_sessions', 'captain_overview_queue_sessions', ARRAY[]::text[]),
      ('captain_session_processes', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      ('captain_session_sources', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      ('captain_turns', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      ('captain_messages', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      ('captain_events', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      ('captain_artifacts', 'captain_overview_queue', ARRAY['session', 'c.session_id']),
      -- A call whose turn this statement's cascade already deleted maps to no
      -- session; the turn's own trigger queued it.
      ('captain_model_calls', 'captain_overview_queue', ARRAY[
        'session', '(SELECT t.session_id FROM public.captain_turns t WHERE t.id = c.turn_id)',
        'prompt_run', 'c.prompt_run_id']),
      ('captain_prompt_runs', 'captain_overview_queue', ARRAY[
        'session', 'c.session_id',
        'session', 'c.execution_session_id',
        'prompt_run', 'c.id']),
      ('captain_prompt_run_iterations', 'captain_overview_queue', ARRAY['prompt_run', 'c.prompt_run_id']),
      ('captain_plans', 'captain_overview_queue', ARRAY[
        'session', 'c.source_session_id',
        'prompt_run', 'c.source_prompt_run_id']),
      -- A revision deleted by its plan's cascade finds no plan; the plan's own
      -- trigger queued that run.
      ('captain_plan_revisions', 'captain_overview_queue', ARRAY[
        'prompt_run', '(SELECT p.source_prompt_run_id FROM public.captain_plans p WHERE p.id = c.plan_id)']),
      -- A run counts only pending requests; any request feeds its session.
      ('captain_turn_requests', 'captain_overview_queue', ARRAY[
        'session', 'c.session_id',
        'prompt_run', 'CASE WHEN c.state = ''pending'' THEN c.prompt_run_id END'])
    ) AS b(table_name, function_name, queue_arguments)
  LOOP
    arguments := (SELECT string_agg(quote_literal(a), ', ' ORDER BY ord)
                  FROM unnest(binding.queue_arguments) WITH ORDINALITY AS arg(a, ord));
    FOREACH event IN ARRAY ARRAY['INSERT', 'UPDATE', 'DELETE'] LOOP
      trigger_name := binding.table_name || '_overview_queue_' || lower(event) || '_after';
      EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', trigger_name, binding.table_name);
      EXECUTE format(
        'CREATE TRIGGER %I AFTER %s ON public.%I REFERENCING %s FOR EACH STATEMENT EXECUTE FUNCTION public.%I(%s)',
        trigger_name, event, binding.table_name,
        CASE event
          WHEN 'INSERT' THEN 'NEW TABLE AS new_rows'
          WHEN 'UPDATE' THEN 'OLD TABLE AS old_rows NEW TABLE AS new_rows'
          ELSE 'OLD TABLE AS old_rows'
        END,
        binding.function_name, COALESCE(arguments, ''));
    END LOOP;
  END LOOP;
END;
$$;

-- Internal functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_overview_enqueue(text, uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_overview_queue() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_overview_queue_sessions() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_overview_flush() FROM PUBLIC;

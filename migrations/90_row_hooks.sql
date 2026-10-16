-- phase: post

-- Row hooks let a host keep a projection of Captain rows exact inside the
-- writer's transaction (see 23_row_hooks.pg.hcl), where captain_row_change
-- (85) only tells a listening host after commit. A registered hook is called
-- after every INSERT, UPDATE and DELETE of a row in a supported table --
-- including a row removed by a cascade -- and an error it raises aborts the
-- Captain write, exactly as a delete guard's rejection does (86).
--
-- Hook signature. A hook is
--
--   f(p_table text, p_op text, p_id uuid, p_session_id uuid,
--     p_root_session_id uuid, p_prompt_run_id uuid) RETURNS void
--
-- and receives the captain_row_change identity: the row's id plus the
-- session, family root and prompt run it belongs to, NULL where 85 leaves a
-- field absent. That is all a host needs to know which of its projected rows
-- to recompute, and the narrowest contract that lets it: unlike a jsonb copy
-- of OLD and NEW it does not tie hosts to Captain's column layout, and it
-- never detoasts a wide row the host would re-read anyway. An UPDATE that
-- moves a row between sessions or runs calls the hook with the old and then
-- the new identity; any other UPDATE calls it once. The hook runs AFTER the
-- row change, so it reads the row's new state (a deleted row is gone).
--
-- captain.suppress_session_change does not silence hooks: it keeps historical
-- backfills from waking transcript followers, whereas a host's projection of a
-- backfilled row must still be exact -- the same rule captain_row_change follows.

-- The tables a hook may be registered on; keep captain_row_hooks_target_table
-- in 23_row_hooks.pg.hcl in step. A source table added here gets its dispatch
-- trigger below, and captain_dispatch_row_hooks() needs a branch reading its
-- identity. The two overview tables (88, 89) are dispatched by their own
-- notify triggers instead, which fire only when a rendered column changed and
-- after the statement-level triggers have settled the row.
CREATE OR REPLACE FUNCTION public.captain_row_hook_tables()
RETURNS text[]
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT ARRAY['captain_sessions', 'captain_prompt_runs', 'captain_turn_requests', 'captain_prompt_run_iterations',
               'captain_session_overview_rows', 'captain_prompt_run_overview_rows'];
$$;

-- The stored form of a hook: schema-qualified and quoted, so it resolves to
-- the same function whatever search_path the writing session has.
CREATE OR REPLACE FUNCTION public.captain_row_hook_name(p_hook regprocedure)
RETURNS text
LANGUAGE sql
STABLE
SET search_path = pg_catalog, public
AS $$
  SELECT format('%I.%I(%s)', n.nspname, p.proname, oidvectortypes(p.proargtypes))
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
   WHERE p.oid = p_hook;
$$;

-- captain_register_row_hook is the only writer of captain_row_hooks. It
-- refuses a hook it could not call: a table without hook support, a function
-- of another signature or result, or no owner (a missing function already
-- fails the regprocedure cast). Registering the same hook again only updates
-- its owner.
CREATE OR REPLACE FUNCTION public.captain_register_row_hook(
  p_target_table text,
  p_hook regprocedure,
  p_owner text
)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  hook pg_catalog.pg_proc%ROWTYPE;
BEGIN
  IF p_target_table IS NULL OR NOT p_target_table = ANY (public.captain_row_hook_tables()) THEN
    RAISE EXCEPTION 'unsupported row hook table "%"; supported: %', p_target_table,
      array_to_string(public.captain_row_hook_tables(), ', ')
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_owner IS NULL OR btrim(p_owner) = '' THEN
    RAISE EXCEPTION 'row hook owner is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_hook IS NULL THEN
    RAISE EXCEPTION 'row hook function is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  SELECT * INTO hook FROM pg_catalog.pg_proc WHERE oid = p_hook;
  IF hook.prokind <> 'f' THEN
    RAISE EXCEPTION 'row hook % is not a function', p_hook USING ERRCODE = 'wrong_object_type';
  END IF;
  IF oidvectortypes(hook.proargtypes) <> 'text, text, uuid, uuid, uuid, uuid' THEN
    RAISE EXCEPTION 'row hook % must take (text, text, uuid, uuid, uuid, uuid)', p_hook
      USING ERRCODE = 'datatype_mismatch';
  END IF;
  IF hook.prorettype <> 'void'::regtype OR hook.proretset THEN
    RAISE EXCEPTION 'row hook % must return void, not %', p_hook, format_type(hook.prorettype, NULL)
      USING ERRCODE = 'datatype_mismatch';
  END IF;

  INSERT INTO public.captain_row_hooks (target_table, hook_function, owner)
  VALUES (p_target_table, public.captain_row_hook_name(p_hook), btrim(p_owner))
  ON CONFLICT (target_table, hook_function) DO UPDATE SET owner = EXCLUDED.owner;
END;
$$;

-- p_hook is text, not regprocedure, so a host can still remove a hook whose
-- function it has already dropped -- the state in which every write to the
-- table fails -- by naming it exactly as captain_row_hooks stores it.
CREATE OR REPLACE FUNCTION public.captain_unregister_row_hook(p_target_table text, p_hook text)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  hook_name text := COALESCE(public.captain_row_hook_name(to_regprocedure(p_hook)), p_hook);
BEGIN
  DELETE FROM public.captain_row_hooks
   WHERE target_table = p_target_table
     AND hook_function = hook_name;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'row hook % on % is not registered', hook_name, p_target_table
      USING ERRCODE = 'undefined_object';
  END IF;
END;
$$;

-- Calls every hook registered for p_table, in hook name order, with one
-- identity (id, session, root, prompt run). A hook whose function has since
-- been dropped fails the write loudly rather than silently skipping the host.
CREATE OR REPLACE FUNCTION public.captain_call_row_hooks(p_table text, p_op text, p_identity uuid[])
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  hook record;
BEGIN
  FOR hook IN
    SELECT h.hook_function, h.owner, format('%I.%I', n.nspname, p.proname) AS callee
      FROM public.captain_row_hooks h
      LEFT JOIN pg_catalog.pg_proc p ON p.oid = to_regprocedure(h.hook_function)
      LEFT JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
     WHERE h.target_table = p_table
     ORDER BY h.hook_function
  LOOP
    IF hook.callee IS NULL THEN
      RAISE EXCEPTION 'row hook % on % (owner %) no longer exists', hook.hook_function, p_table, hook.owner
        USING ERRCODE = 'undefined_function',
              HINT = format('Recreate the function or run captain_unregister_row_hook(%L, %L).', p_table, hook.hook_function);
    END IF;
    EXECUTE format('SELECT %s($1, $2, $3, $4, $5, $6)', hook.callee)
      USING p_table, p_op, p_identity[1], p_identity[2], p_identity[3], p_identity[4];
  END LOOP;
END;
$$;

-- SECURITY DEFINER so hooks run as the schema owner, as a trigger a host owned
-- would, whatever role issues the write. A table with no hook registered pays
-- one primary-key EXISTS probe per row: a trigger WHEN clause cannot read
-- another table, so the registry cannot gate the trigger itself. Each table's
-- identity is read in its own branch, as 85 does, so a column reference is
-- only ever planned against the row type that has it.
CREATE OR REPLACE FUNCTION public.captain_dispatch_row_hooks()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  old_identity uuid[];
  new_identity uuid[];
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.captain_row_hooks WHERE target_table = TG_TABLE_NAME) THEN
    RETURN NULL;
  END IF;
  CASE TG_TABLE_NAME
    WHEN 'captain_sessions' THEN
      IF TG_OP <> 'INSERT' THEN old_identity := ARRAY[OLD.id, OLD.id, COALESCE(OLD.root_session_id, OLD.id), NULL]; END IF;
      IF TG_OP <> 'DELETE' THEN new_identity := ARRAY[NEW.id, NEW.id, COALESCE(NEW.root_session_id, NEW.id), NULL]; END IF;
    WHEN 'captain_prompt_runs' THEN
      IF TG_OP <> 'INSERT' THEN old_identity := ARRAY[OLD.id, OLD.session_id, OLD.root_session_id, OLD.id]; END IF;
      IF TG_OP <> 'DELETE' THEN new_identity := ARRAY[NEW.id, NEW.session_id, NEW.root_session_id, NEW.id]; END IF;
    WHEN 'captain_turn_requests' THEN
      IF TG_OP <> 'INSERT' THEN old_identity := ARRAY[OLD.id, OLD.session_id, NULL, OLD.prompt_run_id]; END IF;
      IF TG_OP <> 'DELETE' THEN new_identity := ARRAY[NEW.id, NEW.session_id, NULL, NEW.prompt_run_id]; END IF;
    WHEN 'captain_prompt_run_iterations' THEN
      IF TG_OP <> 'INSERT' THEN old_identity := ARRAY[OLD.id, NULL, NULL, OLD.prompt_run_id]; END IF;
      IF TG_OP <> 'DELETE' THEN new_identity := ARRAY[NEW.id, NULL, NULL, NEW.prompt_run_id]; END IF;
  END CASE;
  IF old_identity IS NOT NULL THEN
    PERFORM public.captain_call_row_hooks(TG_TABLE_NAME, TG_OP, old_identity);
  END IF;
  IF new_identity IS NOT NULL AND new_identity IS DISTINCT FROM old_identity THEN
    PERFORM public.captain_call_row_hooks(TG_TABLE_NAME, TG_OP, new_identity);
  END IF;
  RETURN NULL;
END;
$$;

DO $$
DECLARE
  target text;
BEGIN
  FOREACH target IN ARRAY public.captain_row_hook_tables() LOOP
    CONTINUE WHEN target LIKE '%\_overview\_rows';
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON public.%I', target || '_row_hooks_after', target);
    EXECUTE format('CREATE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON public.%I '
      'FOR EACH ROW EXECUTE FUNCTION public.captain_dispatch_row_hooks()', target || '_row_hooks_after', target);
  END LOOP;
END;
$$;

-- Registration is a host API reached through the application role, not a
-- PostgREST RPC endpoint; the dispatch functions are internal.
REVOKE ALL ON FUNCTION public.captain_register_row_hook(text, regprocedure, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_unregister_row_hook(text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_call_row_hooks(text, text, uuid[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_dispatch_row_hooks() FROM PUBLIC;

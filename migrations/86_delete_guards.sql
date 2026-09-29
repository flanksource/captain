-- phase: post

-- Delete guards replace a host's ON DELETE RESTRICT foreign keys onto Captain
-- rows (see 22_delete_guards.pg.hcl). A registered guard makes deleting a
-- captain_prompt_runs or captain_plans row -- directly or through a session
-- cascade -- fail while any row of the registered host column still holds its
-- id. The rejection carries SQLSTATE 23503 (foreign_key_violation) and the
-- constraint name captain_delete_guard, as the foreign key's did.

-- captain_register_delete_guard is the only writer of captain_delete_guards.
-- It refuses a guard it could not enforce: a table without guard support, a
-- host relation or column that does not exist, or a column that cannot hold a
-- Captain id. Registering the same host column again only updates its owner.
CREATE OR REPLACE FUNCTION public.captain_register_delete_guard(
  p_target_table text,
  p_host_table text,
  p_host_column text,
  p_owner text
)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  host_relation regclass;
  host_name text;
  column_type text;
BEGIN
  IF p_target_table IS NULL OR p_target_table NOT IN ('captain_prompt_runs', 'captain_plans') THEN
    RAISE EXCEPTION 'unsupported delete guard table "%"; supported: captain_prompt_runs, captain_plans', p_target_table
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_owner IS NULL OR btrim(p_owner) = '' THEN
    RAISE EXCEPTION 'delete guard owner is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  host_relation := to_regclass(p_host_table);
  IF host_relation IS NULL THEN
    RAISE EXCEPTION 'delete guard host table "%" does not exist', p_host_table
      USING ERRCODE = 'undefined_table';
  END IF;
  SELECT format('%I.%I', n.nspname, c.relname)
    INTO host_name
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
   WHERE c.oid = host_relation
     AND c.relkind IN ('r', 'p');
  IF host_name IS NULL THEN
    RAISE EXCEPTION 'delete guard host relation "%" is not a table', p_host_table
      USING ERRCODE = 'wrong_object_type';
  END IF;
  SELECT format_type(a.atttypid, a.atttypmod)
    INTO column_type
    FROM pg_catalog.pg_attribute a
   WHERE a.attrelid = host_relation
     AND a.attname = p_host_column
     AND a.attnum > 0
     AND NOT a.attisdropped;
  IF column_type IS NULL THEN
    RAISE EXCEPTION 'delete guard host table % has no column "%"', host_name, p_host_column
      USING ERRCODE = 'undefined_column';
  END IF;
  IF column_type <> 'uuid' THEN
    RAISE EXCEPTION 'delete guard host column %.% must be uuid, not %', host_name, p_host_column, column_type
      USING ERRCODE = 'datatype_mismatch';
  END IF;

  INSERT INTO public.captain_delete_guards (target_table, host_table, host_column, owner)
  VALUES (p_target_table, host_name, p_host_column, btrim(p_owner))
  ON CONFLICT (target_table, host_table, host_column) DO UPDATE SET owner = EXCLUDED.owner;
END;
$$;

-- SECURITY DEFINER so the check reads the host table as the schema owner, the
-- way a foreign key's RI check does, whatever role issues the delete. The host
-- name is re-parsed through regclass and the column quoted with %I, so a row in
-- captain_delete_guards can only ever name a relation, never inject SQL; a
-- guard whose host table has since been dropped fails the delete loudly rather
-- than silently allowing it.
CREATE OR REPLACE FUNCTION public.captain_enforce_delete_guards()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  guard record;
  referenced boolean;
BEGIN
  FOR guard IN
    SELECT g.host_table, g.host_column, g.owner
      FROM public.captain_delete_guards g
     WHERE g.target_table = TG_TABLE_NAME
     ORDER BY g.host_table, g.host_column
  LOOP
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM %s WHERE %I = $1)', guard.host_table::regclass, guard.host_column)
      INTO referenced
      USING OLD.id;
    IF referenced THEN
      RAISE EXCEPTION USING
        ERRCODE = 'foreign_key_violation',
        CONSTRAINT = 'captain_delete_guard',
        SCHEMA = TG_TABLE_SCHEMA,
        TABLE = TG_TABLE_NAME,
        MESSAGE = format('delete from %s rejected: %s is still referenced by %s.%s (owner %s)',
          TG_TABLE_NAME, OLD.id, guard.host_table, guard.host_column, guard.owner),
        HINT = format('Remove the %s reference in %s before deleting the Captain row.', guard.owner, guard.host_table);
    END IF;
  END LOOP;
  RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS captain_prompt_runs_delete_guard_before ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_delete_guard_before
BEFORE DELETE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_enforce_delete_guards();

DROP TRIGGER IF EXISTS captain_plans_delete_guard_before ON public.captain_plans;
CREATE TRIGGER captain_plans_delete_guard_before
BEFORE DELETE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_enforce_delete_guards();

-- Registration is a host API reached through the application role, not a
-- PostgREST RPC endpoint; the trigger function is internal.
REVOKE ALL ON FUNCTION public.captain_register_delete_guard(text, text, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_enforce_delete_guards() FROM PUBLIC;

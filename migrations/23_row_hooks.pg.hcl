# Host functions Captain calls, in the writer's transaction, for every row
# change of a Captain table a host projects from.
#
# A host that shares the database (gavel's todo_issue_overview) must not add
# DDL to Captain's tables, so it cannot own a trigger on them, and the async
# captain_row_change channel (85_row_change_notify.sql) only reaches a host
# while it is listening. It registers a function here instead, through
# database.RegisterRowHook or captain_register_row_hook(), and
# 90_row_hooks.sql calls it from an AFTER ROW trigger, so the host's projection
# commits or rolls back with the Captain write itself.

table "captain_row_hooks" {
  schema = schema.public

  column "target_table" {
    null = false
    type = text
  }
  # Schema-qualified and quoted as format('%I.%I(%s)') renders a regprocedure,
  # so the hook resolves the same function whatever search_path the writing
  # session has.
  column "hook_function" {
    null = false
    type = text
  }
  # Who registered the hook; named when a hook can no longer be resolved.
  column "owner" {
    null = false
    type = text
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.target_table, column.hook_function]
  }

  # Keep in step with captain_row_hook_tables() in 90_row_hooks.sql.
  check "captain_row_hooks_target_table" {
    expr = "target_table IN ('captain_sessions', 'captain_prompt_runs', 'captain_turn_requests', 'captain_prompt_run_iterations', 'captain_session_overview_rows', 'captain_prompt_run_overview_rows')"
  }
  check "captain_row_hooks_owner" {
    expr = "btrim(owner) <> ''"
  }
}

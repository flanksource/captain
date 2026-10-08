# Host references to Captain rows, declared instead of foreign keys.
#
# A host that shares the database (gavel's todo links) must not add DDL to
# Captain's tables, so it cannot own a foreign key onto them. It registers the
# host column that references a Captain row here instead, through
# database.RegisterDeleteGuard or captain_register_delete_guard(), and
# 86_delete_guards.sql rejects deleting that row -- directly or by a session
# cascade -- while any host row still names it, exactly as ON DELETE RESTRICT
# would. The host's own table stays the single source of truth for which rows
# are referenced; this table records only which columns to look in.

table "captain_delete_guards" {
  schema = schema.public

  column "target_table" {
    null = false
    type = text
  }
  # Schema-qualified and quoted as format('%I.%I') renders it, so the guard
  # resolves the same relation whatever search_path the deleting session has.
  column "host_table" {
    null = false
    type = text
  }
  column "host_column" {
    null = false
    type = text
  }
  # Who registered the guard; named in the rejection so the operator knows
  # which host's link to remove.
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
    columns = [column.target_table, column.host_table, column.host_column]
  }

  check "captain_delete_guards_target_table" {
    expr = "target_table IN ('captain_prompt_runs', 'captain_plans')"
  }
  check "captain_delete_guards_owner" {
    expr = "btrim(owner) <> ''"
  }
}

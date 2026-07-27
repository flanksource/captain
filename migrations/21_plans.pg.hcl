table "captain_plans" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "source_session_id" {
    null = false
    type = uuid
  }
  column "source_prompt_run_id" {
    null = true
    type = uuid
  }
  column "source_iteration_id" {
    null = true
    type = uuid
  }
  column "source_turn_id" {
    null = true
    type = uuid
  }
  column "title" {
    null = true
    type = text
  }
  column "slug" {
    null = true
    type = text
  }
  column "path" {
    null = true
    type = text
  }
  column "variant" {
    null = true
    type = text
  }
  column "spec_profile" {
    null = true
    type = text
  }
  column "approval_state" {
    null    = false
    type    = enum.captain_plan_approval_state
    default = "pending"
  }
  column "approved_revision_id" {
    null = true
    type = uuid
  }
  column "approval_comment" {
    null = true
    type = text
  }
  column "approved_by" {
    null = true
    type = text
  }
  column "approval_created_at" {
    null = true
    type = timestamptz
  }
  column "feedback_at" {
    null = true
    type = timestamptz
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "updated_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_plans_source_session_id_fkey" {
    columns     = [column.source_session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_plans_source_prompt_run_id_fkey" {
    columns     = [column.source_prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }
  foreign_key "captain_plans_source_iteration_id_fkey" {
    columns = [
      column.source_prompt_run_id,
      column.source_iteration_id,
    ]
    ref_columns = [
      table.captain_prompt_run_iterations.column.prompt_run_id,
      table.captain_prompt_run_iterations.column.id,
    ]
    on_update = NO_ACTION
    on_delete = NO_ACTION
  }
  foreign_key "captain_plans_source_turn_id_fkey" {
    columns     = [column.source_turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_plans_approved_revision_id_fkey" {
    columns = [
      column.id,
      column.approved_revision_id,
    ]
    ref_columns = [
      table.captain_plan_revisions.column.plan_id,
      table.captain_plan_revisions.column.id,
    ]
    on_update = NO_ACTION
    on_delete = NO_ACTION
  }

  index "captain_plans_source_session_id_idx" {
    columns = [column.source_session_id]
  }
  index "captain_plans_source_prompt_run_id_idx" {
    columns = [column.source_prompt_run_id]
  }
  index "captain_plans_prompt_variant_key" {
    unique  = true
    columns = [column.source_prompt_run_id, column.variant]
    where   = "source_prompt_run_id IS NOT NULL AND variant IS NOT NULL"
  }
  index "captain_plans_approval_state_idx" {
    columns = [column.approval_state, column.updated_at]
  }

  check "captain_plans_source_iteration_has_run" {
    expr = "source_iteration_id IS NULL OR source_prompt_run_id IS NOT NULL"
  }
}

table "captain_plan_revisions" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "plan_id" {
    null = false
    type = uuid
  }
  column "revision" {
    null = false
    type = integer
  }
  column "plan_markdown" {
    null = false
    type = text
  }
  column "content_hash" {
    null = false
    type = text
  }
  column "feedback" {
    null = true
    type = text
  }
  column "created_by" {
    null = true
    type = text
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_plan_revisions_plan_id_fkey" {
    columns     = [column.plan_id]
    ref_columns = [table.captain_plans.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_plan_revisions_plan_revision_key" {
    unique  = true
    columns = [column.plan_id, column.revision]
  }
  index "captain_plan_revisions_plan_hash_key" {
    unique  = true
    columns = [column.plan_id, column.content_hash]
  }
  index "captain_plan_revisions_plan_id_key" {
    unique  = true
    columns = [column.plan_id, column.id]
  }

  check "captain_plan_revisions_revision_positive" {
    expr = "revision > 0"
  }
  check "captain_plan_revisions_content_nonempty" {
    expr = "length(btrim(plan_markdown)) > 0"
  }
}

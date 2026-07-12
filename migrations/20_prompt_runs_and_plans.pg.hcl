table "captain_prompt_runs" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "session_id" {
    null = false
    type = uuid
  }
  column "root_session_id" {
    null = false
    type = uuid
  }
  column "batch_id" {
    null = true
    type = uuid
  }
  column "parent_run_id" {
    null = true
    type = uuid
  }
  column "input_plan_id" {
    null = true
    type = uuid
  }
  column "input_plan_revision_id" {
    null = true
    type = uuid
  }
  column "origin" {
    null = true
    type = text
  }
  column "spec_profile" {
    null = true
    type = text
  }
  column "admission_key" {
    null = true
    type = text
  }
  column "rendered_spec" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "prompt_markdown" {
    null = true
    type = text
  }
  column "verification_markdown" {
    null = true
    type = text
  }
  column "phase" {
    null    = false
    type    = enum.captain_prompt_run_phase
    default = "queued"
  }
  column "state" {
    null    = false
    type    = enum.captain_prompt_run_state
    default = "pending"
  }
  column "current_iteration" {
    null    = false
    type    = integer
    default = 0
  }
  column "result_text" {
    null = true
    type = text
  }
  column "result_json" {
    null = true
    type = jsonb
  }
  column "error" {
    null = true
    type = text
  }
  column "version" {
    null    = false
    type    = bigint
    default = 0
  }
  column "queued_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "finished_at" {
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

  foreign_key "captain_prompt_runs_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_prompt_runs_root_session_id_fkey" {
    columns     = [column.root_session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_prompt_runs_parent_run_id_fkey" {
    columns     = [column.parent_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_prompt_runs_input_plan_id_fkey" {
    columns     = [column.input_plan_id]
    ref_columns = [table.captain_plans.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }
  foreign_key "captain_prompt_runs_input_plan_revision_id_fkey" {
    columns = [
      column.input_plan_id,
      column.input_plan_revision_id,
    ]
    ref_columns = [
      table.captain_plan_revisions.column.plan_id,
      table.captain_plan_revisions.column.id,
    ]
    on_update = NO_ACTION
    on_delete = NO_ACTION
  }

  index "captain_prompt_runs_active_root_key" {
    unique  = true
    columns = [column.root_session_id]
    where   = "state IN ('pending', 'running', 'waiting')"
  }
  index "captain_prompt_runs_admission_key" {
    unique  = true
    columns = [column.admission_key]
    where   = "admission_key IS NOT NULL"
  }
  index "captain_prompt_runs_batch_id_idx" {
    columns = [column.batch_id]
  }
  index "captain_prompt_runs_parent_run_id_idx" {
    columns = [column.parent_run_id]
  }
  index "captain_prompt_runs_input_plan_id_idx" {
    columns = [column.input_plan_id]
  }
  index "captain_prompt_runs_state_idx" {
    columns = [column.state, column.phase, column.updated_at]
  }

  check "captain_prompt_runs_iteration_nonnegative" {
    expr = "current_iteration >= 0"
  }
  check "captain_prompt_runs_version_nonnegative" {
    expr = "version >= 0"
  }
  check "captain_prompt_runs_parent_not_self" {
    expr = "parent_run_id IS NULL OR parent_run_id <> id"
  }
  check "captain_prompt_runs_input_plan_revision_has_plan" {
    expr = "input_plan_revision_id IS NULL OR input_plan_id IS NOT NULL"
  }
  check "captain_prompt_runs_time_order" {
    expr = "(started_at IS NULL OR started_at >= queued_at) AND (finished_at IS NULL OR (started_at IS NOT NULL AND finished_at >= started_at))"
  }
}

table "captain_prompt_run_iterations" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "prompt_run_id" {
    null = false
    type = uuid
  }
  column "iteration" {
    null = false
    type = integer
  }
  column "state" {
    null    = false
    type    = enum.captain_prompt_run_iteration_state
    default = "pending"
  }
  column "request" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "feedback" {
    null = true
    type = text
  }
  column "verification_result" {
    null = true
    type = jsonb
  }
  column "error" {
    null = true
    type = text
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "finished_at" {
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

  foreign_key "captain_prompt_run_iterations_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_prompt_run_iterations_run_iteration_key" {
    unique  = true
    columns = [column.prompt_run_id, column.iteration]
  }
  index "captain_prompt_run_iterations_run_id_key" {
    unique  = true
    columns = [column.prompt_run_id, column.id]
  }
  index "captain_prompt_run_iterations_state_idx" {
    columns = [column.state, column.updated_at]
  }

  check "captain_prompt_run_iterations_iteration_nonnegative" {
    expr = "iteration >= 0"
  }
  check "captain_prompt_run_iterations_time_order" {
    expr = "finished_at IS NULL OR (started_at IS NOT NULL AND finished_at >= started_at)"
  }
}

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

table "captain_turns" {
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
  column "provider_turn_id" {
    null = true
    type = text
  }
  column "turn_index" {
    null = false
    type = integer
  }
  column "description" {
    null = true
    type = text
  }
  column "status" {
    null    = false
    type    = enum.captain_turn_status
    default = "open"
  }
  column "stop_reason" {
    null = true
    type = text
  }
  column "error" {
    null = true
    type = text
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "ended_at" {
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

  foreign_key "captain_turns_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_turns_session_turn_index_key" {
    unique  = true
    columns = [column.session_id, column.turn_index]
  }
  index "captain_turns_provider_turn_id_key" {
    unique  = true
    columns = [column.session_id, column.provider_turn_id]
    where   = "provider_turn_id IS NOT NULL"
  }
  index "captain_turns_open_session_key" {
    unique  = true
    columns = [column.session_id]
    where   = "status = 'open'"
  }
  index "captain_turns_started_at_idx" {
    columns = [column.started_at]
  }

  check "captain_turns_index_nonnegative" {
    expr = "turn_index >= 0"
  }
  check "captain_turns_time_order" {
    expr = "ended_at IS NULL OR (started_at IS NOT NULL AND ended_at >= started_at)"
  }
}
table "captain_model_calls" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "turn_id" {
    null = false
    type = uuid
  }
  column "prompt_run_id" {
    null = true
    type = uuid
  }
  column "iteration_id" {
    null = true
    type = uuid
  }
  column "provider_call_id" {
    null = true
    type = text
  }
  column "call_index" {
    null = false
    type = integer
  }
  column "model" {
    null = false
    type = text
  }
  column "backend" {
    null = false
    type = text
  }
  column "effort" {
    null = true
    type = text
  }
  column "status" {
    null    = false
    type    = enum.captain_model_call_status
    default = "pending"
  }
  column "stop_reason" {
    null = true
    type = text
  }
  column "input_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "output_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "reasoning_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "cache_read_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "cache_write_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "context_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "context_window_tokens" {
    null    = false
    type    = bigint
    default = 0
  }
  column "input_cost" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "output_cost" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "reasoning_cost" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "cache_read_cost" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "cache_write_cost" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "provider_cost_usd" {
    null    = false
    type    = numeric(20, 8)
    default = 0
  }
  column "currency" {
    null    = false
    type    = text
    default = "USD"
  }
  column "pricing_version" {
    null = true
    type = text
  }
  column "pricing_snapshot" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "request_hash" {
    null = true
    type = text
  }
  column "error" {
    null = true
    type = text
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "ended_at" {
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

  foreign_key "captain_model_calls_turn_id_fkey" {
    columns     = [column.turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_model_calls_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = NO_ACTION
  }
  foreign_key "captain_model_calls_iteration_id_fkey" {
    columns = [
      column.prompt_run_id,
      column.iteration_id,
    ]
    ref_columns = [
      table.captain_prompt_run_iterations.column.prompt_run_id,
      table.captain_prompt_run_iterations.column.id,
    ]
    on_update = NO_ACTION
    on_delete = NO_ACTION
  }

  index "captain_model_calls_turn_call_index_key" {
    unique  = true
    columns = [column.turn_id, column.call_index]
  }
  index "captain_model_calls_provider_call_id_key" {
    unique  = true
    columns = [column.turn_id, column.provider_call_id]
    where   = "provider_call_id IS NOT NULL"
  }
  # Kept despite never being scanned by a query: it is the referencing side of
  # both captain_model_calls_prompt_run_id_fkey and the composite
  # captain_model_calls_iteration_id_fkey, so without it every prompt-run or
  # iteration delete becomes a sequential scan of this table. The composite FK
  # is served by this index's leading column, which is why there is no separate
  # iteration_id index -- that one was dead in both senses and is gone.
  index "captain_model_calls_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
  }

  # captain_model_calls_started_at_idx, captain_model_calls_model_idx and
  # captain_model_calls_iteration_id_idx were removed. This table served 18.5M
  # index scans in one measurement window and not one of them touched these
  # three; a second, independent window agreed. No foreign key depends on them
  # either -- see the note above for why the composite iteration FK does not.

  check "captain_model_calls_index_nonnegative" {
    expr = "call_index >= 0"
  }
  check "captain_model_calls_tokens_nonnegative" {
    expr = "input_tokens >= 0 AND output_tokens >= 0 AND reasoning_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0 AND context_tokens >= 0 AND context_window_tokens >= 0"
  }
  check "captain_model_calls_costs_nonnegative" {
    expr = "input_cost >= 0 AND output_cost >= 0 AND reasoning_cost >= 0 AND cache_read_cost >= 0 AND cache_write_cost >= 0 AND provider_cost_usd >= 0"
  }
  check "captain_model_calls_currency" {
    expr = "length(currency) = 3"
  }
  check "captain_model_calls_time_order" {
    expr = "ended_at IS NULL OR (started_at IS NOT NULL AND ended_at >= started_at)"
  }
  check "captain_model_calls_iteration_has_run" {
    expr = "iteration_id IS NULL OR prompt_run_id IS NOT NULL"
  }
}

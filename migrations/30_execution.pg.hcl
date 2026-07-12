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
    on_delete   = SET_NULL
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
  index "captain_model_calls_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
  }
  index "captain_model_calls_iteration_id_idx" {
    columns = [column.iteration_id]
  }
  index "captain_model_calls_model_idx" {
    columns = [column.model, column.backend]
  }
  index "captain_model_calls_started_at_idx" {
    columns = [column.started_at]
  }

  check "captain_model_calls_index_nonnegative" {
    expr = "call_index >= 0"
  }
  check "captain_model_calls_tokens_nonnegative" {
    expr = "input_tokens >= 0 AND output_tokens >= 0 AND reasoning_tokens >= 0 AND cache_read_tokens >= 0 AND cache_write_tokens >= 0 AND context_tokens >= 0 AND context_window_tokens >= 0"
  }
  check "captain_model_calls_costs_nonnegative" {
    expr = "input_cost >= 0 AND output_cost >= 0 AND reasoning_cost >= 0 AND cache_read_cost >= 0 AND cache_write_cost >= 0"
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

table "captain_messages" {
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
  column "turn_id" {
    null = true
    type = uuid
  }
  column "model_call_id" {
    null = true
    type = uuid
  }
  column "provider_message_id" {
    null = true
    type = text
  }
  column "sequence" {
    null = false
    type = bigint
  }
  column "role" {
    null = false
    type = text
  }
  column "parts" {
    null    = false
    type    = jsonb
    default = sql("'[]'::jsonb")
  }
  column "raw" {
    null = true
    type = jsonb
  }
  column "schema_version" {
    null    = false
    type    = integer
    default = 1
  }
  column "occurred_at" {
    null = true
    type = timestamptz
  }
  column "recorded_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_messages_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_messages_turn_id_fkey" {
    columns     = [column.turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_messages_model_call_id_fkey" {
    columns     = [column.model_call_id]
    ref_columns = [table.captain_model_calls.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  index "captain_messages_session_sequence_key" {
    unique  = true
    columns = [column.session_id, column.sequence]
  }
  index "captain_messages_provider_message_id_key" {
    unique  = true
    columns = [column.session_id, column.provider_message_id]
    where   = "provider_message_id IS NOT NULL"
  }
  index "captain_messages_turn_id_idx" {
    columns = [column.turn_id]
  }
  index "captain_messages_model_call_id_idx" {
    columns = [column.model_call_id]
  }

  check "captain_messages_sequence_nonnegative" {
    expr = "sequence >= 0"
  }
  check "captain_messages_role_nonempty" {
    expr = "length(btrim(role)) > 0"
  }
  check "captain_messages_schema_version_positive" {
    expr = "schema_version > 0"
  }
}

table "captain_events" {
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
  column "turn_id" {
    null = true
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
  column "model_call_id" {
    null = true
    type = uuid
  }
  column "parent_event_id" {
    null = true
    type = uuid
  }
  column "event_key" {
    null = true
    type = text
  }
  column "stream" {
    null    = false
    type    = text
    default = "runtime"
  }
  column "sequence" {
    null = true
    type = bigint
  }
  column "kind" {
    null = false
    type = text
  }
  column "scope" {
    null    = false
    type    = text
    default = "session"
  }
  column "payload" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "schema_version" {
    null    = false
    type    = integer
    default = 1
  }
  column "occurred_at" {
    null = true
    type = timestamptz
  }
  column "recorded_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_events_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_events_turn_id_fkey" {
    columns     = [column.turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_events_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_events_iteration_id_fkey" {
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
  foreign_key "captain_events_model_call_id_fkey" {
    columns     = [column.model_call_id]
    ref_columns = [table.captain_model_calls.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_events_parent_event_id_fkey" {
    columns     = [column.parent_event_id]
    ref_columns = [table.captain_events.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  index "captain_events_event_key" {
    unique  = true
    columns = [column.session_id, column.event_key]
    where   = "event_key IS NOT NULL"
  }
  index "captain_events_stream_sequence_key" {
    unique  = true
    columns = [column.session_id, column.stream, column.sequence]
    where   = "sequence IS NOT NULL"
  }
  index "captain_events_turn_id_idx" {
    columns = [column.turn_id]
  }
  index "captain_events_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
  }
  index "captain_events_iteration_id_idx" {
    columns = [column.iteration_id]
  }
  index "captain_events_model_call_id_idx" {
    columns = [column.model_call_id]
  }
  index "captain_events_kind_recorded_at_idx" {
    columns = [column.kind, column.recorded_at]
  }

  check "captain_events_sequence_nonnegative" {
    expr = "sequence IS NULL OR sequence >= 0"
  }
  check "captain_events_schema_version_positive" {
    expr = "schema_version > 0"
  }
  check "captain_events_parent_not_self" {
    expr = "parent_event_id IS NULL OR parent_event_id <> id"
  }
  check "captain_events_iteration_has_run" {
    expr = "iteration_id IS NULL OR prompt_run_id IS NOT NULL"
  }
}

table "captain_turn_requests" {
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
  column "turn_id" {
    null = true
    type = uuid
  }
  column "prompt_run_id" {
    null = true
    type = uuid
  }
  column "plan_id" {
    null = true
    type = uuid
  }
  column "model_call_id" {
    null = true
    type = uuid
  }
  column "tool_call_id" {
    null = true
    type = text
  }
  column "kind" {
    null = false
    type = enum.captain_turn_request_kind
  }
  column "state" {
    null    = false
    type    = enum.captain_turn_request_state
    default = "pending"
  }
  column "request" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "response" {
    null = true
    type = jsonb
  }
  column "idempotency_key" {
    null = true
    type = text
  }
  column "requested_by" {
    null = true
    type = text
  }
  column "resolved_by" {
    null = true
    type = text
  }
  column "reason" {
    null = true
    type = text
  }
  column "version" {
    null    = false
    type    = bigint
    default = 0
  }
  column "expires_at" {
    null = true
    type = timestamptz
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "resolved_at" {
    null = true
    type = timestamptz
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_turn_requests_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_turn_requests_turn_id_fkey" {
    columns     = [column.turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_turn_requests_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_turn_requests_plan_id_fkey" {
    columns     = [column.plan_id]
    ref_columns = [table.captain_plans.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_turn_requests_model_call_id_fkey" {
    columns     = [column.model_call_id]
    ref_columns = [table.captain_model_calls.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  index "captain_turn_requests_idempotency_key" {
    unique  = true
    columns = [column.session_id, column.idempotency_key]
    where   = "idempotency_key IS NOT NULL"
  }
  index "captain_turn_requests_pending_session_idx" {
    columns = [column.session_id, column.kind, column.created_at]
    where   = "state = 'pending'"
  }
  index "captain_turn_requests_turn_id_idx" {
    columns = [column.turn_id]
  }
  index "captain_turn_requests_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
  }

  check "captain_turn_requests_version_nonnegative" {
    expr = "version >= 0"
  }
  check "captain_turn_requests_resolution" {
    expr = "(state = 'pending' AND resolved_at IS NULL) OR (state <> 'pending' AND resolved_at IS NOT NULL)"
  }
  check "captain_turn_requests_time_order" {
    expr = "resolved_at IS NULL OR resolved_at >= created_at"
  }
}

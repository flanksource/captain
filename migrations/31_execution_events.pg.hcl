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
  column "source_line" {
    null = true
    type = bigint
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
  # Partial on purpose. captain_messages_model_call_id_fkey is ON DELETE SET
  # NULL, so deleting a model call must find its referencing messages -- without
  # an index that is a sequential scan of the largest table in the schema. But
  # the ingest path never sets model_call_id, so a full index on the column was
  # 8.4 MB of nothing but NULLs, maintained on every message insert. Excluding
  # the NULLs costs the FK check nothing (model_call_id = <id> can never match a
  # NULL row) and takes the index, and its insert-time upkeep, to zero.
  index "captain_messages_model_call_id_idx" {
    columns = [column.model_call_id]
    where   = "model_call_id IS NOT NULL"
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
  check "captain_messages_source_line_positive" {
    expr = "source_line IS NULL OR source_line > 0"
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
    on_delete   = NO_ACTION
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


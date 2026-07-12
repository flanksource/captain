table "captain_artifacts" {
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
  column "prompt_run_id" {
    null = true
    type = uuid
  }
  column "kind" {
    null = false
    type = text
  }
  column "path" {
    null = true
    type = text
  }
  column "digest" {
    null = true
    type = text
  }
  column "content_type" {
    null = true
    type = text
  }
  column "metadata" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "occurred_at" {
    null = true
    type = timestamptz
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_artifacts_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_artifacts_turn_id_fkey" {
    columns     = [column.turn_id]
    ref_columns = [table.captain_turns.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_artifacts_model_call_id_fkey" {
    columns     = [column.model_call_id]
    ref_columns = [table.captain_model_calls.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }
  foreign_key "captain_artifacts_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  index "captain_artifacts_session_kind_idx" {
    columns = [column.session_id, column.kind, column.occurred_at]
  }
  index "captain_artifacts_turn_id_idx" {
    columns = [column.turn_id]
  }
  index "captain_artifacts_model_call_id_idx" {
    columns = [column.model_call_id]
  }
  index "captain_artifacts_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
  }
  index "captain_artifacts_digest_idx" {
    columns = [column.digest]
    where   = "digest IS NOT NULL"
  }
}

table "captain_session_sources" {
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
  column "source_kind" {
    null = false
    type = text
  }
  column "path" {
    null = false
    type = text
  }
  column "source_identity" {
    null = true
    type = text
  }
  column "parser_version" {
    null    = false
    type    = integer
    default = 1
  }
  column "byte_offset" {
    null    = false
    type    = bigint
    default = 0
  }
  column "observed_size" {
    null    = false
    type    = bigint
    default = 0
  }
  column "observed_mod_time" {
    null = true
    type = timestamptz
  }
  column "last_event_key" {
    null = true
    type = text
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

  foreign_key "captain_session_sources_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_session_sources_source_path_key" {
    unique  = true
    columns = [column.source_kind, column.path]
  }
  index "captain_session_sources_session_id_idx" {
    columns = [column.session_id]
  }

  check "captain_session_sources_parser_version_positive" {
    expr = "parser_version > 0"
  }
  check "captain_session_sources_offsets_nonnegative" {
    expr = "byte_offset >= 0 AND observed_size >= 0"
  }
}

table "captain_outbox" {
  schema = schema.public

  column "sequence" {
    null = false
    type = bigserial
  }
  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "topic" {
    null = false
    type = text
  }
  column "aggregate_type" {
    null = false
    type = text
  }
  column "aggregate_id" {
    null = false
    type = uuid
  }
  column "event_type" {
    null = false
    type = text
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
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.sequence]
  }

  index "captain_outbox_id_key" {
    unique  = true
    columns = [column.id]
  }
  index "captain_outbox_topic_sequence_idx" {
    columns = [column.topic, column.sequence]
  }
  index "captain_outbox_aggregate_idx" {
    columns = [column.aggregate_type, column.aggregate_id, column.sequence]
  }

  check "captain_outbox_schema_version_positive" {
    expr = "schema_version > 0"
  }
}

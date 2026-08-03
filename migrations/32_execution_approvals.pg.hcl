table "captain_session_mcp_credentials" {
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
  column "prompt_run_id" {
    null = false
    type = uuid
  }
  column "backend" {
    null = false
    type = text
  }
  column "secret_hash" {
    null = false
    type = bytea
  }
  column "policy" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "expires_at" {
    null = true
    type = timestamptz
  }
  column "revoked_at" {
    null = true
    type = timestamptz
  }
  column "revocation_reason" {
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

  foreign_key "captain_session_mcp_credentials_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_session_mcp_credentials_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_session_mcp_credentials_secret_hash_key" {
    unique  = true
    columns = [column.secret_hash]
  }
  index "captain_session_mcp_credentials_active_session_idx" {
    columns = [column.session_id, column.created_at]
    where   = "revoked_at IS NULL"
  }

  check "captain_session_mcp_credentials_hash_length" {
    expr = "octet_length(secret_hash) = 32"
  }
  check "captain_session_mcp_credentials_expiry" {
    expr = "expires_at IS NULL OR expires_at > created_at"
  }
  check "captain_session_mcp_credentials_revocation" {
    expr = "(revoked_at IS NULL AND revocation_reason IS NULL) OR revoked_at IS NOT NULL"
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
  column "credential_id" {
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
  foreign_key "captain_turn_requests_credential_id_fkey" {
    columns     = [column.credential_id]
    ref_columns = [table.captain_session_mcp_credentials.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
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
  index "captain_turn_requests_credential_call_key" {
    unique  = true
    columns = [column.credential_id, column.tool_call_id]
    where   = "credential_id IS NOT NULL AND tool_call_id IS NOT NULL"
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
  check "captain_turn_requests_tool_approval_identity" {
    expr = "kind <> 'tool_approval' OR (prompt_run_id IS NOT NULL AND turn_id IS NOT NULL AND model_call_id IS NOT NULL AND tool_call_id IS NOT NULL)"
  }
}

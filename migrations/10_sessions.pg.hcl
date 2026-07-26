table "captain_sessions" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "provider_session_id" {
    null = true
    type = text
  }
  column "source" {
    null = false
    type = text
  }
  column "provider" {
    null    = false
    type    = text
    default = ""
  }
  column "host_id" {
    null    = false
    type    = text
    default = "local"
  }
  column "parent_session_id" {
    null = true
    type = uuid
  }
  column "root_session_id" {
    null = true
    type = uuid
  }
  column "path" {
    null = true
    type = text
  }
  column "project" {
    null = true
    type = text
  }
  column "cwd" {
    null = true
    type = text
  }
  column "title" {
    null = true
    type = text
  }
  column "initial_prompt" {
    null = true
    type = text
  }
  column "slug" {
    null = true
    type = text
  }
  column "agent_type" {
    null = true
    type = text
  }
  column "description" {
    null = true
    type = text
  }
  column "cli_version" {
    null = true
    type = text
  }
  column "lifecycle_status" {
    null    = false
    type    = enum.captain_session_lifecycle_status
    default = "created"
  }
  column "activity_state" {
    null    = false
    type    = enum.captain_session_activity_state
    default = "idle"
  }
  column "health_state" {
    null    = false
    type    = enum.captain_session_health_state
    default = "healthy"
  }
  column "state_reason" {
    null = true
    type = text
  }
  column "state_version" {
    null    = false
    type    = bigint
    default = 0
  }
  column "state_observed_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "git" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "metadata" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "started_at" {
    null = true
    type = timestamptz
  }
  column "ended_at" {
    null = true
    type = timestamptz
  }
  column "last_activity_at" {
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

  foreign_key "captain_sessions_parent_session_id_fkey" {
    columns     = [column.parent_session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  foreign_key "captain_sessions_root_session_id_fkey" {
    columns     = [column.root_session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_sessions_provider_identity_key" {
    unique  = true
    columns = [column.source, column.provider, column.host_id, column.provider_session_id]
    where   = "provider_session_id IS NOT NULL"
  }
  index "captain_sessions_provider_session_id_idx" {
    on {
      column = column.provider_session_id
      ops    = "text_pattern_ops"
    }
    where = "provider_session_id IS NOT NULL"
  }
  index "captain_sessions_parent_session_id_idx" {
    columns = [column.parent_session_id]
  }
  index "captain_sessions_root_session_id_idx" {
    columns = [column.root_session_id]
  }
  # Backs FindSessionIDByCWD, the process-to-session heuristic run for every
  # discovered agent process whose command line carries no session id. Partial
  # on the root sessions because that is the only side the lookup considers.
  index "captain_sessions_source_cwd_idx" {
    columns = [column.source, column.cwd]
    where   = "parent_session_id IS NULL"
  }
  index "captain_sessions_state_idx" {
    columns = [column.lifecycle_status, column.activity_state, column.health_state]
  }
  index "captain_sessions_last_activity_at_idx" {
    columns = [column.last_activity_at]
  }

  # captain_sessions_project_idx was removed. ListSessionOverviews can filter on
  # project, but it does so through captain_session_overview and alongside a
  # metadata predicate and a COALESCE sort, so the planner has never chosen the
  # index -- 0 scans across two independent measurement windows totalling five
  # hours and 2.1M index scans of this table. It is not free to keep: this is
  # the heartbeat table, every non-HOT update rewrites every index entry, and
  # 71_session_storage_params.sql exists precisely to keep those updates HOT.

  check "captain_sessions_parent_not_self" {
    expr = "parent_session_id IS NULL OR parent_session_id <> id"
  }
  check "captain_sessions_root_not_self" {
    expr = "root_session_id IS NULL OR root_session_id <> id"
  }
  check "captain_sessions_state_version_nonnegative" {
    expr = "state_version >= 0"
  }
  check "captain_sessions_time_order" {
    expr = "ended_at IS NULL OR started_at IS NULL OR ended_at >= started_at"
  }
}

table "captain_session_processes" {
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
  column "host_id" {
    null = false
    type = text
  }
  column "boot_id" {
    null = false
    type = text
  }
  column "pid" {
    null = false
    type = bigint
  }
  column "process_started_at" {
    null = false
    type = timestamptz
  }
  column "status" {
    null = false
    type = text
  }
  column "command" {
    null = true
    type = text
  }
  column "cwd" {
    null = true
    type = text
  }
  column "surface" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "source" {
    null    = false
    type    = text
    default = ""
  }
  column "cpu_percent" {
    null    = false
    type    = numeric(6, 2)
    default = 0
  }
  column "memory_percent" {
    null    = false
    type    = numeric(6, 2)
    default = 0
  }
  column "memory_rss_bytes" {
    null = true
    type = bigint
  }
  column "sampled_at" {
    null = true
    type = timestamptz
  }
  column "last_heartbeat_at" {
    null = true
    type = timestamptz
  }
  column "lease_owner" {
    null = true
    type = text
  }
  column "lease_token" {
    null = true
    type = uuid
  }
  column "lease_expires_at" {
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

  foreign_key "captain_session_processes_session_id_fkey" {
    columns     = [column.session_id]
    ref_columns = [table.captain_sessions.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  index "captain_session_processes_identity_key" {
    unique  = true
    columns = [column.host_id, column.boot_id, column.pid, column.process_started_at]
  }
  index "captain_session_processes_active_session_key" {
    unique  = true
    columns = [column.session_id]
    where   = "ended_at IS NULL"
  }
  index "captain_session_processes_lease_idx" {
    columns = [column.lease_expires_at]
    where   = "ended_at IS NULL"
  }

  check "captain_session_processes_pid_positive" {
    expr = "pid > 0"
  }
  check "captain_session_processes_lease_complete" {
    expr = "lease_expires_at IS NULL OR (lease_owner IS NOT NULL AND lease_token IS NOT NULL)"
  }
  check "captain_session_processes_time_order" {
    expr = "ended_at IS NULL OR ended_at >= process_started_at"
  }
  check "captain_session_processes_metrics_nonnegative" {
    expr = "cpu_percent >= 0 AND memory_percent >= 0 AND (memory_rss_bytes IS NULL OR memory_rss_bytes >= 0)"
  }
}

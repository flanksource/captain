# Durable run history for tasks dispatched to remote git-agents.
#
# The enrolled-agent roster is deliberately NOT here: ~/.captain.yaml stays the
# single source of truth for it, because the receiver re-reads that file on every
# SSH handshake so a revocation takes effect immediately (SPEC-git-agent-protocol
# R8.5). A second copy in Postgres would be a second source of truth for an
# authorization decision. Only what a run *did* is persisted.
#
# Written by the ingest watcher in captain serve (pkg/monitor), which walks the
# supervisor's mailbox tree. Both tables are upserted on their natural key, so a
# re-scan of unchanged state is a no-op.

table "captain_git_agent_tasks" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  # The protocol task id, unique only within its mailbox.
  column "task_id" {
    null = false
    type = text
  }
  # The mailbox this task was routed through ("mailboxes/<sha256>.git"). One
  # endpoint serves many repositories, so the mailbox is the id's scope.
  column "mailbox" {
    null = false
    type = text
  }
  # Canonical repository path bound to the mailbox, for display.
  column "repository" {
    null = true
    type = text
  }
  # The configured sandbox backend that dispatched this task.
  column "backend" {
    null = true
    type = text
  }
  # The enrolled agent it was dispatched to, when one was pinned or chosen.
  column "agent" {
    null = true
    type = text
  }
  # Filled opportunistically: persistPromptRun writes the prompt_runs row only
  # after the run finishes, so the task row always exists first. The watcher
  # resolves this from admission_key on a later pass.
  column "prompt_run_id" {
    null = true
    type = uuid
  }
  # The originating run's admission key, the handle used to resolve
  # prompt_run_id once that row lands.
  column "admission_key" {
    null = true
    type = text
  }
  column "base" {
    null = false
    type = text
  }
  column "dispatch_commit" {
    null = false
    type = text
  }
  column "control_commit" {
    null = true
    type = text
  }
  column "relay" {
    null = true
    type = text
  }
  # The dispatch policy (paths, maxAttempts, maxBlobSize) verbatim.
  column "policy" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "hooks" {
    null = true
    type = jsonb
  }
  # Highest attempt seen. Monotonic: the watcher never lowers it.
  column "attempts" {
    null    = false
    type    = integer
    default = 0
  }
  column "max_attempts" {
    null    = false
    type    = integer
    default = 0
  }
  column "status" {
    null    = false
    type    = enum.captain_git_agent_task_status
    default = "dispatched"
  }
  # The concluding verdict, once one exists.
  column "final_status" {
    null = true
    type = enum.captain_git_agent_verdict_status
  }
  # Branch the accepted work was integrated onto.
  column "integrated_branch" {
    null = true
    type = text
  }
  column "error" {
    null = true
    type = text
  }
  column "dispatched_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "concluded_at" {
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

  # SET_NULL, not CASCADE: the remote task happened regardless of whether the
  # prompt run row is later pruned, and losing that history would be wrong.
  foreign_key "captain_git_agent_tasks_prompt_run_id_fkey" {
    columns     = [column.prompt_run_id]
    ref_columns = [table.captain_prompt_runs.column.id]
    on_update   = NO_ACTION
    on_delete   = SET_NULL
  }

  index "captain_git_agent_tasks_mailbox_task_key" {
    unique  = true
    columns = [column.mailbox, column.task_id]
  }
  index "captain_git_agent_tasks_status_idx" {
    columns = [column.status, column.updated_at]
  }
  index "captain_git_agent_tasks_dispatched_at_idx" {
    columns = [column.dispatched_at]
  }
  index "captain_git_agent_tasks_agent_idx" {
    columns = [column.agent, column.dispatched_at]
    where   = "agent IS NOT NULL"
  }
  index "captain_git_agent_tasks_prompt_run_id_idx" {
    columns = [column.prompt_run_id]
    where   = "prompt_run_id IS NOT NULL"
  }
  index "captain_git_agent_tasks_admission_key_idx" {
    columns = [column.admission_key]
    where   = "admission_key IS NOT NULL"
  }

  check "captain_git_agent_tasks_attempts_nonnegative" {
    expr = "attempts >= 0 AND max_attempts >= 0"
  }
  check "captain_git_agent_tasks_time_order" {
    expr = "concluded_at IS NULL OR concluded_at >= dispatched_at"
  }
  check "captain_git_agent_tasks_task_id_nonempty" {
    expr = "length(btrim(task_id)) > 0"
  }
}

# One tier's decision on one attempt. Findings ride along as jsonb rather than a
# third table: they are always read with their attempt and never queried alone,
# matching captain_prompt_run_iterations.verification_result.
table "captain_git_agent_task_attempts" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "task_id" {
    null = false
    type = uuid
  }
  column "attempt" {
    null = false
    type = integer
  }
  # "sidecar" | "supervisor". Text with a check rather than an enum:
  # TierVerdict.Tier is a free Go string, and a new tier should not need a
  # migration to record.
  column "tier" {
    null = false
    type = text
  }
  column "status" {
    null = false
    type = enum.captain_git_agent_verdict_status
  }
  column "protocol_version" {
    null    = false
    type    = integer
    default = 1
  }
  column "findings" {
    null    = false
    type    = jsonb
    default = sql("'[]'::jsonb")
  }
  column "result_commit" {
    null = true
    type = text
  }
  column "feedback" {
    null = true
    type = text
  }
  column "recorded_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("now()")
  }

  primary_key {
    columns = [column.id]
  }

  foreign_key "captain_git_agent_task_attempts_task_id_fkey" {
    columns     = [column.task_id]
    ref_columns = [table.captain_git_agent_tasks.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }

  # Keyed on (task, attempt, tier), not (task, attempt): the sidecar and the
  # supervisor each reach their own verdict on the same attempt.
  index "captain_git_agent_task_attempts_task_attempt_tier_key" {
    unique  = true
    columns = [column.task_id, column.attempt, column.tier]
  }
  index "captain_git_agent_task_attempts_status_idx" {
    columns = [column.status, column.recorded_at]
  }

  check "captain_git_agent_task_attempts_attempt_positive" {
    expr = "attempt >= 1"
  }
  check "captain_git_agent_task_attempts_tier" {
    expr = "tier IN ('sidecar', 'supervisor')"
  }
}

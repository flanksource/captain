schema "public" {}

enum "captain_session_lifecycle_status" {
  schema = schema.public
  values = ["created", "running", "succeeded", "partial", "failed", "cancelled", "interrupted"]
}

enum "captain_session_activity_state" {
  schema = schema.public
  values = ["idle", "thinking", "working", "ask", "approval"]
}

enum "captain_session_health_state" {
  schema = schema.public
  values = ["healthy", "stalled", "zombie"]
}

enum "captain_prompt_run_phase" {
  schema = schema.public
  values = ["queued", "pre_run", "generate", "verify", "feedback", "post_run", "output", "finished"]
}

enum "captain_prompt_run_state" {
  schema = schema.public
  values = ["pending", "running", "waiting", "succeeded", "failed", "cancelled"]
}

enum "captain_prompt_run_iteration_state" {
  schema = schema.public
  values = ["pending", "running", "succeeded", "failed", "cancelled"]
}

enum "captain_turn_status" {
  schema = schema.public
  values = ["open", "ended", "error", "interrupted"]
}

enum "captain_model_call_status" {
  schema = schema.public
  values = ["pending", "running", "succeeded", "failed", "cancelled"]
}

enum "captain_turn_request_kind" {
  schema = schema.public
  values = ["question", "tool_approval", "plan_exit_approval"]
}

enum "captain_turn_request_state" {
  schema = schema.public
  values = ["pending", "approved", "denied", "answered", "cancelled", "expired"]
}

enum "captain_plan_approval_state" {
  schema = schema.public
  values = ["pending", "approved", "rejected", "revision_requested"]
}

# The lifecycle of one task dispatched to a remote git-agent. Only "dispatched"
# and "running" come from the protocol; the terminal states are derived by the
# ingest watcher, because the mailbox never records "this task is over".
enum "captain_git_agent_task_status" {
  schema = schema.public
  values = ["dispatched", "running", "accepted", "rejected", "errored", "timed_out"]
}

# Mirrors gitagent.VerdictStatus (pkg/gitagent/verdict.go). "error" means the
# tier could not reach a verdict, which rejects the push.
enum "captain_git_agent_verdict_status" {
  schema = schema.public
  values = ["accepted", "rejected", "error"]
}

# What an API token may reach. "git" authorizes pushing to a served repository
# and nothing else, so a token held by a remote coding agent cannot reach the
# /api/v1 executor — which runs arbitrary captain commands. Mirrors
# captaintoken.Scope.
enum "captain_api_token_scope" {
  schema = schema.public
  values = ["git", "api"]
}

# Where a runtime preset sits in the resolved layer order: later scopes override
# earlier ones, and a user-scoped preset ties with the caller's request layer.
# Mirrors api.SpecLayerScope.
enum "captain_spec_layer_scope" {
  schema = schema.public
  values = ["global", "context", "surface", "user"]
}

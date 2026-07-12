schema "public" {}

enum "captain_session_lifecycle_status" {
  schema = schema.public
  values = ["created", "running", "succeeded", "failed", "cancelled", "interrupted"]
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

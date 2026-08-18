# Bearer credentials for reaching this captain over the network.
#
# A token is durable, not a bootstrap coupon: it stays valid until it expires or
# is revoked. That is what lets a restarting or rescheduled sidecar re-present
# the same credential instead of crash-looping on a spent one.
#
# Only the hash is stored, and unlike captain_session_mcp_credentials it is an
# argon2id encoded string rather than a raw sha256 digest — hence text, and no
# octet_length check. The presented secret is high-entropy, so the KDF is
# defence in depth against a leaked database rather than against guessing.

table "captain_api_tokens" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  # The public half of the token, carried in plaintext by the client and safe to
  # log. Verification looks this up on its unique index and only then runs the
  # KDF, so a presented token costs one indexed read rather than a table scan.
  column "token_id" {
    null = false
    type = text
  }
  column "secret_hash" {
    null = false
    type = text
  }
  column "name" {
    null = false
    type = text
  }
  column "scope" {
    null = false
    type = enum.captain_api_token_scope
  }
  # Set when the token is bound to a single agent identity. Null on a pool
  # token, whose members are named as they arrive.
  column "agent" {
    null = true
    type = text
  }
  column "pool" {
    null    = false
    type    = boolean
    default = false
  }
  # Members already admitted under a pool token, so max_agents can be enforced
  # and a returning member keeps its name across restarts.
  column "pool_agents" {
    null    = false
    type    = jsonb
    default = sql("'[]'::jsonb")
  }
  column "max_agents" {
    null = true
    type = int
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
  column "last_used_at" {
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

  index "captain_api_tokens_token_id_key" {
    unique  = true
    columns = [column.token_id]
  }
  index "captain_api_tokens_live_idx" {
    columns = [column.created_at]
    where   = "revoked_at IS NULL"
  }
  index "captain_api_tokens_agent_idx" {
    columns = [column.agent]
    where   = "agent IS NOT NULL AND revoked_at IS NULL"
  }

  check "captain_api_tokens_expiry" {
    expr = "expires_at IS NULL OR expires_at > created_at"
  }
  check "captain_api_tokens_revocation" {
    expr = "(revoked_at IS NULL AND revocation_reason IS NULL) OR revoked_at IS NOT NULL"
  }
  # A token names one identity or serves a pool, never both: the two answer
  # "who is this?" differently, and a row claiming both would leave the
  # namespace owner ambiguous. Pooling is a git-scope concept — an API caller
  # has no member name to derive — so the api scope is neither pooled nor bound.
  check "captain_api_tokens_identity" {
    expr = "(scope = 'git' AND pool AND agent IS NULL) OR (scope = 'git' AND NOT pool AND agent IS NOT NULL) OR (scope = 'api' AND NOT pool AND agent IS NULL)"
  }
  check "captain_api_tokens_max_agents" {
    expr = "max_agents IS NULL OR (pool AND max_agents > 0)"
  }
}

# Multidimensional AI budgets. Rules are authored either here or as catalog
# YAML. Attribution deliberately stores the catalog id as text: YAML rules have
# no row to reference, and retained attribution must survive rule deletion.

table "captain_budget_rules" {
  schema = schema.public

  column "id" {
    null    = false
    type    = uuid
    default = sql("gen_random_uuid()")
  }
  column "name" {
    null = false
    type = text
  }
  column "match" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  column "group_by" {
    null    = false
    type    = jsonb
    default = sql("'[]'::jsonb")
  }
  column "amount" {
    null = false
    type = numeric(20, 8)
  }
  column "window" {
    null = false
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
  index "captain_budget_rules_name_key" {
    unique = true
    on {
      expr = "lower(name)"
    }
  }
  check "captain_budget_rules_name" {
    expr = "length(btrim(name)) > 0"
  }
  check "captain_budget_rules_match" {
    expr = "jsonb_typeof(match) = 'object' AND jsonb_typeof(COALESCE(match->'dimensions', '{}'::jsonb)) = 'object' AND jsonb_typeof(COALESCE(match->'models', '[]'::jsonb)) = 'array'"
  }
  check "captain_budget_rules_group_by" {
    expr = "jsonb_typeof(group_by) = 'array'"
  }
  check "captain_budget_rules_amount" {
    expr = "amount > 0"
  }
  check "captain_budget_rules_window" {
    expr = "length(btrim(\"window\")) > 0"
  }
}

# Attribution is keyed by model call, the unit whose cost settles, so rebinding a
# later run in the same turn never moves spend already recorded by an earlier one.
table "captain_model_call_budgets" {
  schema = schema.public

  column "model_call_id" {
    null = false
    type = uuid
  }
  # Encoded catalog id. It may identify a database row or a YAML file.
  column "budget_rule_id" {
    null = false
    type = text
  }
  column "group_values" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }

  primary_key {
    columns = [column.model_call_id, column.budget_rule_id]
  }
  foreign_key "captain_model_call_budgets_model_call_id_fkey" {
    columns     = [column.model_call_id]
    ref_columns = [table.captain_model_calls.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  index "captain_model_call_budgets_rule_group_idx" {
    columns = [column.budget_rule_id, column.group_values]
  }
  check "captain_model_call_budgets_rule_id" {
    expr = "length(btrim(budget_rule_id)) > 0"
  }
  check "captain_model_call_budgets_group_values" {
    expr = "jsonb_typeof(group_values) = 'object'"
  }
}

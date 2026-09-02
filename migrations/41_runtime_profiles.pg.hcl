# Reusable runtime presets and the profiles that compose them.
#
# Names are unique case-insensitively. A preset or profile is referenced by
# name from other profiles, from .prompt frontmatter and from CLI flags, where
# "Review" and "review" would otherwise be two records nobody can tell apart
# when typing one of them.
#
# A profile's presets column is an ordered list of references rather than a
# join table. Order is semantic (later presets override earlier ones), and a
# reference may name a preset by catalog id or by name in any catalog source --
# a YAML file next to the repository as much as a row here -- so a foreign key
# could never express it. Referential integrity lives in the catalog
# (runtimeprofiles.Catalog.ReferencedBy), which refuses to delete a preset a
# profile still names.

table "captain_runtime_presets" {
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
  column "description" {
    null = true
    type = text
  }
  column "scope" {
    null = false
    type = enum.captain_spec_layer_scope
  }
  # The reusable subset of a Spec (api.RuntimePresetSpec) as JSON.
  column "spec" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
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

  index "captain_runtime_presets_name_key" {
    unique = true
    on {
      expr = "lower(name)"
    }
  }

  check "captain_runtime_presets_name" {
    expr = "length(btrim(name)) > 0"
  }
  check "captain_runtime_presets_spec" {
    expr = "jsonb_typeof(spec) = 'object'"
  }
}

table "captain_runtime_profiles" {
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
  column "description" {
    null = true
    type = text
  }
  # The task-specific Spec layered after the presets (api.Spec) as JSON.
  column "spec" {
    null    = false
    type    = jsonb
    default = sql("'{}'::jsonb")
  }
  # Ordered preset references, each a catalog id or a preset name.
  column "presets" {
    null    = false
    type    = jsonb
    default = sql("'[]'::jsonb")
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

  index "captain_runtime_profiles_name_key" {
    unique = true
    on {
      expr = "lower(name)"
    }
  }

  check "captain_runtime_profiles_name" {
    expr = "length(btrim(name)) > 0"
  }
  check "captain_runtime_profiles_spec" {
    expr = "jsonb_typeof(spec) = 'object'"
  }
  check "captain_runtime_profiles_presets" {
    expr = "jsonb_typeof(presets) = 'array'"
  }
}

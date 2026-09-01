-- phase: post

-- Rewrite the persisted JSON that still names a composite adapter under
-- `backend` into the (provider, mode) pair it always meant.
--
-- captain_sessions.metadata->'aichatRuntime' is the highest-risk of these:
-- SetSessionMetadataOnce compares the normalized value with reflect.DeepEqual,
-- so a row this misses does not degrade — it throws ErrThreadRuntimeConflict on
-- that thread's very next turn, permanently. Every row is rewritten here, in one
-- statement, in the same migration as the read-path change.

-- The composite id → axis mapping, as functions so the several JSONB rewrites
-- below cannot drift from one another. They are dropped at the end of this file:
-- nothing outside this migration may consult the old vocabulary.
CREATE OR REPLACE FUNCTION public.captain_runtime_provider(value text)
RETURNS text
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog, public
AS $$
  SELECT CASE lower(btrim(coalesce(value, '')))
    WHEN 'anthropic'    THEN 'anthropic'
    WHEN 'claude-cli'   THEN 'anthropic'
    WHEN 'claude-agent' THEN 'anthropic'
    WHEN 'claude-cmux'  THEN 'anthropic'
    WHEN 'openai'       THEN 'openai'
    WHEN 'codex-cli'    THEN 'openai'
    WHEN 'codex-agent'  THEN 'openai'
    WHEN 'codex-cmux'   THEN 'openai'
    WHEN 'gemini'       THEN 'google'
    WHEN 'gemini-cli'   THEN 'google'
    WHEN 'deepseek'     THEN 'deepseek'
    ELSE NULL
  END;
$$;

CREATE OR REPLACE FUNCTION public.captain_runtime_mode(value text)
RETURNS text
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog, public
AS $$
  SELECT CASE lower(btrim(coalesce(value, '')))
    WHEN 'anthropic'    THEN 'api'
    WHEN 'openai'       THEN 'api'
    WHEN 'gemini'       THEN 'api'
    WHEN 'deepseek'     THEN 'api'
    WHEN 'claude-cli'   THEN 'cli'
    WHEN 'codex-cli'    THEN 'cli'
    WHEN 'gemini-cli'   THEN 'cli'
    WHEN 'claude-agent' THEN 'agent'
    WHEN 'codex-agent'  THEN 'agent'
    WHEN 'claude-cmux'  THEN 'cmux'
    WHEN 'codex-cmux'   THEN 'cmux'
    -- Already a bare mode: idempotent, so a re-apply is a no-op.
    WHEN 'api'          THEN 'api'
    WHEN 'cli'          THEN 'cli'
    WHEN 'agent'        THEN 'agent'
    WHEN 'cmux'         THEN 'cmux'
    ELSE NULL
  END;
$$;

UPDATE public.captain_sessions
SET metadata = jsonb_set(
  metadata,
  '{aichatRuntime}',
  (metadata -> 'aichatRuntime')
    - 'backend'
    || jsonb_strip_nulls(jsonb_build_object(
      -- COALESCE with the row's own keys: an already-migrated row keeps what it
      -- has, so a re-apply changes nothing.
      'provider', COALESCE(
        public.captain_runtime_provider(metadata #>> '{aichatRuntime,backend}'),
        NULLIF(metadata #>> '{aichatRuntime,provider}', '')),
      'mode', COALESCE(
        public.captain_runtime_mode(metadata #>> '{aichatRuntime,backend}'),
        NULLIF(metadata #>> '{aichatRuntime,mode}', ''))
    ))
)
WHERE jsonb_typeof(metadata -> 'aichatRuntime') = 'object'
  AND metadata #>> '{aichatRuntime,backend}' IS NOT NULL;

-- A record left naming no mode cannot bind a thread, and half a record is worse
-- than none: SetSessionMetadataOnce compares it with the (provider, mode, model)
-- the read path now writes, so it would fail that thread's next turn forever.
-- This catches the backends no axis was recoverable from ('legacy', 'unknown',
-- 'captain') as well as records that only ever held a model. Dropping the key
-- returns the thread to unbound, and its next send binds it from the model.
UPDATE public.captain_sessions
SET metadata = metadata - 'aichatRuntime'
WHERE jsonb_typeof(metadata -> 'aichatRuntime') = 'object'
  AND NULLIF(metadata #>> '{aichatRuntime,mode}', '') IS NULL;

-- captain_prompt_runs.runtime->'requested'/'resolved' carry the same identity.
-- runtime->>'mode' is a DIFFERENT concept (the run mode: "run"/"api") and is
-- deliberately untouched — only the nested selections are rewritten.
UPDATE public.captain_prompt_runs
SET runtime = runtime
  || (CASE WHEN jsonb_typeof(runtime -> 'requested') = 'object'
        AND runtime #>> '{requested,backend}' IS NOT NULL
      THEN jsonb_build_object('requested',
        (runtime -> 'requested') - 'backend' || jsonb_strip_nulls(jsonb_build_object(
          'provider', public.captain_runtime_provider(runtime #>> '{requested,backend}'),
          'mode', public.captain_runtime_mode(runtime #>> '{requested,backend}')
        )))
      ELSE '{}'::jsonb END)
  || (CASE WHEN jsonb_typeof(runtime -> 'resolved') = 'object'
        AND runtime #>> '{resolved,backend}' IS NOT NULL
      THEN jsonb_build_object('resolved',
        (runtime -> 'resolved') - 'backend' || jsonb_strip_nulls(jsonb_build_object(
          'provider', public.captain_runtime_provider(runtime #>> '{resolved,backend}'),
          'mode', public.captain_runtime_mode(runtime #>> '{resolved,backend}')
        )))
      ELSE '{}'::jsonb END)
WHERE jsonb_typeof(runtime) = 'object'
  AND (runtime #>> '{requested,backend}' IS NOT NULL
    OR runtime #>> '{resolved,backend}' IS NOT NULL);

-- rendered_spec carries an authored Model, whose runtime key is now `mode`.
UPDATE public.captain_prompt_runs
SET rendered_spec = rendered_spec
  - 'backend'
  || jsonb_strip_nulls(jsonb_build_object('mode',
       public.captain_runtime_mode(rendered_spec ->> 'backend')))
WHERE jsonb_typeof(rendered_spec) = 'object'
  AND NULLIF(rendered_spec ->> 'backend', '') IS NOT NULL;

UPDATE public.captain_prompt_runs
SET rendered_spec = jsonb_set(rendered_spec, '{model}',
  (rendered_spec -> 'model')
    - 'backend'
    || jsonb_strip_nulls(jsonb_build_object('mode',
         public.captain_runtime_mode(rendered_spec #>> '{model,backend}'))))
WHERE jsonb_typeof(rendered_spec -> 'model') = 'object'
  AND rendered_spec #>> '{model,backend}' IS NOT NULL;

-- captain_sessions.provider holds a provider LABEL ('captain', 'multi-model',
-- '') for most rows; only the values that are composite adapter ids are
-- normalized. The labels are a different vocabulary and are left alone.
UPDATE public.captain_sessions
SET provider = public.captain_runtime_provider(provider)
WHERE public.captain_runtime_provider(provider) IS NOT NULL
  AND provider <> public.captain_runtime_provider(provider);

DROP FUNCTION IF EXISTS public.captain_runtime_provider(text);
DROP FUNCTION IF EXISTS public.captain_runtime_mode(text);

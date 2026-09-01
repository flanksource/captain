-- phase: pre

-- Split the composite `backend` column into its two real axes before Atlas drops
-- it. A runtime is (provider, mode); the composite ids were a compression of
-- that pair, and the same JSON key meant an adapter outbound and a mode inbound.
--
-- This phase must run PRE: it reads `backend`, which the schema drops.
--
-- Each table is guarded on `backend` still being present, which is the script's
-- actual precondition and makes it idempotent in both directions:
--
--   * fresh database — a pre-phase script runs before the Atlas realm diff, so
--     the table does not exist yet and there is nothing to migrate; the HCL
--     creates provider/mode directly.
--   * already migrated — the diff has dropped `backend`, so the backfill has
--     nothing left to read.
--
-- Guarding on the table alone is not enough (the second case still reads a
-- dropped column), and ADD COLUMN IF NOT EXISTS guards the column, not the
-- table.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'captain_model_calls'
      AND column_name = 'backend'
  ) THEN
    RETURN;
  END IF;

  ALTER TABLE public.captain_model_calls ADD COLUMN IF NOT EXISTS provider text;
  ALTER TABLE public.captain_model_calls ADD COLUMN IF NOT EXISTS mode text;

  -- captain_model_calls.backend never had one vocabulary. Four writers disagreed:
  --   * the 11 composite adapter ids   (aichat / prompt runs) — both axes recoverable
  --   * 'claude' / 'codex'             (pkg/monitor: a transcript SOURCE, not an
  --                                     adapter) — provider recoverable, mode never observed
  --   * 'unknown'                      (session ingest's blank fallback)
  --   * 'legacy'                       (xero-cli's chat-session import)
  -- The last three carry no mode. They get NULL rather than a guess: a fabricated
  -- 'agent' here would be indistinguishable from an observed one forever after.
  UPDATE public.captain_model_calls
  SET
    provider = CASE lower(btrim(backend))
      WHEN 'anthropic'    THEN 'anthropic'
      WHEN 'claude-cli'   THEN 'anthropic'
      WHEN 'claude-agent' THEN 'anthropic'
      WHEN 'claude-cmux'  THEN 'anthropic'
      WHEN 'claude'       THEN 'anthropic'
      WHEN 'openai'       THEN 'openai'
      WHEN 'codex-cli'    THEN 'openai'
      WHEN 'codex-agent'  THEN 'openai'
      WHEN 'codex-cmux'   THEN 'openai'
      WHEN 'codex'        THEN 'openai'
      WHEN 'gemini'       THEN 'google'
      WHEN 'gemini-cli'   THEN 'google'
      WHEN 'deepseek'     THEN 'deepseek'
      ELSE NULL
    END,
    mode = CASE lower(btrim(backend))
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
      ELSE NULL
    END
  WHERE provider IS NULL AND mode IS NULL;
END
$$;

-- captain_session_mcp_credentials.backend only ever held an adapter id, so both
-- axes are recoverable and the columns stay NOT NULL.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'captain_session_mcp_credentials'
      AND column_name = 'backend'
  ) THEN
    RETURN;
  END IF;

  ALTER TABLE public.captain_session_mcp_credentials ADD COLUMN IF NOT EXISTS provider text;
  ALTER TABLE public.captain_session_mcp_credentials ADD COLUMN IF NOT EXISTS mode text;

  UPDATE public.captain_session_mcp_credentials
  SET
    provider = CASE lower(btrim(backend))
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
      ELSE 'anthropic'
    END,
    mode = CASE lower(btrim(backend))
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
      ELSE 'agent'
    END
  WHERE provider IS NULL AND mode IS NULL;

  ALTER TABLE public.captain_session_mcp_credentials ALTER COLUMN provider SET NOT NULL;
  ALTER TABLE public.captain_session_mcp_credentials ALTER COLUMN mode SET NOT NULL;
END
$$;

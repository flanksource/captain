-- phase: pre
-- dependsOn: 02_merge_duplicate_sessions.sql

-- One admission session is one provider conversation, so it holds at most one
-- transcript child; captain_sessions_transcript_parent_key in 10_sessions.pg.hcl
-- enforces it. Before the key existed, a run of a different provider family
-- admitted on a session that already had a transcript (a Codex-planned Gavel
-- session resumed with Claude) minted a second transcript child, and every read
-- of that session failed as a conflict.
--
-- The key cannot be created while those rows exist, which is why this runs in
-- the pre phase -- after 02, whose re-pointing of children onto a surviving
-- duplicate can itself leave a parent with two transcript children. The oldest
-- child by (created_at, id) is the conversation the session started as and stays
-- its transcript; the others are kept as 'agent' children so nothing is deleted.
--
-- On a database that predates 78 the column does not exist yet and there is
-- nothing to repair. Idempotent: once every parent has one transcript child the
-- update matches nothing.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'captain_sessions'
      AND column_name = 'parent_relation'
  ) THEN
    RETURN;
  END IF;

  UPDATE captain_sessions child
  SET parent_relation = 'agent'
  FROM (
    SELECT id,
           row_number() OVER (PARTITION BY parent_session_id ORDER BY created_at, id) AS position
    FROM captain_sessions
    WHERE parent_relation = 'transcript'
  ) ranked
  WHERE child.id = ranked.id
    AND ranked.position > 1;
END
$$;

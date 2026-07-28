-- phase: pre

-- captain_sessions_provider_identity_key used to include `provider`, and three
-- writers disagree about what a Codex rollout's provider is: the launcher
-- records `openai` or `codex-agent`, the monitor records ''. Each disagreement
-- minted its own session row for one rollout. Only the monitor's row was ever
-- ingested, so the others stayed at zero messages and surfaced as ghosts in
-- `sessions list` and as repeated headers in `sessions get`.
--
-- `provider` is a label on a session, not part of its identity -- the rollout id
-- and the host already identify it -- so the index narrows to
-- (source, host_id, provider_session_id) in 10_sessions.pg.hcl. That index
-- cannot be created while the duplicates exist, which is why this runs in the
-- pre phase.
--
-- The member carrying the transcript wins. On a developer database with 72
-- duplicate groups no group had more than one member with messages, so this
-- picks a winner rather than merging two transcripts; ties fall back to the
-- oldest row and then to the id, so the choice is deterministic on re-run.
-- Prompt runs pointing at a ghost are re-pointed, the ghosts are deleted, and
-- the winner then absorbs a ghost's provider label -- that label exists nowhere
-- else once the ghost is gone.
--
-- Idempotent: once the groups are collapsed every statement matches nothing.

DO $$
BEGIN
  IF to_regclass('public.captain_sessions') IS NULL THEN
    RETURN;
  END IF;

  -- Materialized, not a view: the statements below delete the rows the mapping
  -- is derived from, and the label fold has to outlive that deletion.
  CREATE TEMPORARY TABLE captain_duplicate_session_map AS
  WITH ranked AS (
    SELECT s.id,
           s.provider,
           first_value(s.id) OVER w AS winner
    FROM captain_sessions s
    JOIN (
      SELECT source, host_id, provider_session_id
      FROM captain_sessions
      WHERE provider_session_id IS NOT NULL
      GROUP BY source, host_id, provider_session_id
      HAVING count(*) > 1
    ) d
      ON s.source = d.source
     AND s.host_id IS NOT DISTINCT FROM d.host_id
     AND s.provider_session_id = d.provider_session_id
    WINDOW w AS (
      PARTITION BY s.source, s.host_id, s.provider_session_id
      ORDER BY (SELECT count(*) FROM captain_messages m WHERE m.session_id = s.id) DESC,
               s.created_at ASC,
               s.id ASC
    )
  )
  SELECT id AS loser, provider AS loser_provider, winner
  FROM ranked
  WHERE id <> winner;

  UPDATE captain_prompt_runs r SET session_id = m.winner
  FROM captain_duplicate_session_map m WHERE r.session_id = m.loser;

  UPDATE captain_prompt_runs r SET root_session_id = m.winner
  FROM captain_duplicate_session_map m WHERE r.root_session_id = m.loser;

  UPDATE captain_prompt_runs r SET execution_session_id = m.winner
  FROM captain_duplicate_session_map m WHERE r.execution_session_id = m.loser;

  DELETE FROM captain_sessions s
  USING captain_duplicate_session_map m
  WHERE s.id = m.loser;

  -- After the delete, not before: while the old wide index still exists,
  -- writing the ghost's label onto the winner would collide with the ghost.
  UPDATE captain_sessions w
  SET provider = label.provider
  FROM (
    SELECT DISTINCT ON (winner) winner, loser_provider AS provider
    FROM captain_duplicate_session_map
    WHERE loser_provider <> ''
    ORDER BY winner, loser
  ) label
  WHERE w.id = label.winner AND w.provider = '';

  DROP TABLE captain_duplicate_session_map;
END
$$;

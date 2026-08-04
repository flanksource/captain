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
-- Prompt runs and child sessions pointing at a ghost are re-pointed, the ghosts
-- are deleted, and the winner then absorbs a ghost's provider label -- that label
-- exists nowhere else once the ghost is gone.
--
-- Every reference re-pointed below is one the ghost's ON DELETE CASCADE would
-- otherwise destroy. The ghost's own transcript rows -- messages, turns, events,
-- artifacts -- are still cascaded away, which is sound only because the winner is
-- by definition the member holding the transcript; those rows are (session_id,
-- sequence)-unique, so moving them onto the winner is not a plain re-point and is
-- deliberately not attempted here.
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

  UPDATE captain_session_processes p SET session_id = m.winner
  FROM captain_duplicate_session_map m WHERE p.session_id = m.loser;

  -- captain_sessions.parent_session_id and root_session_id are self-referential
  -- and ON DELETE CASCADE, so a ghost that any subagent row named as its parent
  -- takes that whole subtree -- sessions, messages, turns, artifacts -- down with
  -- it. The launcher is both the writer that minted the ghosts and the writer
  -- that records parent/root links, so this is the likely case rather than the
  -- pathological one, and re-pointing has to happen before the delete.
  UPDATE captain_sessions c SET parent_session_id = m.winner
  FROM captain_duplicate_session_map m
  WHERE c.parent_session_id = m.loser AND c.id <> m.winner;

  UPDATE captain_sessions c SET root_session_id = m.winner
  FROM captain_duplicate_session_map m
  WHERE c.root_session_id = m.loser AND c.id <> m.winner;

  -- The `c.id <> m.winner` guards above leave one case: a winner whose own
  -- parent or root is a duplicate of itself. Re-pointing it would make the row
  -- its own parent, so the link is dropped instead -- a NULL parent already means
  -- "root" to every reader (see COALESCE(root_session_id, id) in
  -- 63_view_session_agents.sql), and leaving it would cascade the winner away.
  UPDATE captain_sessions w SET parent_session_id = NULL
  FROM captain_duplicate_session_map m
  WHERE w.id = m.winner AND w.parent_session_id = m.loser;

  UPDATE captain_sessions w SET root_session_id = NULL
  FROM captain_duplicate_session_map m
  WHERE w.id = m.winner AND w.root_session_id = m.loser;

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

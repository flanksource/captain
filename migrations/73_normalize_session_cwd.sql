-- phase: post

-- FindSessionIDByCWD used to compare rtrim(cwd, '/') = $2 because callers were
-- free to store either spelling of a directory. No index can serve that
-- expression, so the process-to-session heuristic seq-scanned captain_sessions
-- every time an agent process was discovered without a session id in its
-- command line. Measured on a developer database: 2,374 sequential scans had
-- read 18.8M tuples from an 8,000-row table, and a single lookup scanned 8,141
-- rows to return 133.
--
-- normalizeCWD now settles the spelling at both write sites (CreateOrGetSession
-- and projectSessionColumns), the query is a plain equality test, and
-- captain_sessions_source_cwd_idx backs it. This backfills the rows written
-- before that, so the equality test cannot miss them.
--
-- Root is deliberately left alone: rtrim('/', '/') is the empty string, which
-- would erase the directory rather than normalize it. Rows already holding the
-- empty string are equally untouched -- there is nothing to normalize.
UPDATE public.captain_sessions
SET cwd = rtrim(btrim(cwd), '/')
WHERE cwd IS NOT NULL
  AND cwd <> rtrim(btrim(cwd), '/')
  AND rtrim(btrim(cwd), '/') <> '';

-- phase: post

-- Drop captain_session_files, which was never wired to anything.
--
-- The view was added as "normalized file.* artifact rows for the SessionInspector
-- files tab", but no non-test code has ever inserted a captain_artifacts row, so
-- it selected zero rows on every database while the files tab it was built for
-- rendered empty. The changed-file set a session actually reports comes from the
-- monitor's projection in captain_sessions.metadata->'files'.
--
-- Nothing depends on the view, so a plain DROP suffices. The matching
-- file_read_count/file_written_count columns on captain_session_overview are
-- left in place: CREATE OR REPLACE VIEW cannot drop a column, and commons-db
-- only drops a view when a table diff would break it, so removing them would
-- fail the migration on every existing database.

DROP VIEW IF EXISTS public.captain_session_files;

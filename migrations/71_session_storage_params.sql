-- phase: post

-- captain_sessions is a heartbeat table: every live session repeatedly rewrites
-- last_activity_at, activity_state, health_state and state_version on a row
-- averaging ~950 bytes across 29 columns, and three row-level triggers fire on
-- each of those updates. At the default fillfactor of 100 a heap page is packed
-- with no room for a new row version, so the heartbeat cannot stay HOT and each
-- update migrates the tuple to a fresh page. Plain VACUUM then frees space
-- inside pages it can never hand back, so the heap only ever ratchets upward.
--
-- Measured on a developer database before this change: 7,791 live rows spread
-- over 40,458 heap pages (316 MB) with just 8.8% of pages holding any live
-- tuple -- roughly 40x the ~10 MB the live set warrants. It was also the single
-- largest source of physical reads in the database, at 1.47M of 1.76M total
-- block reads and a 25% heap cache hit ratio while every other table sat above
-- 92%.
--
-- fillfactor reserves in-page room so the heartbeat stays HOT, and the lowered
-- autovacuum scale factors reclaim that space long before a fifth of the table
-- is dead. Atlas OSS does not model PostgreSQL storage parameters, so the
-- post-HCL SQL phase owns them, the same way 50_constraints.sql owns
-- constraint deferrability.
--
-- This script is hash-gated: it re-runs only when its content changes. If an
-- HCL change recreates captain_sessions, bump this script so the storage
-- parameters are restored. Setting the parameters does not rewrite the table --
-- existing bloat is reclaimed by a separate VACUUM FULL or pg_repack.
ALTER TABLE public.captain_sessions SET (
  fillfactor = 70,
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_delay = 0
);

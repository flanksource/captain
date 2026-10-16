-- phase: post

-- captain_session_processes is the monitor's process heartbeat table: every
-- poll that finds a sample worth writing rewrites cpu_percent, memory_percent,
-- memory_rss_bytes, sampled_at and last_heartbeat_at on the live row of each
-- agent process. At the default fillfactor of 100 a heap page has no room for
-- the new row version, so the heartbeat cannot stay HOT and each update
-- migrates the tuple to a fresh page and touches every index.
--
-- Measured on a developer database before this change: 6.1M updates and 432k
-- dead tuples against 42k live rows, from two writes per process every 5s.
-- The monitor now skips unchanged samples, but the rows it still heartbeats
-- are exactly the rewrite-in-place workload fillfactor exists for.
--
-- fillfactor reserves in-page room so the heartbeat stays HOT, and the lowered
-- autovacuum scale factors reclaim that space long before a fifth of the table
-- is dead. Atlas OSS does not model PostgreSQL storage parameters, so the
-- post-HCL SQL phase owns them, the same way 71_session_storage_params.sql owns
-- them for captain_sessions.
--
-- This script is hash-gated: it re-runs only when its content changes. If an
-- HCL change recreates captain_session_processes, bump this script so the
-- storage parameters are restored. Setting the parameters does not rewrite the
-- table -- existing bloat is reclaimed by a separate VACUUM FULL or pg_repack.
ALTER TABLE public.captain_session_processes SET (
  fillfactor = 70,
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_delay = 0
);

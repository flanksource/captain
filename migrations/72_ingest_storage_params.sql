-- phase: post

-- The transcript ingest tables need visibility-map upkeep that the shipped
-- autovacuum defaults never deliver, and two of the three are update-churned
-- rather than append-only.
--
-- captain_messages is written once per row and never updated: insertMessages
-- upserts with ON CONFLICT DO NOTHING. Its default fillfactor of 100 is
-- therefore correct -- there are no updates to keep HOT, and dense pages are
-- what an append-only table wants. What it does need is a far tighter
-- insert-driven vacuum. autovacuum_vacuum_insert_scale_factor defaults to 0.2,
-- so on a measured 714,045-row table roughly 143,000 new rows must accumulate
-- before an insert vacuum fires -- about two weeks at the observed ~10,000
-- messages/day. Across that window the visibility map rots, and because
-- captain_messages_session_sequence_key is the most-scanned index in the
-- database (2,649,235 scans), every one of those index-only scans quietly
-- degrades into full heap fetches.
--
-- Measured on a developer database before this change, a 200-session lookup
-- reported Heap Fetches: 28,614 and shared hit=11,517; after a plain VACUUM
-- restored the map the same plan reported Heap Fetches: 2 and shared hit=2,029
-- -- 5.7x fewer buffers for an identical result.
--
-- captain_turns and captain_model_calls are a different shape. upsertTurns and
-- the model-call upsert both use ON CONFLICT DO UPDATE, and a live session is
-- re-ingested each time its transcript grows, so every turn already persisted
-- is rewritten on every pass. That is update churn, not appends, and it showed:
-- captain_turns sat at 26.9% and captain_model_calls at 38.6% all-visible page
-- coverage. These two get fillfactor to keep the repeated rewrites HOT, the
-- same reasoning as 71_session_storage_params.sql, plus the tightened insert
-- threshold for map upkeep.
--
-- Atlas OSS does not model PostgreSQL storage parameters, so the post-HCL SQL
-- phase owns them. This script is hash-gated: it re-runs only when its content
-- changes. If an HCL change recreates any of these tables, bump this script so
-- the storage parameters are restored. Setting the parameters does not rewrite
-- a table or backfill the visibility map -- the first autovacuum pass does.
ALTER TABLE public.captain_messages SET (
  autovacuum_vacuum_insert_scale_factor = 0.02,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_delay = 0
);

ALTER TABLE public.captain_turns SET (
  fillfactor = 70,
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_insert_scale_factor = 0.05,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_delay = 0
);

ALTER TABLE public.captain_model_calls SET (
  fillfactor = 70,
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_insert_scale_factor = 0.05,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_vacuum_cost_delay = 0
);

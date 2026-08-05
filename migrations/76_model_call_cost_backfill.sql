-- phase: post

-- Repair rows written before finishChatModelCall split its cost buckets.
--
-- The old writer put the whole turn's provider-reported cost into output_cost
-- and left the other four buckets at zero. That made every rollup read as a
-- list-price estimate (provider_cost_usd = 0) while actually reporting billed
-- money, and it overstated output_cost by the entire input and cache spend.
--
-- Old rows are identifiable without ambiguity: a priced call always prices its
-- input, so output_cost > 0 with every other bucket at zero cannot be produced
-- by the current writer. Only the total is recoverable — the per-bucket split
-- is not, and is deliberately left at zero rather than guessed. The rollup
-- views prefer provider_cost_usd per row, so the totals stay exact.

UPDATE public.captain_model_calls
SET provider_cost_usd = output_cost,
    output_cost = 0
WHERE provider_cost_usd = 0
  AND output_cost > 0
  AND input_cost = 0
  AND reasoning_cost = 0
  AND cache_read_cost = 0
  AND cache_write_cost = 0;

-- The same writer set context_tokens to the input count alone. Context is what
-- the model actually read, so cache hits and writes belong in it; without them
-- a cache-heavy turn reports a context far below the window it really used.
UPDATE public.captain_model_calls
SET context_tokens = input_tokens + cache_read_tokens + cache_write_tokens
WHERE context_tokens = input_tokens
  AND cache_read_tokens + cache_write_tokens > 0;

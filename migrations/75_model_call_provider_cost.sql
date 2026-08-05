-- phase: post

-- captain_model_calls.provider_cost_usd carries the model provider's own billed
-- total for a call, alongside the five list-price bucket columns. The rollup
-- views prefer it per row, mirroring api.Cost.Total().
--
-- The non-negative check lives here rather than only in 30_execution.pg.hcl
-- because Atlas does not diff check-constraint expressions: editing the HCL
-- alone applies to freshly created databases but silently leaves existing ones
-- on the old five-column predicate.

ALTER TABLE public.captain_model_calls
  DROP CONSTRAINT IF EXISTS captain_model_calls_costs_nonnegative;

ALTER TABLE public.captain_model_calls
  ADD CONSTRAINT captain_model_calls_costs_nonnegative
  CHECK (
    input_cost >= 0
    AND output_cost >= 0
    AND reasoning_cost >= 0
    AND cache_read_cost >= 0
    AND cache_write_cost >= 0
    AND provider_cost_usd >= 0
  ) NOT VALID;

ALTER TABLE public.captain_model_calls
  VALIDATE CONSTRAINT captain_model_calls_costs_nonnegative;

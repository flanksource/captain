-- phase: post

-- Paired provenance foreign keys form intentional cycles across runs, plans,
-- iterations, calls and events. Deferring their NO ACTION checks allows a
-- complete session aggregate to cascade in one statement while a standalone
-- deletion of referenced provenance still fails at transaction commit. Atlas
-- OSS does not model this PostgreSQL constraint property, so the post-HCL SQL
-- phase owns it.
--
-- This script is hash-gated: it re-runs only when its content changes. If an
-- HCL change recreates one of these constraints, bump this script so the
-- deferrable property is restored.
ALTER TABLE public.captain_prompt_runs
  ALTER CONSTRAINT captain_prompt_runs_input_plan_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_prompt_runs
  ALTER CONSTRAINT captain_prompt_runs_input_plan_revision_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_source_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_source_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_plans
  ALTER CONSTRAINT captain_plans_approved_revision_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_model_calls
  ALTER CONSTRAINT captain_model_calls_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_model_calls
  ALTER CONSTRAINT captain_model_calls_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_events
  ALTER CONSTRAINT captain_events_prompt_run_id_fkey
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.captain_events
  ALTER CONSTRAINT captain_events_iteration_id_fkey
  DEFERRABLE INITIALLY DEFERRED;

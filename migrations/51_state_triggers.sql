-- phase: post

CREATE OR REPLACE FUNCTION public.captain_set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_session_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.lifecycle_status,
      NEW.activity_state,
      NEW.health_state,
      NEW.state_reason
    ) IS DISTINCT FROM ROW(
      OLD.lifecycle_status,
      OLD.activity_state,
      OLD.health_state,
      OLD.state_reason
    ) THEN
      NEW.state_version := OLD.state_version + 1;
      NEW.state_observed_at := observed_at;
    ELSE
      NEW.state_version := OLD.state_version;
      NEW.state_observed_at := OLD.state_observed_at;
    END IF;
  END IF;

  IF NEW.lifecycle_status = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.lifecycle_status IN ('succeeded', 'partial', 'failed', 'cancelled', 'interrupted') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;

  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_prompt_run_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.phase,
      NEW.state,
      NEW.current_iteration,
      NEW.execution_session_id,
      NEW.rendered_spec,
      NEW.result_text,
      NEW.result_json,
      NEW.error
    ) IS DISTINCT FROM ROW(
      OLD.phase,
      OLD.state,
      OLD.current_iteration,
      OLD.execution_session_id,
      OLD.rendered_spec,
      OLD.result_text,
      OLD.result_json,
      OLD.error
    ) THEN
      NEW.version := OLD.version + 1;
    ELSE
      NEW.version := OLD.version;
    END IF;
  END IF;

  IF NEW.state IN ('running', 'waiting') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.finished_at := NULL;
  ELSIF NEW.state IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.finished_at, NEW.created_at, observed_at);
    NEW.finished_at := COALESCE(NEW.finished_at, observed_at);
  END IF;

  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_prompt_iteration_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.state = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.finished_at := NULL;
  ELSIF NEW.state IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.finished_at, NEW.created_at, observed_at);
    NEW.finished_at := COALESCE(NEW.finished_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_turn_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.status = 'open' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.status IN ('ended', 'error', 'interrupted') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_model_call_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
BEGIN
  IF NEW.status = 'running' THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.created_at, observed_at);
    NEW.ended_at := NULL;
  ELSIF NEW.status IN ('succeeded', 'failed', 'cancelled') THEN
    NEW.started_at := COALESCE(NEW.started_at, NEW.ended_at, NEW.created_at, observed_at);
    NEW.ended_at := COALESCE(NEW.ended_at, observed_at);
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_turn_request_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF ROW(
      NEW.state,
      NEW.response,
      NEW.resolved_by,
      NEW.reason
    ) IS DISTINCT FROM ROW(
      OLD.state,
      OLD.response,
      OLD.resolved_by,
      OLD.reason
    ) THEN
      NEW.version := OLD.version + 1;
    ELSE
      NEW.version := OLD.version;
    END IF;
  END IF;

  IF NEW.state = 'pending' THEN
    NEW.resolved_at := NULL;
  ELSE
    NEW.resolved_at := COALESCE(NEW.resolved_at, clock_timestamp());
    IF NEW.resolved_at < NEW.created_at THEN
      NEW.created_at := NEW.resolved_at;
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_set_plan_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.approval_state IN ('approved', 'rejected') THEN
      NEW.approval_created_at := COALESCE(NEW.approval_created_at, clock_timestamp());
    ELSIF NEW.approval_state = 'revision_requested' THEN
      NEW.feedback_at := COALESCE(NEW.feedback_at, clock_timestamp());
    END IF;
  ELSIF NEW.approval_state IS DISTINCT FROM OLD.approval_state THEN
    IF NEW.approval_state IN ('approved', 'rejected') THEN
      NEW.approval_created_at := COALESCE(NEW.approval_created_at, clock_timestamp());
    ELSIF NEW.approval_state = 'revision_requested' THEN
      NEW.feedback_at := COALESCE(NEW.feedback_at, clock_timestamp());
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.captain_sync_prompt_run_iteration()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  UPDATE public.captain_prompt_runs
     SET current_iteration = GREATEST(current_iteration, NEW.iteration)
   WHERE id = NEW.prompt_run_id
     AND current_iteration < NEW.iteration;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS captain_sessions_state_before ON public.captain_sessions;
CREATE TRIGGER captain_sessions_state_before
BEFORE INSERT OR UPDATE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_set_session_state();

DROP TRIGGER IF EXISTS captain_prompt_runs_state_before ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_state_before
BEFORE INSERT OR UPDATE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_set_prompt_run_state();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_state_before ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_state_before
BEFORE INSERT OR UPDATE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_set_prompt_iteration_state();

DROP TRIGGER IF EXISTS captain_turns_state_before ON public.captain_turns;
CREATE TRIGGER captain_turns_state_before
BEFORE INSERT OR UPDATE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_set_turn_state();

DROP TRIGGER IF EXISTS captain_model_calls_state_before ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_state_before
BEFORE INSERT OR UPDATE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_set_model_call_state();

DROP TRIGGER IF EXISTS captain_turn_requests_state_before ON public.captain_turn_requests;
CREATE TRIGGER captain_turn_requests_state_before
BEFORE INSERT OR UPDATE ON public.captain_turn_requests
FOR EACH ROW EXECUTE FUNCTION public.captain_set_turn_request_state();

DROP TRIGGER IF EXISTS captain_plans_state_before ON public.captain_plans;
CREATE TRIGGER captain_plans_state_before
BEFORE INSERT OR UPDATE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_set_plan_state();

DROP TRIGGER IF EXISTS captain_sessions_updated_at_before ON public.captain_sessions;
CREATE TRIGGER captain_sessions_updated_at_before
BEFORE UPDATE ON public.captain_sessions
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_session_processes_updated_at_before ON public.captain_session_processes;
CREATE TRIGGER captain_session_processes_updated_at_before
BEFORE UPDATE ON public.captain_session_processes
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_runs_updated_at_before ON public.captain_prompt_runs;
CREATE TRIGGER captain_prompt_runs_updated_at_before
BEFORE UPDATE ON public.captain_prompt_runs
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_updated_at_before ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_updated_at_before
BEFORE UPDATE ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_plans_updated_at_before ON public.captain_plans;
CREATE TRIGGER captain_plans_updated_at_before
BEFORE UPDATE ON public.captain_plans
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_turns_updated_at_before ON public.captain_turns;
CREATE TRIGGER captain_turns_updated_at_before
BEFORE UPDATE ON public.captain_turns
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_model_calls_updated_at_before ON public.captain_model_calls;
CREATE TRIGGER captain_model_calls_updated_at_before
BEFORE UPDATE ON public.captain_model_calls
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_session_sources_updated_at_before ON public.captain_session_sources;
CREATE TRIGGER captain_session_sources_updated_at_before
BEFORE UPDATE ON public.captain_session_sources
FOR EACH ROW EXECUTE FUNCTION public.captain_set_updated_at();

DROP TRIGGER IF EXISTS captain_prompt_run_iterations_sync_after ON public.captain_prompt_run_iterations;
CREATE TRIGGER captain_prompt_run_iterations_sync_after
AFTER INSERT OR UPDATE OF iteration, prompt_run_id ON public.captain_prompt_run_iterations
FOR EACH ROW EXECUTE FUNCTION public.captain_sync_prompt_run_iteration();

-- Internal trigger functions are not PostgREST RPC endpoints.
REVOKE ALL ON FUNCTION public.captain_set_updated_at() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_session_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_prompt_run_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_prompt_iteration_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_turn_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_model_call_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_turn_request_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_set_plan_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.captain_sync_prompt_run_iteration() FROM PUBLIC;

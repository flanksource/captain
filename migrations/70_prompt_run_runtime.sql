-- phase: post

UPDATE public.captain_prompt_runs
SET
  runtime = CASE
    WHEN jsonb_typeof(rendered_spec -> 'runtime') = 'object' THEN rendered_spec -> 'runtime'
    ELSE jsonb_strip_nulls(jsonb_build_object(
      'mode', 'run',
      'resolved', jsonb_strip_nulls(jsonb_build_object(
        'backend', NULLIF(rendered_spec ->> 'backend', ''),
        'model', NULLIF(rendered_spec ->> 'model', ''),
        'effort', NULLIF(COALESCE(
          rendered_spec #>> '{config,effort}',
          rendered_spec #>> '{input,effort}'
        ), '')
      ))
    ))
  END,
  rendered_spec = rendered_spec - 'runtime'
WHERE runtime = '{}'::jsonb
  AND (
    jsonb_typeof(rendered_spec -> 'runtime') = 'object'
    OR NULLIF(rendered_spec ->> 'backend', '') IS NOT NULL
    OR NULLIF(rendered_spec ->> 'model', '') IS NOT NULL
  );

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
      NEW.runtime,
      NEW.result_text,
      NEW.result_json,
      NEW.error
    ) IS DISTINCT FROM ROW(
      OLD.phase,
      OLD.state,
      OLD.current_iteration,
      OLD.execution_session_id,
      OLD.rendered_spec,
      OLD.runtime,
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

REVOKE ALL ON FUNCTION public.captain_set_prompt_run_state() FROM PUBLIC;

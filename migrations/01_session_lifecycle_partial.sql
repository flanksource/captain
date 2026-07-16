-- phase: pre

DO $$
BEGIN
  IF to_regtype('public.captain_session_lifecycle_status') IS NOT NULL THEN
    ALTER TYPE public.captain_session_lifecycle_status
      ADD VALUE IF NOT EXISTS 'partial' BEFORE 'failed';
  END IF;
END
$$;

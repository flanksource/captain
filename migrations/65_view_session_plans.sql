-- phase: post

CREATE OR REPLACE VIEW public.captain_session_plans
WITH (security_barrier = true)
AS
SELECT
  p.id,
  p.source_session_id AS session_id,
  p.source_prompt_run_id,
  p.source_iteration_id,
  p.source_turn_id,
  p.title,
  p.slug,
  p.path,
  p.variant,
  p.spec_profile,
  p.approval_state,
  p.approved_revision_id,
  p.approval_comment,
  p.approved_by,
  p.approval_created_at,
  p.feedback_at,
  p.created_at,
  p.updated_at,
  latest_revision.id AS latest_revision_id,
  latest_revision.revision AS latest_revision,
  latest_revision.plan_markdown AS latest_plan_markdown,
  latest_revision.content_hash AS latest_content_hash,
  latest_revision.feedback AS latest_feedback,
  latest_revision.created_by AS latest_created_by,
  latest_revision.created_at AS latest_created_at,
  approved_revision.revision AS approved_revision,
  approved_revision.plan_markdown AS approved_plan_markdown,
  approved_revision.content_hash AS approved_content_hash,
  COALESCE(approved_revision.id, latest_revision.id) AS current_revision_id,
  COALESCE(approved_revision.revision, latest_revision.revision) AS current_revision,
  COALESCE(approved_revision.plan_markdown, latest_revision.plan_markdown) AS plan_markdown,
  COALESCE(approved_revision.content_hash, latest_revision.content_hash) AS content_hash
FROM public.captain_plans p
LEFT JOIN LATERAL (
  SELECT r.*
  FROM public.captain_plan_revisions r
  WHERE r.plan_id = p.id
  ORDER BY r.revision DESC, r.created_at DESC, r.id DESC
  LIMIT 1
) latest_revision ON true
LEFT JOIN public.captain_plan_revisions approved_revision
  ON approved_revision.id = p.approved_revision_id;

COMMENT ON VIEW public.captain_session_plans IS
  'Plan rows with latest and approved immutable revisions for the SessionInspector plan tab.';

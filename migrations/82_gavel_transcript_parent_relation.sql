-- phase: post

-- Re-label the transcript-bearing children of a Gavel admission root.
--
-- A Gavel run produces two rows for one provider session: the `gavel` admission
-- root, which is a provider-identity bridge holding no transcript, and a
-- `claude`/`codex` child that holds the actual transcript, messages and token
-- accounting. ensureAgentSession created that child with parent_relation
-- 'agent', while GetTranscriptSession looks for 'transcript' — so Captain's own
-- transcript lookup could never resolve a Gavel run's transcript, and the
-- session read fell back to the admission root's empty aggregate.
--
-- ensureAgentSession now writes 'transcript'. reconcileSessionHierarchy refuses
-- to change a relation once a parent is set — deliberately, so a hierarchy is
-- not silently rewritten — which means a resumed run whose child predates that
-- change fails loudly with a session conflict instead of being repaired. This
-- migration is that repair, done once, for exactly the rows the old writer
-- produced.
--
-- The predicate is unambiguous: only a Gavel admission root parents a provider
-- session row, and the transcript child is the only child it creates. Rows
-- already at 'transcript' are left alone, so this is safe to re-run.
--
-- captain_sessions_transcript_parent_key allows one transcript child per parent
-- and, on an upgrade, already exists when this post-phase script runs. So only
-- the oldest candidate of a root is relabelled, and only when the root has no
-- transcript child yet; a later Claude/Codex child of the same root is a
-- different conversation and stays 'agent' (see
-- 04_demote_extra_transcript_children.sql).

UPDATE public.captain_sessions AS child
SET parent_relation = 'transcript'
FROM (
  SELECT DISTINCT ON (candidate.parent_session_id) candidate.id
  FROM public.captain_sessions AS candidate
  JOIN public.captain_sessions AS root ON root.id = candidate.parent_session_id
  WHERE candidate.parent_relation = 'agent'
    AND candidate.source IN ('claude', 'codex')
    AND root.source = 'gavel'
    AND NOT EXISTS (
      SELECT 1 FROM public.captain_sessions AS transcript
      WHERE transcript.parent_session_id = root.id
        AND transcript.parent_relation = 'transcript'
    )
  ORDER BY candidate.parent_session_id, candidate.created_at, candidate.id
) AS oldest
WHERE child.id = oldest.id;

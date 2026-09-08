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

UPDATE public.captain_sessions AS child
SET parent_relation = 'transcript'
FROM public.captain_sessions AS root
WHERE child.parent_session_id = root.id
  AND child.parent_relation = 'agent'
  AND child.source IN ('claude', 'codex')
  AND root.source = 'gavel';

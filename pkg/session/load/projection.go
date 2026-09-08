package load

import (
	"context"
	"sort"

	"github.com/flanksource/captain/pkg/session"
)

// ProjectionFacts is the stored projection of a session: the monitor-owned
// metadata blob, the git blob, the context-window reading, the authoritative
// plan revision, and the file sets of the session's descendants.
//
// Approvals are deliberately absent. The metadata blob's copy counts every
// operational tool use as an approval and reads as "200 approved"; only
// Requests may supply them.
type ProjectionFacts struct {
	Metadata session.Metadata
	Git      session.GitState
	Context  *session.Context
	// Plan is the authoritative revision from captain_plans — the approved one
	// when there is one. A transcript cannot know about an approval, so this
	// outranks whatever the branch already found.
	Plan *session.Plan
	// ThreadFiles is the union of the descendants' file sets, nil when the
	// session has no descendants. A session that spawns sub-agents edits nothing
	// itself, so reporting only its own set hides the whole thread's output; a
	// leaf keeps its own set so the hierarchy still says who touched what.
	ThreadFiles *session.ChangedFiles
}

// Projection contributes the facts that are properties of the stored row rather
// than of the branch that produced the session.
//
// It runs at database precedence so a transcript-derived value, which is fresher
// than the stored copy, keeps the field. Three branches used to carry their own
// subset of this, so a session's changed files, git branch and plan appeared or
// vanished depending on which one served the request.
func Projection(facts ProjectionFacts) Contributor { return projectionContributor{facts: facts} }

type projectionContributor struct {
	facts ProjectionFacts
}

func (c projectionContributor) Source() Source { return SourceDatabase }

func (c projectionContributor) Contribute(_ context.Context, aggregate *session.Session, _ *Provenance) error {
	metadata := c.facts.Metadata
	setString(&aggregate.Model, metadata.Model)
	setString(&aggregate.Provider, metadata.Provider)
	if len(aggregate.Files.Read) == 0 && len(aggregate.Files.Written) == 0 {
		aggregate.Files = metadata.Files
	}
	if len(aggregate.Todos) == 0 {
		aggregate.Todos = metadata.Todos
	}
	if aggregate.Plan == nil {
		aggregate.Plan = metadata.Plan
	}
	if aggregate.Git == (session.GitState{}) {
		aggregate.Git = c.facts.Git
	}
	if aggregate.Context == nil {
		aggregate.Context = c.facts.Context
	}
	if c.facts.Plan != nil {
		aggregate.Plan = c.facts.Plan
	}
	if c.facts.ThreadFiles != nil {
		aggregate.Files = mergeFiles(aggregate.Files, *c.facts.ThreadFiles)
	}
	return nil
}

func mergeFiles(own, descendants session.ChangedFiles) session.ChangedFiles {
	return session.ChangedFiles{
		Read:    sortedUnique(append(append([]string{}, own.Read...), descendants.Read...)),
		Written: sortedUnique(append(append([]string{}, own.Written...), descendants.Written...)),
	}
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

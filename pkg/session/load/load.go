package load

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/session"
)

// Contributor supplies the facts one source knows about a session.
//
// A contributor adds only what Provenance lets it claim, so ordering — not
// per-contributor conditionals — decides who wins a contested facet. Every
// contributor runs on every load: none is selected in preference to another.
type Contributor interface {
	// Source is the contributor's precedence identity; it must appear in Precedence.
	Source() Source
	// Contribute adds this source's facts to the aggregate. Returning ErrNoFacts
	// means the source simply has nothing for this session, which is ordinary.
	Contribute(ctx context.Context, aggregate *session.Session, prov *Provenance) error
}

// ErrNoFacts reports that a source holds nothing for this session. It is not a
// failure: a session with no transcript yet is the normal case for a run whose
// transcript has not been ingested.
var ErrNoFacts = fmt.Errorf("no facts for session")

// Result is one composed session and the record of where each facet came from.
type Result struct {
	Session    *session.Session
	Provenance Provenance
}

// Load composes the aggregate by running contributors in Precedence order.
//
// A source that fails is reported but does not fail the read: the other
// contributors still hold facts, and a transcript that cannot be parsed must not
// hide the approval that is blocking the run. Failures are returned alongside the
// aggregate so a caller can surface them rather than silently degrade.
func Load(ctx context.Context, contributors ...Contributor) (Result, []error) {
	result := Result{Session: &session.Session{}}
	var failures []error
	for _, source := range Precedence {
		for _, contributor := range contributors {
			if contributor.Source() != source {
				continue
			}
			err := contributor.Contribute(ctx, result.Session, &result.Provenance)
			if err != nil && !errors.Is(err, ErrNoFacts) {
				failures = append(failures, fmt.Errorf("%s: %w", source, err))
			}
		}
	}
	Reconcile(result.Session, result.Session.Requests)
	return result, failures
}

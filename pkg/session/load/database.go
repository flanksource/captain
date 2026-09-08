package load

import "github.com/flanksource/captain/pkg/session"

// Database contributes the canonical aggregate assembled from the stored
// message, turn and usage rows.
//
// It claims the same facets as Transcript and loses them by ordering alone: a
// session with both a transcript and stored rows now renders the transcript and
// keeps every fact the store holds that the transcript does not, instead of the
// two being mutually exclusive branches.
func Database(stored *session.Session) Contributor {
	return aggregateContributor{source: SourceDatabase, from: stored}
}

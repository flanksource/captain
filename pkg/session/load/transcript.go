package load

import "github.com/flanksource/captain/pkg/session"

// Transcript contributes a session parsed from the provider's own transcript
// file. It outranks the stored rows for messages, turns and usage: the file is
// what actually happened, and the store is a projection of it that can lag.
//
// The parsed aggregate is supplied already built. Reading and parsing the
// transcript is the wiring layer's work; this package performs no I/O, which is
// what keeps composition orthogonal to where the facts came from.
func Transcript(parsed *session.Session) Contributor {
	return aggregateContributor{source: SourceTranscript, from: parsed}
}

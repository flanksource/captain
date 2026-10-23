package load

import "github.com/flanksource/captain/pkg/session"

// Transcript contributes a session parsed from the provider's own transcript
// file. It outranks the stored rows for messages, turns and usage: the file is
// what actually happened, and the store is a projection of it that can lag.
//
// Only its conversational messages are contributed, as ingestion stores and a
// live follow streams: synthetic lifecycle rows (ai-title, stop-hook summaries)
// are events, and a read that listed them would hand a reader a cursor the
// follow never emits.
//
// The parsed aggregate is supplied already built. Reading and parsing the
// transcript is the wiring layer's work; this package performs no I/O, which is
// what keeps composition orthogonal to where the facts came from.
func Transcript(parsed *session.Session) Contributor {
	if parsed != nil {
		projected := *parsed
		projected.Messages = session.ConversationalMessages(parsed.Messages)
		parsed = &projected
	}
	return aggregateContributor{source: SourceTranscript, from: parsed}
}

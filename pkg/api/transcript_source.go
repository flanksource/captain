package api

// Transcript sources are the session sources whose rows are backed by a
// transcript file Captain ingests: the logs the claude and codex agents write,
// and the one Captain writes itself for an API-mode run, whose provider keeps
// no log of its own.
const (
	TranscriptSourceClaude = "claude"
	TranscriptSourceCodex  = "codex"
	TranscriptSourceAPI    = "api"
)

// IsTranscriptSource reports whether a session source names a transcript file.
func IsTranscriptSource(source string) bool {
	switch source {
	case TranscriptSourceClaude, TranscriptSourceCodex, TranscriptSourceAPI:
		return true
	default:
		return false
	}
}

// TranscriptFormat is the file format a transcript source is written in, which
// selects its parser. An API transcript is written in Claude Code's format: the
// content blocks of an API exchange are the ones a Claude transcript records.
func TranscriptFormat(source string) string {
	if source == TranscriptSourceAPI {
		return TranscriptSourceClaude
	}
	return source
}

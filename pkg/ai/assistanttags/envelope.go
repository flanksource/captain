package assistanttags

import (
	"encoding/json"
	"strings"
)

// resultEnvelope is the subset of gavel's result/plan final-result envelope
// needed to recognize a structured completion message. The full schema (plan,
// questions) is owned by flanksource/gavel and intentionally not modeled here —
// captain only reads these messages, and the dependency runs gavel → captain.
type resultEnvelope struct {
	Summary   string `json:"summary"`
	EndStatus string `json:"endStatus"`
}

var envelopeEndStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"ask":       true,
}

// EnvelopeSummary returns the human-readable summary of a gavel result-envelope
// final message, and ok=false when text is not a recognized envelope. The whole
// (trimmed) message must be a single JSON object carrying a non-empty summary and
// an endStatus of completed|failed|ask — strict enough that ordinary assistant
// prose or unrelated JSON is never mistaken for an envelope.
func EnvelopeSummary(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var env resultEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return "", false
	}
	summary := strings.TrimSpace(env.Summary)
	if summary == "" || !envelopeEndStatuses[env.EndStatus] {
		return "", false
	}
	return summary, true
}

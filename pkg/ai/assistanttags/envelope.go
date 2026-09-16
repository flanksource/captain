package assistanttags

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// resultEnvelope is the subset of gavel's result/plan final-result envelope
// needed to recognize a structured completion message and the questions it asks.
// The rest of the schema (plan, verdicts) is owned by flanksource/gavel and
// intentionally not modeled here — captain only reads these messages, and the
// dependency runs gavel → captain. Questions stay raw so a malformed list never
// stops the summary from being recognized.
type resultEnvelope struct {
	Summary   string          `json:"summary"`
	EndStatus string          `json:"endStatus"`
	Questions json.RawMessage `json:"questions"`
}

// Envelope is a recognized result envelope: its summary, how the run ended, and
// the questions it left for a person when it ended on ask.
type Envelope struct {
	Summary   string
	EndStatus string
	Questions []api.TerminalQuestion
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
	envelope, ok := decodeEnvelope(text)
	return envelope.Summary, ok
}

// ParseEnvelope reads a result envelope with its questions. ok=false means text
// is not an envelope (see EnvelopeSummary). Questions are read only from an ask:
// an ask whose questions cannot be asked is reported as an error with ok=true, so
// a caller can tell a broken ask from ordinary text. An empty list is no questions.
func ParseEnvelope(text string) (Envelope, bool, error) {
	decoded, ok := decodeEnvelope(text)
	if !ok {
		return Envelope{}, false, nil
	}
	envelope := Envelope{Summary: decoded.Summary, EndStatus: decoded.EndStatus}
	if decoded.EndStatus != "ask" {
		return envelope, true, nil
	}
	var raw any
	if len(decoded.Questions) > 0 {
		if err := json.Unmarshal(decoded.Questions, &raw); err != nil {
			return envelope, true, fmt.Errorf("envelope questions: %w", err)
		}
	}
	if items, isList := raw.([]any); raw == nil || (isList && len(items) == 0) {
		return envelope, true, nil
	}
	questions, err := api.TerminalQuestionsFromInput(map[string]any{"questions": raw})
	if err != nil {
		return envelope, true, fmt.Errorf("envelope questions: %w", err)
	}
	envelope.Questions = questions
	return envelope, true, nil
}

func decodeEnvelope(text string) (resultEnvelope, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return resultEnvelope{}, false
	}
	var envelope resultEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return resultEnvelope{}, false
	}
	envelope.Summary = strings.TrimSpace(envelope.Summary)
	if envelope.Summary == "" || !envelopeEndStatuses[envelope.EndStatus] {
		return resultEnvelope{}, false
	}
	return envelope, true
}

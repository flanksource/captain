package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	native "github.com/flanksource/captain/pkg/ai/jev"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

type jevAnswerRow struct {
	question      string
	answer        string
	confidence    string
	probabilities string
}

func (jevAnswerRow) Columns() []clickyapi.ColumnDef {
	return []clickyapi.ColumnDef{
		clicky.Column("question").Label("Question").Build(),
		clicky.Column("answer").Label("Answer").Build(),
		clicky.Column("confidence").Label("Confidence").Build(),
		clicky.Column("probabilities").Label("Probabilities").Build(),
	}
}

func (row jevAnswerRow) Row() map[string]any {
	return map[string]any{
		"question":      row.question,
		"answer":        row.answer,
		"confidence":    row.confidence,
		"probabilities": row.probabilities,
	}
}

func jevResponsePretty(source string) clickyapi.Textable {
	var envelope struct {
		Value    json.RawMessage `json:"value"`
		TypeSafe native.Response `json:"typesafe"`
	}
	if err := json.Unmarshal([]byte(source), &envelope); err != nil {
		panic(fmt.Errorf("decode Jev response envelope: %w", err))
	}
	if len(envelope.Value) == 0 || envelope.TypeSafe.Model == "" || len(envelope.TypeSafe.Answers) == 0 ||
		envelope.TypeSafe.Usage.InputTokens == nil || envelope.TypeSafe.Usage.OutputTokens == nil {
		panic("Jev response envelope requires value, model, answers, and usage")
	}

	rows := make([]jevAnswerRow, 0, len(envelope.TypeSafe.Answers))
	names := make([]string, 0, len(envelope.TypeSafe.Answers))
	for name := range envelope.TypeSafe.Answers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		answer := envelope.TypeSafe.Answers[name]
		value, probabilities := jevAnswerDisplay(name, answer)
		if answer.Type != "noul" && answer.Confidence == nil {
			panic(fmt.Errorf("render Jev answer %q: missing confidence", name))
		}
		confidence := ""
		if answer.Confidence != nil {
			confidence = jevPercent(*answer.Confidence)
		}
		rows = append(rows, jevAnswerRow{
			question:      clickyapi.PrettifyFieldName(name),
			answer:        value,
			confidence:    confidence,
			probabilities: probabilities,
		})
	}
	return clickyapi.Text{}.
		Append("Model: ", "text-gray-500").
		Append(envelope.TypeSafe.Model).
		Append(fmt.Sprintf("  Tokens: %d in / %d out", *envelope.TypeSafe.Usage.InputTokens, *envelope.TypeSafe.Usage.OutputTokens), "text-gray-500").
		NewLine().
		Add(clickyapi.NewTableFrom(rows))
}

func jevAnswerDisplay(name string, answer native.Answer) (string, string) {
	switch answer.Type {
	case "noul":
		if answer.Noul == nil {
			panic(fmt.Errorf("render Jev answer %q: missing noul", name))
		}
		return jevPercent(*answer.Noul) + " yes", ""
	case "choice":
		if answer.Choice == nil {
			panic(fmt.Errorf("render Jev answer %q: missing choice", name))
		}
		return *answer.Choice, jevChoiceProbabilities(name, answer.Probabilities)
	case "score":
		if answer.Score == nil || len(answer.Legend) < 2 {
			panic(fmt.Errorf("render Jev answer %q: missing score or legend", name))
		}
		return strconv.FormatFloat(*answer.Score, 'f', -1, 64) + fmt.Sprintf(" / %d", len(answer.Legend)-1), jevScoreProbabilities(name, answer)
	default:
		panic(fmt.Errorf("render Jev answer %q: unsupported type %q", name, answer.Type))
	}
}

func jevChoiceProbabilities(name string, probabilities map[string]float64) string {
	if len(probabilities) == 0 {
		panic(fmt.Errorf("render Jev answer %q: missing choice probabilities", name))
	}
	names := make([]string, 0, len(probabilities))
	for name := range probabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+" "+jevPercent(probabilities[name]))
	}
	return strings.Join(parts, ", ")
}

func jevScoreProbabilities(name string, answer native.Answer) string {
	parts := make([]string, 0, len(answer.Legend))
	for index := range len(answer.Legend) {
		key := strconv.Itoa(index)
		label, hasLabel := answer.Legend[key]
		probability, hasProbability := answer.Probabilities[key]
		if !hasLabel || !hasProbability {
			panic(fmt.Errorf("render Jev answer %q: missing score level %d", name, index))
		}
		parts = append(parts, label+" "+jevPercent(probability))
	}
	return strings.Join(parts, ", ")
}

func jevPercent(value float64) string {
	return fmt.Sprintf("%.0f%%", value*100)
}

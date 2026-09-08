package load

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

// PromptRunFacts is what a captain-launched prompt run knows about the session
// it produced: the prompt it was given, what came back, and the runtime it
// resolved to. Runtime is flattened to the values the wiring layer already
// picked (resolved before requested), so composition stays free of the store.
type PromptRunFacts struct {
	RunID            string
	State            string
	PromptMarkdown   string
	ResultText       string
	ResultJSON       map[string]any
	RenderedSpec     map[string]any
	Error            string
	Provider         string
	Model            string
	Mode             string
	Effort           string
	QueuedAt         time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	SuppressMessages bool
}

// PromptRun contributes the run's own facts to the session it produced.
//
// It is one contributor, not a constructor plus an enricher. Previously a run
// only built a session when no transcript had been recorded, and the enricher
// that ran otherwise carried a strict subset — so a run whose transcript existed
// silently lost its rendered spec, its structured output and its failure. Every
// fact here now applies whatever else was found; only the synthesised messages
// stand down, and only when a real transcript already claimed them.
func PromptRun(facts PromptRunFacts) Contributor { return promptRunContributor{facts: facts} }

type promptRunContributor struct {
	facts PromptRunFacts
}

func (c promptRunContributor) Source() Source { return SourcePromptRun }

func (c promptRunContributor) Contribute(_ context.Context, aggregate *session.Session, prov *Provenance) error {
	if c.facts.RunID == "" {
		return ErrNoFacts
	}
	c.mergeRuntime(aggregate)
	resultText, err := c.resultText()
	if err != nil {
		return err
	}
	if !c.facts.SuppressMessages && prov.Claim(FacetMessages, SourcePromptRun) {
		if c.facts.PromptMarkdown != "" {
			aggregate.Messages = append(aggregate.Messages, c.message("user", c.facts.PromptMarkdown))
		}
		if resultText != "" {
			aggregate.Messages = append(aggregate.Messages, c.message("assistant", resultText))
		}
	}
	if c.facts.Error != "" {
		aggregate.Events = append(aggregate.Events, session.Event{
			Type: "error", Scope: "prompt_run", Timestamp: c.facts.FinishedAt, UUID: c.facts.RunID,
			Data: map[string]any{"message": c.facts.Error, "state": c.facts.State},
		})
	}
	if !prov.Claim(FacetPrompt, SourcePromptRun) {
		return nil
	}
	if len(c.facts.RenderedSpec) > 0 {
		rendered, marshalErr := json.Marshal(c.facts.RenderedSpec)
		if marshalErr != nil {
			return fmt.Errorf("encode prompt run %s rendered spec: %w", c.facts.RunID, marshalErr)
		}
		aggregate.Prompt = rendered
	}
	output, err := c.structuredOutput()
	aggregate.StructuredOutput = output
	return err
}

func (c promptRunContributor) mergeRuntime(aggregate *session.Session) {
	setString(&aggregate.InitialPrompt, c.facts.PromptMarkdown)
	setString(&aggregate.Provider, c.facts.Provider)
	setString(&aggregate.Model, c.facts.Model)
	setString(&aggregate.ReasoningEffort, c.facts.Effort)
	setMode(&aggregate.ModelMode, api.RuntimeMode(c.facts.Mode))
	started := c.facts.StartedAt
	if started == nil || started.IsZero() {
		started = &c.facts.QueuedAt
	}
	setTime(&aggregate.StartedAt, started)
	setTime(&aggregate.EndedAt, c.facts.FinishedAt)
}

func (c promptRunContributor) message(role, text string) session.Message {
	return session.Message{
		ID: c.facts.RunID + "-" + role, Role: role,
		Parts: []session.Part{{Type: session.PartText, Text: text}},
	}
}

func (c promptRunContributor) resultText() (string, error) {
	if c.facts.ResultText != "" {
		return c.facts.ResultText, nil
	}
	if len(c.facts.ResultJSON) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(c.facts.ResultJSON)
	if err != nil {
		return "", fmt.Errorf("encode prompt run %s result: %w", c.facts.RunID, err)
	}
	return string(raw), nil
}

// structuredOutput decodes the object a schema-constrained run returned. A run
// whose spec declares no output schema has none by construction; a run that
// declares one and returned something a schema could not have produced is
// reported rather than silently read as having returned nothing.
func (c promptRunContributor) structuredOutput() (map[string]any, error) {
	if c.facts.ResultJSON != nil {
		return c.facts.ResultJSON, nil
	}
	if !declaresOutputSchema(c.facts.RenderedSpec) || !json.Valid([]byte(c.facts.ResultText)) {
		return nil, nil
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(c.facts.ResultText), &output); err != nil {
		return nil, fmt.Errorf("decode prompt run %s structured output: %w", c.facts.RunID, err)
	}
	return output, nil
}

func declaresOutputSchema(rendered map[string]any) bool {
	schema, ok := rendered["outputSchema"].(map[string]any)
	return ok && len(schema) > 0
}

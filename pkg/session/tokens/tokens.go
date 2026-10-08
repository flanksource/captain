package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

type Options struct {
	Method   string   `json:"method"`
	RowIDs   []string `json:"rowIds,omitempty"`
	Revision *int64   `json:"revision,omitempty"`
}

var ErrInvalidRequest = errors.New("invalid token sizing request")
var ErrRevisionConflict = errors.New("session revision changed")

type Row struct {
	RowID      string         `json:"rowId"`
	Attributed bool           `json:"attributed"`
	Size       *api.TokenSize `json:"size,omitempty"`
	Error      string         `json:"error,omitempty"`
}
type Result struct {
	SessionID string `json:"sessionId"`
	Revision  int64  `json:"revision"`
	Rows      []Row  `json:"rows"`
}

func Size(ctx context.Context, detail *session.Session, opts Options) (Result, error) {
	if detail == nil {
		return Result{}, fmt.Errorf("session has no transcript")
	}
	if opts.Method == "" {
		opts.Method = "estimate"
	}
	if opts.Method != "estimate" && opts.Method != "provider" {
		return Result{}, fmt.Errorf("%w: method %q", ErrInvalidRequest, opts.Method)
	}
	if opts.Revision != nil && *opts.Revision != detail.Revision {
		return Result{}, fmt.Errorf("%w: expected %d, received %d", ErrRevisionConflict, *opts.Revision, detail.Revision)
	}
	selected := map[string]bool{}
	for _, id := range opts.RowIDs {
		selected[id] = false
	}
	var projected []tokenRow
	for seq, msg := range detail.Messages {
		base := msg.ID
		if base == "" && msg.Provenance != nil {
			base = msg.Provenance.UUID
		}
		if base == "" {
			base = fmt.Sprintf("m%d", seq)
		}
		if msg.Provenance != nil && msg.Provenance.APIErrorStatus != 0 {
			id := base + "-err"
			if _, ok := selected[id]; len(selected) == 0 || ok {
				if ok {
					selected[id] = true
				}
				projected = append(projected, tokenRow{ID: id, Message: msg, Part: session.Part{Type: "api-error"}})
			}
			continue
		}
		for index, part := range msg.Parts {
			id := fmt.Sprintf("%s-%d", base, index)
			if len(selected) > 0 {
				if _, ok := selected[id]; !ok {
					continue
				}
				selected[id] = true
			}
			projected = append(projected, tokenRow{ID: id, Message: msg, Part: part})
		}
	}
	for id, found := range selected {
		if !found {
			return Result{}, fmt.Errorf("%w: row %q not found in session %s", ErrInvalidRequest, id, detail.ID)
		}
	}
	return sizeRows(ctx, detail, projected, opts.Method)
}

type tokenRow struct {
	ID      string
	Message session.Message
	Part    session.Part
}

func sizeRows(ctx context.Context, detail *session.Session, rows []tokenRow, method string) (Result, error) {
	result := Result{SessionID: detail.ID, Revision: detail.Revision, Rows: []Row{}}
	for _, projected := range rows {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		row := Row{RowID: projected.ID, Attributed: projected.Part.EstimatedCost != nil}
		size, err := sizePart(ctx, projected.Part, partSizingOptions{Model: rowModel(detail, projected.Message), Role: projected.Message.Role, Method: method})
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			row.Error = err.Error()
		} else {
			row.Size = &size
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

type partSizingOptions struct {
	Model  api.Model
	Role   string
	Method string
}

func rowModel(detail *session.Session, msg session.Message) api.Model {
	model := api.Model{Name: detail.Model, Mode: detail.ModelMode}
	for _, turn := range detail.Turns {
		matches := turn.ID == msg.TurnID && msg.TurnID != ""
		for _, id := range turn.MessageIDs {
			if id == msg.ID {
				matches = true
			}
		}
		if matches {
			if turn.Model != "" {
				model.Name = turn.Model
			}
			if turn.Mode != "" {
				model.Mode = api.RuntimeMode(turn.Mode)
			}
			break
		}
	}
	if msg.Provenance != nil && msg.Provenance.Model != "" {
		model.Name = msg.Provenance.Model
	}
	return model
}

func sizePart(ctx context.Context, part session.Part, opts partSizingOptions) (api.TokenSize, error) {
	direction := "input"
	if opts.Role == "assistant" {
		direction = "output"
	}
	if part.Type == session.PartReasoning {
		direction = "reasoning"
	}
	request := api.Spec{Model: opts.Model, Prompt: api.Prompt{User: part.Text}}
	if part.Type == session.PartFile {
		request.Prompt.User = ""
		request.Prompt.Attachments = []api.AttachmentRef{{URL: part.URL, MediaType: part.MediaType, Filename: part.Filename}}
		if part.URL != "" && !strings.Contains(part.URL, ":") {
			request.Prompt.Attachments[0].Path = part.URL
			request.Prompt.Attachments[0].URL = ""
		}
		if part.URL == "" {
			request.Prompt.Attachments[0].ID = part.AttachmentID
		}
	} else if part.Type == session.PartTool || strings.HasPrefix(part.Type, "tool-") {
		return sizeToolPart(ctx, part, opts)
	} else if part.Type != session.PartText && part.Type != session.PartReasoning {
		return api.TokenSize{}, fmt.Errorf("row type %q has no model content to count", part.Type)
	}
	if request.Prompt.User == "" && part.Type == session.PartReasoning {
		return api.TokenSize{}, fmt.Errorf("encrypted reasoning content is unavailable")
	}
	result, err := ai.SizeTokens(ctx, ai.TokenSizeOptions{Request: request, Method: opts.Method, Direction: direction})
	if err != nil {
		return result, err
	}
	result.Coverage.Excluded = append(result.Coverage.Excluded, "surrounding conversation and historical cache usage")
	if part.Type == session.PartReasoning {
		result.Coverage.Excluded = append(result.Coverage.Excluded, "encrypted reasoning and signatures")
	}
	result.Coverage.Partial = true
	return result, nil
}

func sizeToolPart(ctx context.Context, part session.Part, opts partSizingOptions) (api.TokenSize, error) {
	name := part.ToolName
	if name == "" {
		name = strings.TrimPrefix(part.Type, "tool-")
	}
	args, err := json.Marshal(struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input,omitempty"`
	}{name, json.RawMessage(part.Input)})
	if err != nil {
		return api.TokenSize{}, fmt.Errorf("tool input: %w", err)
	}
	output, err := ai.SizeTokens(ctx, ai.TokenSizeOptions{Request: api.Spec{Model: opts.Model, Prompt: api.Prompt{User: string(args)}}, Method: opts.Method, Direction: "output"})
	if err != nil {
		return output, err
	}
	if len(part.Output) > 0 || part.ErrorText != "" {
		result, err := json.Marshal(struct {
			Output json.RawMessage `json:"output,omitempty"`
			Error  string          `json:"error,omitempty"`
		}{json.RawMessage(part.Output), part.ErrorText})
		if err != nil {
			return output, fmt.Errorf("tool output: %w", err)
		}
		input, err := ai.SizeTokens(ctx, ai.TokenSizeOptions{Request: api.Spec{Model: opts.Model, Prompt: api.Prompt{User: string(result)}}, Method: opts.Method})
		if err != nil {
			return output, err
		}
		output.Usage = output.Usage.Add(input.Usage)
		output.TotalTokens += input.TotalTokens
		if output.CostUSD != nil && input.CostUSD != nil {
			cost := *output.CostUSD + *input.CostUSD
			output.CostUSD = &cost
		} else {
			output.CostUSD = nil
		}
		output.Coverage.Excluded = append(output.Coverage.Excluded, input.Coverage.Excluded...)
	}
	output.Coverage.Excluded = append(output.Coverage.Excluded, "surrounding conversation and historical cache usage", "tool payload counted as isolated JSON text")
	output.Coverage.Partial = true
	return output, nil
}

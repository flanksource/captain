package api

import (
	"encoding/json"
	"fmt"
)

// StructuredToolResult is a ToolHandler result with one representation per audience,
// mirroring MCP's content and structuredContent: Content is what the model
// reads, Structured is the data a UI renders and the tool's output schema
// describes. A handler that has a single representation returns it directly.
type StructuredToolResult struct {
	Content    string
	Structured any
}

// ToolOutputParts is a handler result split for the two places it goes. Display
// is nil when the model text is also what a UI should render.
type ToolOutputParts struct {
	Text    string
	Display json.RawMessage
}

// ModelValue is the part of a ToolHandler result the model receives and its
// conversation history keeps: a StructuredToolResult's Content, otherwise the
// value itself.
func ModelValue(value any) any {
	if result, ok := value.(StructuredToolResult); ok {
		return result.Content
	}
	return value
}

// ToolOutput splits a ToolHandler result into the model-facing text and the
// display value. Every provider that runs caller tools in-process converts
// through here, so a StructuredToolResult means the same thing on each of them.
func ToolOutput(value any) (ToolOutputParts, error) {
	switch v := value.(type) {
	case nil:
		return ToolOutputParts{}, nil
	case string:
		return ToolOutputParts{Text: v}, nil
	case StructuredToolResult:
		if v.Content == "" {
			return ToolOutputParts{}, fmt.Errorf("tool result has structured data but no content for the model")
		}
		display, err := json.Marshal(v.Structured)
		if err != nil {
			return ToolOutputParts{}, fmt.Errorf("encode structured tool result: %w", err)
		}
		return ToolOutputParts{Text: v.Content, Display: display}, nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ToolOutputParts{}, fmt.Errorf("encode tool result: %w", err)
		}
		return ToolOutputParts{Text: string(encoded)}, nil
	}
}

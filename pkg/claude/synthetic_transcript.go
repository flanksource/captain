package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

// SyntheticTranscript writes a Claude Code-format transcript of a conversation
// Captain held with a model through its provider's API. An API run leaves no log
// of its own; written in this format it is read back by the same parser,
// pricing and viewer as a Claude Code session.
//
// Lines are appended and chained by parentUuid as Claude Code chains them. The
// system prompt is a meta user line, which is how Claude Code records text the
// person did not type.
type SyntheticTranscript struct {
	mu        sync.Mutex
	file      *os.File
	sessionID string
	cwd       string
	parent    string
}

// CreateSyntheticTranscript opens the transcript at path for appending,
// creating it and its directory.
func CreateSyntheticTranscript(path, sessionID, cwd string) (*SyntheticTranscript, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("synthetic transcript: session id is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("synthetic transcript: create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("synthetic transcript: open %s: %w", path, err)
	}
	return &SyntheticTranscript{file: file, sessionID: sessionID, cwd: cwd}, nil
}

// Path is the transcript file.
func (t *SyntheticTranscript) Path() string { return t.file.Name() }

// Start records the session's model and the tools offered to it.
func (t *SyntheticTranscript) Start(model string, tools []string) error {
	return t.write(syntheticLine{Type: "system", Subtype: "init", Model: model, Tools: tools})
}

// Append records one conversation message; usage is set on a model reply.
func (t *SyntheticTranscript) Append(model string, message api.Message, usage *api.Usage) error {
	content, err := syntheticContent(message)
	if err != nil {
		return err
	}
	line := syntheticLine{Type: "user", Message: &syntheticMessage{Role: string(MessageRoleUser), Content: content}}
	switch message.Role {
	case api.RoleSystem:
		line.IsMeta = true
	case api.RoleUser, api.RoleTool:
	case api.RoleAssistant:
		line.Type = "assistant"
		line.Message = &syntheticMessage{
			ID: "msg_" + uuid.NewString(), Model: model, Role: string(MessageRoleAssistant),
			Content: content, Usage: syntheticUsage(usage),
		}
	default:
		return fmt.Errorf("synthetic transcript: unsupported message role %q", message.Role)
	}
	return t.write(line)
}

// Fail records the error that ended the run, as Claude Code records an API
// error: an assistant line carrying it.
func (t *SyntheticTranscript) Fail(model, message string) error {
	return t.write(syntheticLine{
		Type:    "assistant",
		Message: &syntheticMessage{ID: "msg_" + uuid.NewString(), Model: model, Role: string(MessageRoleAssistant), Content: []ContentBlock{}},
		Error:   message,
	})
}

func (t *SyntheticTranscript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

func (t *SyntheticTranscript) write(line syntheticLine) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return errors.New("synthetic transcript: write after close")
	}
	line.UUID = uuid.NewString()
	line.ParentUUID = t.parent
	line.SessionID = t.sessionID
	line.CWD = t.cwd
	line.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("synthetic transcript: encode %s line: %w", line.Type, err)
	}
	if _, err := t.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("synthetic transcript: write %s: %w", t.file.Name(), err)
	}
	t.parent = line.UUID
	return nil
}

type syntheticLine struct {
	Type       string            `json:"type"`
	Subtype    string            `json:"subtype,omitempty"`
	UUID       string            `json:"uuid"`
	ParentUUID string            `json:"parentUuid,omitempty"`
	SessionID  string            `json:"sessionId"`
	Timestamp  string            `json:"timestamp"`
	CWD        string            `json:"cwd,omitempty"`
	IsMeta     bool              `json:"isMeta,omitempty"`
	Model      string            `json:"model,omitempty"`
	Tools      []string          `json:"tools,omitempty"`
	Message    *syntheticMessage `json:"message,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type syntheticMessage struct {
	ID      string         `json:"id,omitempty"`
	Model   string         `json:"model,omitempty"`
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// syntheticUsage maps Captain's disjoint usage onto Anthropic's: reasoning is
// billed as output, and input excludes cached tokens on both sides.
func syntheticUsage(usage *api.Usage) *Usage {
	if usage == nil {
		return nil
	}
	return &Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens + usage.ReasoningTokens,
		CacheReadInputTokens:     usage.CacheReadTokens,
		CacheCreationInputTokens: usage.CacheWriteTokens,
	}
}

func syntheticContent(message api.Message) ([]ContentBlock, error) {
	blocks := make([]ContentBlock, 0, len(message.Parts))
	for i, part := range message.Parts {
		block, err := syntheticBlock(part)
		if err != nil {
			return nil, fmt.Errorf("synthetic transcript: %s message part %d: %w", message.Role, i+1, err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func syntheticBlock(part api.Part) (ContentBlock, error) {
	switch part.Type {
	case api.PartText:
		return ContentBlock{Type: ContentTypeText, Text: part.Text}, nil
	case api.PartReasoning:
		return ContentBlock{Type: ContentTypeThinking, Thinking: part.Text}, nil
	case api.PartAttachment:
		return ContentBlock{Type: ContentTypeText, Text: fmt.Sprintf("[%s attachment %s]", part.Attachment.MediaType, part.Attachment.ID)}, nil
	case api.PartToolRequest:
		input := part.ToolRequest.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return ContentBlock{Type: ContentTypeToolUse, ID: part.ToolRequest.ToolCallID, Name: part.ToolRequest.Name, Input: input}, nil
	case api.PartToolResult:
		return syntheticToolResult(part.ToolResult)
	default:
		return ContentBlock{}, fmt.Errorf("unsupported part type %q", part.Type)
	}
}

// syntheticToolResult writes a tool's output as the text the model read: a JSON
// string output as its string, anything else as its JSON.
func syntheticToolResult(result *api.ToolResult) (ContentBlock, error) {
	text := result.Error
	if result.Error == "" {
		text = string(result.Output)
		var unquoted string
		if json.Unmarshal(result.Output, &unquoted) == nil {
			text = unquoted
		}
	}
	content, err := json.Marshal(text)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("encode tool %s result: %w", result.ToolCallID, err)
	}
	return ContentBlock{Type: ContentTypeToolResult, ToolUseID: result.ToolCallID, Content: content, IsError: result.Error != ""}, nil
}

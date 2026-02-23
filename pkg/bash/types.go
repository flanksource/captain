package bash

import "encoding/json"

type OperationType string

const (
	OpCreate OperationType = "create"
	OpModify OperationType = "modify"
	OpDelete OperationType = "delete"
)

type FileOperation struct {
	Path      string
	Operation OperationType
	Command   string
	Line      int
	HasGlob   bool
	HasVar    bool
}

type AnalysisResult struct {
	Operations      []FileOperation
	Commands        []string // all command names invoked
	ReferencedPaths []string // all paths referenced (cd targets, absolute paths)
}

// Violation represents a detected safety issue in the bash command
type Violation struct {
	Message        string `json:"message"`
	Command        string `json:"command"`
	Path           string `json:"path,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

// ScanResult contains the complete analysis of a bash command
type ScanResult struct {
	Allowed        bool            `json:"allowed"`
	Reason         string          `json:"reason,omitempty"`
	Violations     []Violation     `json:"violations,omitempty"`
	Operations     []FileOperation `json:"operations,omitempty"`
	SafeOperations []string        `json:"safe_operations,omitempty"`
	ParseError     string          `json:"parse_error,omitempty"`
}

// Config represents the scanner configuration from YAML file
type Config struct {
	SafePaths           []string `yaml:"safe_paths" json:"safe_paths,omitempty"`
	WhitelistedCommands []string `yaml:"whitelisted_commands" json:"whitelisted_commands,omitempty"`
}

// PathClassification represents the safety classification of a file path
type PathClassification struct {
	Path   string
	IsSafe bool
	Reason string
}

// HookInput represents the JSON structure received from Claude Code hooks
type HookInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput     json.RawMessage `json:"tool_output,omitempty"`
}

// HookOutput represents the JSON structure returned to Claude Code hooks
type HookOutput struct {
	Continue           bool                `json:"continue"`
	StopReason         string              `json:"stopReason,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput contains permission-related hook results
type HookSpecificOutput struct {
	PermissionDecision string `json:"permissionDecision,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// BashToolInput represents the input for Bash tool
type BashToolInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

func (r *AnalysisResult) Created() []FileOperation {
	return r.filterByOp(OpCreate)
}

func (r *AnalysisResult) Modified() []FileOperation {
	return r.filterByOp(OpModify)
}

func (r *AnalysisResult) Deleted() []FileOperation {
	return r.filterByOp(OpDelete)
}

func (r *AnalysisResult) Paths() []string {
	seen := make(map[string]bool)
	var paths []string
	for _, op := range r.Operations {
		if !seen[op.Path] {
			seen[op.Path] = true
			paths = append(paths, op.Path)
		}
	}
	return paths
}

func (r *AnalysisResult) filterByOp(op OperationType) []FileOperation {
	var result []FileOperation
	for _, o := range r.Operations {
		if o.Operation == op {
			result = append(result, o)
		}
	}
	return result
}

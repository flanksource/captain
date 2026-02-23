package claude

// Bash represents a Bash tool invocation
type Bash struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
}

// Read represents a Read tool invocation
type Read struct {
	FilePath string  `json:"file_path"`
	Limit    float64 `json:"limit,omitempty"`
	Offset   float64 `json:"offset,omitempty"`
}

// Write represents a Write tool invocation
type Write struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content,omitempty"`
}

// Edit represents an Edit tool invocation
type Edit struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Grep represents a Grep tool invocation
type Grep struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
	Glob       string `json:"glob,omitempty"`
}

// Glob represents a Glob tool invocation
type Glob struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// Task represents a Task tool invocation
type Task struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt,omitempty"`
	SubAgentType string `json:"subagent_type,omitempty"`
}

// WebFetch represents a WebFetch tool invocation
type WebFetch struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt,omitempty"`
}

// WebSearch represents a WebSearch tool invocation
type WebSearch struct {
	Query string `json:"query"`
}

// TodoWrite represents a TodoWrite tool invocation
type TodoWrite struct {
	Todos []Todo `json:"todos"`
}

// Todo represents a single todo item
type Todo struct {
	ActiveForm string `json:"activeForm,omitempty"`
	Content    string `json:"content,omitempty"`
	Status     string `json:"status,omitempty"`
}

// AskUserQuestion represents question data
type Questions struct {
	Questions []Question `json:"questions"`
}

// Question represents a single question
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

// Option represents a question option
type Option struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

// ExitPlanMode represents an ExitPlanMode tool invocation
type ExitPlanMode struct {
	Plan           string          `json:"plan,omitempty"`
	AllowedPrompts []AllowedPrompt `json:"allowedPrompts,omitempty"`
}

// AllowedPrompt represents a prompt-based permission
type AllowedPrompt struct {
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

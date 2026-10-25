package api

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/shell"

	"github.com/flanksource/captain/pkg/credproxy"
)

// DefaultInlineToolTimeout bounds one attempt of an inline tool that declares
// no timeout of its own.
const DefaultInlineToolTimeout = 10 * time.Minute

// InlineTools are caller tools a prompt declares in its frontmatter, keyed by
// tool name. Each runs a script through the commons-db shell seam with the tool
// input as JSON on stdin.
type InlineTools map[string]InlineTool

// InlineTool is one frontmatter-declared tool. The embedded shell.Setup supplies
// the script's cwd, dotenv files, env vars and connections; checkout is refused
// because relocating the workspace belongs to the run-level setup.
type InlineTool struct {
	shell.Setup `json:",inline" yaml:",inline"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Input and Output are Picoschema or JSON Schema. Output, when set, is the
	// schema the script's stdout must satisfy as JSON.
	Input  any `json:"input,omitempty" yaml:"input,omitempty"`
	Output any `json:"output,omitempty" yaml:"output,omitempty"`
	// Exec is the script: a shebang selects the interpreter, otherwise the
	// default shell runs it with -c.
	Exec        string     `json:"exec" yaml:"exec"`
	ReadOnly    bool       `json:"readonly,omitempty" yaml:"readonly,omitempty"`
	Destructive bool       `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	Permission  ToolPolicy `json:"permission,omitempty" yaml:"permission,omitempty"`
	// Timeout bounds each attempt; empty means DefaultInlineToolTimeout.
	Timeout string     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Retry   *ToolRetry `json:"retry,omitempty" yaml:"retry,omitempty"`
	// Placement is where the script runs relative to the run's sandbox; empty
	// means the sandbox's default: host locally, supervisor under git-agent.
	Placement ToolPlacement `json:"placement,omitempty" yaml:"placement,omitempty" jsonschema:"enum=host,enum=sandbox,enum=supervisor,enum=sidecar"`
	// Credentials is how the tool's connections and secret env vars reach the
	// script; empty derives it (see ResolveCredentials).
	Credentials ToolCredentials `json:"credentials,omitempty" yaml:"credentials,omitempty" jsonschema:"enum=none,enum=direct,enum=proxied,enum=sidecar,enum=sidecar-proxied"`
	// Grants are the HTTP(S) destinations a proxied tool may call. The script
	// sees a placeholder per granted header and the proxy substitutes the real
	// value on the way out, only in that header, only for that destination.
	Grants []credproxy.Grant `json:"grants,omitempty" yaml:"grants,omitempty"`
}

// ToolRetry re-runs a failed attempt with exponential backoff. An attempt fails
// when it times out or exits non-zero; ExitCodes narrows the exits retried.
type ToolRetry struct {
	// Attempts counts every run, the first included.
	Attempts  int     `json:"attempts,omitempty" yaml:"attempts,omitempty"`
	Delay     string  `json:"delay,omitempty" yaml:"delay,omitempty"`
	MaxDelay  string  `json:"maxDelay,omitempty" yaml:"maxDelay,omitempty"`
	Backoff   float64 `json:"backoff,omitempty" yaml:"backoff,omitempty"`
	ExitCodes []int   `json:"exitCodes,omitempty" yaml:"exitCodes,omitempty"`
}

// RetryPlan is a ToolRetry with its durations parsed and defaults applied.
type RetryPlan struct {
	Attempts  int
	Delay     time.Duration
	MaxDelay  time.Duration
	Backoff   float64
	ExitCodes []int
}

// Names returns the tool names in sorted order.
func (t InlineTools) Names() []string {
	names := make([]string, 0, len(t))
	for name := range t {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t InlineTools) Validate() error {
	for _, name := range t.Names() {
		if err := t[name].validate(name); err != nil {
			return fmt.Errorf("tools.%s: %w", name, err)
		}
	}
	return nil
}

func (t InlineTool) validate(name string) error {
	if !ValidToolName(name) {
		return fmt.Errorf("tool name may contain only letters, digits, '-' and '_'")
	}
	if strings.TrimSpace(t.Exec) == "" {
		return fmt.Errorf("exec is required")
	}
	if t.ReadOnly && t.Destructive {
		return fmt.Errorf("readonly and destructive are mutually exclusive")
	}
	if t.Permission != "" && !t.Permission.Valid() {
		return fmt.Errorf("invalid permission %q", t.Permission)
	}
	if t.Checkout != nil {
		return fmt.Errorf("checkout is not supported per tool; declare it in the run's setup")
	}
	if err := t.validateEnvironment(); err != nil {
		return err
	}
	if _, err := t.ParseTimeout(); err != nil {
		return err
	}
	_, err := t.Retry.Plan()
	return err
}

// Hints projects the declared booleans onto the MCP hint pair: declaring one answers the other, and declaring neither claims
// nothing.
func (t InlineTool) Hints() (readOnly, destructive *bool) {
	if !t.ReadOnly && !t.Destructive {
		return nil, nil
	}
	return &t.ReadOnly, &t.Destructive
}

func (t InlineTool) ParseTimeout() (time.Duration, error) {
	return parsePositiveDuration("timeout", t.Timeout, DefaultInlineToolTimeout)
}

// Plan parses the retry policy; a nil retry is a single attempt.
func (r *ToolRetry) Plan() (RetryPlan, error) {
	if r == nil {
		return RetryPlan{Attempts: 1}, nil
	}
	if r.Attempts < 1 {
		return RetryPlan{}, fmt.Errorf("retry.attempts must be at least 1, got %d", r.Attempts)
	}
	if r.Backoff != 0 && r.Backoff < 1 {
		return RetryPlan{}, fmt.Errorf("retry.backoff must be at least 1, got %v", r.Backoff)
	}
	delay, err := parsePositiveDuration("retry.delay", r.Delay, time.Second)
	if err != nil {
		return RetryPlan{}, err
	}
	maxDelay, err := parsePositiveDuration("retry.maxDelay", r.MaxDelay, 30*time.Second)
	if err != nil {
		return RetryPlan{}, err
	}
	if maxDelay < delay {
		return RetryPlan{}, fmt.Errorf("retry.maxDelay %s is shorter than retry.delay %s", maxDelay, delay)
	}
	backoff := r.Backoff
	if backoff == 0 {
		backoff = 2
	}
	return RetryPlan{Attempts: r.Attempts, Delay: delay, MaxDelay: maxDelay, Backoff: backoff,
		ExitCodes: append([]int(nil), r.ExitCodes...)}, nil
}

func parsePositiveDuration(field, value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("invalid %s %q (must be > 0)", field, value)
	}
	return parsed, nil
}

// ValidToolName reports whether name is usable as a caller tool name across
// every provider: letters, digits, '-' and '_' only.
func ValidToolName(name string) bool {
	if name == "" {
		return false
	}
	for _, value := range name {
		if value >= 'a' && value <= 'z' ||
			value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' ||
			value == '-' ||
			value == '_' {
			continue
		}
		return false
	}
	return true
}

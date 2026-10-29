package prompt

import "fmt"

// HookParseError identifies frontmatter that cannot be parsed during discovery.
type HookParseError struct {
	Prompt string
	Err    error
}

func (e *HookParseError) Error() string {
	return fmt.Sprintf("parse prompt %s frontmatter: %v", e.Prompt, e.Err)
}

func (e *HookParseError) Unwrap() error { return e.Err }

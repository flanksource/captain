package aiflags

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
)

// ErrUnconfigured marks the "nothing chose a model or a mode" failure so callers
// can recognise it without matching on message text.
var ErrUnconfigured = errors.New("no model configured")

// UnconfiguredError reports that a run reached the execution boundary without a
// model or a mechanism, and names the commands that fix it.
//
// The shared request resolver decides whether a missing mode is a migration
// warning or a strict error. A missing model remains an execution error.
type UnconfiguredError struct {
	// Field is "model" or "mode": which half is missing.
	Field string
	// Provider is the family the caller named, when it is known. A missing model
	// often means no family is known either, in which case it is nil.
	Provider *registry.Provider
	// Model is whatever the caller did supply, for context in the message.
	Model string
}

func (e *UnconfiguredError) Unwrap() error { return ErrUnconfigured }

func (e *UnconfiguredError) Error() string {
	var b strings.Builder
	if e.Field == "mode" && e.Provider != nil {
		// AgentName, not Name: users speak in families ("gemini models"), not
		// provider keys ("google models").
		fmt.Fprintf(&b, "no runtime mode configured for %s models", e.Provider.AgentName)
	} else if e.Field == "mode" {
		b.WriteString("no runtime mode configured")
	} else {
		b.WriteString("no model configured")
	}
	if e.Model != "" {
		fmt.Fprintf(&b, " (selecting %q)", e.Model)
	}
	b.WriteString("\n  set one with either:")
	b.WriteString("\n    captain configure")
	if e.Provider != nil {
		fmt.Fprintf(&b, " %s", e.Provider.Name)
	}
	b.WriteString("\n    gavel configure                 (per-repo, writes .gavel.yaml)")
	b.WriteString("\n  or name one inline, e.g. --model agent:claude-sonnet-5")
	if e.Field == "mode" && e.Provider != nil {
		fmt.Fprintf(&b, "\n  modes available for %s: %s", e.Provider.AgentName, modeList(e.Provider.Modes()))
	}
	return b.String()
}

func modeList(modes []registry.RuntimeMode) string {
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}

// IsUnconfigured reports whether err is the "nothing configured a model" failure.
func IsUnconfigured(err error) bool { return errors.Is(err, ErrUnconfigured) }

func (c UnconfiguredCandidate) Error() string {
	return c.Path + ": " + (&UnconfiguredError{Field: "mode", Model: c.Model, Provider: c.Provider}).Error()
}

func (c UnconfiguredCandidate) Unwrap() error { return ErrUnconfigured }

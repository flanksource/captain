package api

import (
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/commons/merge"
)

// MergePolicy is the structural-merge policy for a Spec: Model's policy plus the
// spec's own indivisible values.
//
// Pointers to scalars are replaced rather than merged through, so an explicit
// zero (a *bool false, a *int 0) counts as set instead of being read as unset.
// Tool-approval resume state is indivisible: it is a snapshot of one suspended
// turn, and half of one snapshot layered over half of another describes no turn
// that ever happened. SandboxRef replacement is handled by Spec.Merge: equal
// modes merge their provider-neutral policy; changing modes replaces the whole
// boundary so settings cannot leak between isolation mechanisms.
func MergePolicy() merge.Policy {
	return registry.MergePolicy().With(merge.Policy{
		Replace: []any{
			(*bool)(nil),
			(*int)(nil),
			(*int64)(nil),
			(*uint)(nil),
			(*string)(nil),
			(*ToolApprovalResume)(nil),
			(*SandboxRef)(nil),
		},
	})
}

// Merge returns a copy of s with override's set (non-zero) fields taking
// precedence. A zero-valued field in override is treated as "unset" and keeps
// s's value, so a base spec can supply defaults that an operation-specific spec
// selectively overrides:
//
//	resolved := base.Merge(operation)
//
// Merging is structural (see pkg/merge): scalars take the override when set,
// slices are replaced wholesale when the override's is non-empty, maps merge
// key-wise, and structs — including Setup, Workflow and Permissions.Tools behind
// their pointers — merge field by field, so setting one sub-field does not erase
// its siblings. Boolean toggles follow zero=unset: an override can turn a flag on
// but not off, since false is indistinguishable from absent.
//
// Neither operand is mutated and the result shares no mutable memory with
// either, so a merged spec can be edited without reaching back into the config
// it inherited from.
func (s Spec) Merge(override Spec) Spec {
	merged := merge.Apply(s, override, MergePolicy())
	if s.Sandbox != nil && override.Sandbox != nil && s.Sandbox.Mode == override.Sandbox.Mode {
		resolved := merge.Apply(*s.Sandbox, *override.Sandbox, MergePolicy())
		merged.Sandbox = &resolved
	}
	return merged
}

// WithoutSession returns s stripped of everything that binds it to a prior
// conversation: the session to resume, pending tool-approval resume state, and
// canonical message history.
//
// Model, budget, permissions and setup are deliberately kept — a derived run
// inherits how to run, never what was already said. Use it wherever one run's
// spec seeds another that must reach its own conclusion: a verifier grading the
// work, a follow-up run continuing from an approved plan. Without it the derived
// run resumes into the session it was meant to judge or supersede.
func (s Spec) WithoutSession() Spec {
	s.SessionID = ""
	s.ToolApproval = nil
	s.Messages = nil
	return s
}

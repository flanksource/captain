package api

import (
	"reflect"
	"sort"
	"strings"

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

// Merge returns a copy of s with override's supplied fields taking precedence.
// A zero-valued field without explicit presence is "unset" and keeps
// s's value, so a base spec can supply defaults that an operation-specific spec
// selectively overrides:
//
//	resolved := base.Merge(operation)
//
// Merging is structural (see pkg/merge): scalars take the override when set,
// slices are replaced wholesale when the override's is non-empty, maps merge
// key-wise, and structs — including Setup, Workflow and Permissions.Tools behind
// their pointers — merge field by field, so setting one sub-field does not erase
// its siblings. Decoded explicit zero values, or fields marked by WithExplicit,
// replace inherited values, including false booleans and empty collections.
//
// Neither operand is mutated and the result shares no mutable memory with
// either, so a merged spec can be edited without reaching back into the config
// it inherited from.
func (s Spec) Merge(override Spec) Spec {
	s = s.withoutReplacedPresence(override)
	merged := merge.Apply(s, override, MergePolicy())
	if s.Sandbox != nil && override.Sandbox != nil && s.Sandbox.Mode == override.Sandbox.Mode {
		resolved := merge.Apply(*s.Sandbox, *override.Sandbox, MergePolicy())
		merged.Sandbox = &resolved
	}
	cloned := merge.Apply(Spec{}, override, MergePolicy())
	paths := make([]string, 0, len(override.explicitFields()))
	for path := range override.explicitFields() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		tokens := strings.Split(strings.TrimPrefix(path, "/"), "/")
		source := serializedField(reflect.ValueOf(cloned), tokens)
		target := serializedField(reflect.ValueOf(&merged).Elem(), tokens)
		if source.IsValid() && target.IsValid() && target.CanSet() {
			target.Set(source)
		}
	}
	return merged
}

func (s Spec) withoutReplacedPresence(override Spec) Spec {
	s.Explicit = s.Explicit.Clone()
	s.Model.Explicit = s.Model.Explicit.Clone()
	for path := range override.Fields() {
		value := serializedField(reflect.ValueOf(override), strings.Split(strings.TrimPrefix(path, "/"), "/"))
		if !replacesField(value) && path != "/toolApproval" && (path != "/sandbox" || s.Sandbox == nil || override.Sandbox == nil || s.Sandbox.Mode == override.Sandbox.Mode) {
			continue
		}
		for _, fields := range []FieldPresence{s.Explicit, s.Model.Explicit} {
			for previous := range fields {
				if previous == path || strings.HasPrefix(previous, path+"/") {
					delete(fields, previous)
				}
			}
		}
	}
	return s
}

func replacesField(value reflect.Value) bool {
	return value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Map && value.Len() == 0 || value.Kind() == reflect.Pointer && value.IsNil())
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
	s.Explicit = s.Explicit.Clone()
	for path := range s.Explicit {
		for _, removed := range []string{"/sessionId", "/toolApproval", "/messages"} {
			if path == removed || strings.HasPrefix(path, removed+"/") {
				delete(s.Explicit, path)
			}
		}
	}
	return s
}

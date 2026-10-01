package runtimeprofiles

import "fmt"

// OwnedLayersError marks invalid stored profile or preset data,
// distinct from an absent or ambiguous top-level selection supplied by a caller.
type OwnedLayersError struct {
	Kind Kind
	Ref  string
	Err  error
}

func (e *OwnedLayersError) Error() string {
	kind := e.Kind
	if kind == "" {
		kind = KindProfile
	}
	return fmt.Sprintf("runtime %s %q configuration: %v", kind, e.Ref, e.Err)
}

func (e *OwnedLayersError) Unwrap() error { return e.Err }

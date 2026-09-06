package runtimeprofiles

import "fmt"

// OwnedLayersError marks invalid stored profile data or its referenced presets,
// distinct from an absent or ambiguous top-level selection supplied by a caller.
type OwnedLayersError struct {
	Ref string
	Err error
}

func (e *OwnedLayersError) Error() string {
	return fmt.Sprintf("runtime profile %q configuration: %v", e.Ref, e.Err)
}

func (e *OwnedLayersError) Unwrap() error { return e.Err }

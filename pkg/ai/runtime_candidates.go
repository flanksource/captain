package ai

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// ResolveCandidates returns the exact ordered models NewProvider will try,
// including disabled-model filtering and the catalog's enabled replacement.
// It performs no provider construction or credential lookup.
func ResolveCandidates(model api.Model) ([]api.Model, error) {
	resolved, err := Resolve(model)
	if err != nil {
		return nil, err
	}
	candidates := resolved.Candidates()
	for i := range candidates {
		candidate, err := Resolve(candidates[i])
		if err != nil {
			return nil, fmt.Errorf("candidate[%d] %q: %w", i, candidates[i].Name, err)
		}
		candidates[i] = candidate
	}
	return candidates, nil
}

package cli

import (
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/gomega"
)

// withCaps fills a hand-built model's capability flags.
//
// It fails the spec rather than returning the model untouched. The version this
// replaces was silent, so a model naming a provider×mode cell that does not
// exist came back looking enriched with every capability false — a test could
// then pass while asserting against an adapter that cannot run.
func withCaps(model api.Model) api.Model {
	resolved, err := api.ResolveModel(model)
	Expect(err).NotTo(HaveOccurred())
	return resolved
}

// resolveAll is withCaps over a runtime list, for the surfaces that take one.
func resolveAll(models ...api.Model) []api.Model {
	out := make([]api.Model, len(models))
	for i, model := range models {
		out[i] = withCaps(model)
	}
	return out
}

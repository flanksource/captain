package aichat_test

import (
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/gomega"
)

// withCaps fills a hand-built model's provider and capability flags.
//
// It fails the spec rather than returning the model untouched. The version this
// replaces was silent, so a model naming a provider×mode cell that does not
// exist came back looking enriched with every capability false — a test could
// then pass while asserting against an adapter that cannot run.
func withCaps(model api.Model) api.Model {
	enriched, err := model.WithCapabilities()
	Expect(err).NotTo(HaveOccurred())
	return enriched
}

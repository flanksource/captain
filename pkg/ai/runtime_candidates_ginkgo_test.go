package ai

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runtime candidates", func() {
	DescribeTable("keep the authored agent runtime and exact model",
		func(model api.Model, name string) {
			candidates, err := ResolveCandidates(model)
			Expect(err).NotTo(HaveOccurred())
			Expect(candidates).To(HaveExactElements(SatisfyAll(
				HaveField("Name", name),
				HaveField("Provider", api.Anthropic),
				HaveField("Mode", api.ModeAgent),
			)))
		},
		Entry("selector with exact id", api.Model{Name: "agent:claude-sonnet-5"}, "claude-sonnet-5"),
		Entry("frontmatter model and mode", api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, "claude-sonnet-5"),
		Entry("selector with alias", api.Model{Name: "agent:sonnet"}, "claude-sonnet-5-5"),
	)
})

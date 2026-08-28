package registry

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("canonical model runtime backend", func() {
	DescribeTable("accepts only authored backend values",
		func(value string, valid bool) {
			_, ok := ParseRuntimeMode(value)
			Expect(ok).To(Equal(valid))
		},
		Entry("api", "api", true),
		Entry("agent", "agent", true),
		Entry("cli", "cli", true),
		Entry("cmux", "cmux", true),
		Entry("old sdk alias", "sdk", false),
		Entry("provider", "anthropic", false),
		Entry("composite adapter", "claude-agent", false),
	)

	It("lets the compact model prefix take precedence over the backend field", func() {
		resolved, err := ResolveModel(Model{Name: "agent:opus:high", Mode: ModeAPI})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Name).To(Equal("claude-opus-5"))
		Expect(resolved.Mode).To(Equal(ModeAgent))
		Expect(resolved.Backend).To(Equal(BackendClaudeAgent))
		Expect(resolved.Effort).To(Equal(EffortHigh))
	})

	It("serializes the authored backend without exposing the resolved adapter", func() {
		resolved, err := ResolveModel(Model{Name: "agent:opus"})
		Expect(err).NotTo(HaveOccurred())

		payload, err := json.Marshal(resolved)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(payload)).To(ContainSubstring(`"backend":"agent"`))
		Expect(string(payload)).NotTo(ContainSubstring(`"mode"`))
		Expect(string(payload)).NotTo(ContainSubstring("claude-agent"))
	})

	DescribeTable("rejects legacy authored values as invalid model configuration",
		func(payload string, invalidValue string) {
			var model Model
			Expect(json.Unmarshal([]byte(payload), &model)).To(Succeed())

			_, err := ResolveModel(model)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid model configuration"))
			Expect(err.Error()).To(ContainSubstring(invalidValue))
			Expect(err.Error()).To(ContainSubstring("api, agent, cli, cmux"))
		},
		Entry("provider backend", `{"model":"opus","backend":"anthropic"}`, "anthropic"),
		Entry("composite backend", `{"model":"opus","backend":"claude-agent"}`, "claude-agent"),
	)

	It("rejects legacy compact prefixes instead of translating them", func() {
		_, err := ResolveModel(Model{Name: "claude-agent:opus"})

		Expect(err).To(HaveOccurred())
		Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid model configuration"))
	})
})

package registry

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A runtime is (model, mode). This suite pins the mode half of the wire: the
// only values it accepts, the only key it travels under, and the fact that the
// deleted composite vocabulary is rejected rather than translated.
var _ = Describe("canonical model runtime mode", func() {
	DescribeTable("accepts only the four mechanisms",
		func(value string, valid bool) {
			_, ok := ParseRuntimeMode(value)
			Expect(ok).To(Equal(valid))
		},
		Entry("api", "api", true),
		Entry("agent", "agent", true),
		Entry("cli", "cli", true),
		Entry("cmux", "cmux", true),
		Entry("old sdk alias", "sdk", false),
		Entry("provider name", "anthropic", false),
		Entry("composite adapter id", "claude-agent", false),
	)

	It("lets the compact model prefix take precedence over the mode field", func() {
		resolved, err := ResolveModel(Model{Name: "agent:opus:high", Mode: ModeAPI})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Name).To(Equal("claude-opus-5"))
		Expect(resolved.Mode).To(Equal(ModeAgent))
		Expect(resolved.Provider).To(Equal(Anthropic))
		Expect(resolved.Effort).To(Equal(EffortHigh))
	})

	It("serializes the mode under `mode`, with no trace of an adapter id", func() {
		resolved, err := ResolveModel(Model{Name: "agent:opus"})
		Expect(err).NotTo(HaveOccurred())

		payload, err := json.Marshal(resolved)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(payload)).To(ContainSubstring(`"mode":"agent"`))
		Expect(string(payload)).NotTo(ContainSubstring(`"backend"`))
		Expect(string(payload)).NotTo(ContainSubstring("claude-agent"))
	})

	// The provider is a resolution result, not part of the wire form: a client
	// that reads a model back and posts it must name only what it selected.
	It("keeps the provider off the wire", func() {
		resolved, err := ResolveModel(Model{Name: "agent:opus"})
		Expect(err).NotTo(HaveOccurred())

		payload, err := json.Marshal(resolved)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(payload)).NotTo(ContainSubstring(`"provider"`))
		Expect(string(payload)).NotTo(ContainSubstring("anthropic"))
	})

	DescribeTable("rejects a composite id where a mode belongs",
		func(mode string) {
			_, err := ResolveModel(Model{Name: "opus", Mode: RuntimeMode(mode)})

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid model configuration"))
			Expect(err.Error()).To(ContainSubstring(mode))
			Expect(err.Error()).To(ContainSubstring("api, agent, cli, cmux"))
		},
		Entry("provider name", "anthropic"),
		Entry("composite adapter id", "claude-agent"),
	)

	// A pre-rename client posts `backend`. encoding/json ignores the unknown key,
	// so the model decodes with no mode — which must land on the default rather
	// than on whatever the stale key said.
	It("ignores a legacy backend key instead of honouring it", func() {
		var model Model
		Expect(json.Unmarshal([]byte(`{"model":"opus","backend":"cli"}`), &model)).To(Succeed())
		Expect(model.Mode).To(BeEmpty())

		resolved, err := ResolveModel(model)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Mode).To(Equal(Anthropic.DefaultMode))
	})

	It("rejects legacy compact prefixes instead of translating them", func() {
		_, err := ResolveModel(Model{Name: "claude-agent:opus"})

		Expect(err).To(HaveOccurred())
		Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid model configuration"))
	})
})

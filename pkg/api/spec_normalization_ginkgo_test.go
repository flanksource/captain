package api

import (
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Spec runtime context normalization", func() {
	It("attributes fallback inheritance to a primary field derived by runtime context", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "agent:sonnet,agent:haiku"}
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Normalize: func(spec Spec) (SpecNormalization, error) {
			spec.Temperature = floatPtr(0.2)
			return SpecNormalization{Spec: spec, Fields: FieldPresence{"/temperature": true}, Source: FieldSource{Kind: FieldSourceContext, Name: "runtime context"}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Fallbacks[0].Temperature).To(Equal(floatPtr(0.2)))
		Expect(resolved.Provenance["/fallbacks/0/temperature"].Source).To(Equal(FieldSource{Kind: FieldSourceContext, Name: "runtime context", Key: "/temperature"}))
	})

	It("normalizes saved primary and fallback selections once before provider mode defaults", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "api:sonnet,api:sol"}
		calls := 0
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, RequireModel: true, Normalize: func(spec Spec) (SpecNormalization, error) {
			calls++
			Expect(spec.Name).To(Equal("sonnet"))
			Expect(spec.Fallbacks).To(HaveLen(1))
			Expect(spec.Mode).To(BeEmpty())
			Expect(spec.Fallbacks[0].Mode).To(BeEmpty())
			spec.Mode, spec.Fallbacks[0].Mode = ModeCLI, ModeCLI
			return SpecNormalization{Spec: spec, Fields: FieldPresence{"/mode": true, "/fallbacks/0/mode": true}, Source: FieldSource{Kind: FieldSourceContext, Name: "runtime context"}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		Expect(resolved.Spec.Mode).To(Equal(ModeCLI))
		Expect(resolved.Spec.Fallbacks[0].Mode).To(Equal(ModeCLI))
		Expect(resolved.Provenance["/fallbacks/0/model"].Source.Key).To(Equal("ai.defaultModel"))
		Expect(resolved.Provenance["/fallbacks/0/mode"].Source.Kind).To(Equal(FieldSourceContext))
		Expect(resolved.Trace).To(BeEmpty())
	})

	It("preserves higher authored effort over a lower compact selector and its field ownership", func() {
		layers := []SpecLayer{PromptSpecLayer("project", Spec{Model: Model{Name: "agent:sol:high"}}), RequestSpecLayer("request", Spec{Model: Model{Effort: EffortLow}})}
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: layers, Saved: &captainconfig.AIDefaults{}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Effort).To(Equal(EffortLow))
		Expect(resolved.Provenance["/effort"].Source.Name).To(Equal("request"))
		Expect(resolved.Provenance["/effort"].Source.Key).To(Equal("/effort"))
		Expect(resolved.Trace).To(Equal(layers))
	})

	It("expands authored CSV candidates before applying runtime context", func() {
		layer := RequestSpecLayer("request", Spec{Model: Model{Name: "sonnet,sol", Fallbacks: []Model{{Name: "haiku"}}}})
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}, Normalize: func(spec Spec) (SpecNormalization, error) {
			Expect(spec.Name).To(Equal("sonnet"))
			Expect(spec.Fallbacks).To(HaveLen(2))
			spec.Mode = ModeCLI
			for i := range spec.Fallbacks {
				spec.Fallbacks[i].Mode = ModeCLI
			}
			return SpecNormalization{Spec: spec, Fields: FieldPresence{"/mode": true, "/fallbacks/0/mode": true, "/fallbacks/1/mode": true}, Source: FieldSource{Kind: FieldSourceContext, Name: "runtime context"}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Provenance["/fallbacks/0/model"].Source.Key).To(Equal("/model"))
		Expect(resolved.Provenance["/fallbacks/1/model"].Source.Key).To(Equal("/fallbacks/0/model"))
		Expect(resolved.Trace).To(Equal([]SpecLayer{layer}))
	})

	It("keeps named sandbox references structurally composable until context resolves them", func() {
		layer := RequestSpecLayer("request", Spec{Model: Model{Name: "sonnet", Mode: ModeCLI}, Sandbox: &SandboxRef{Backend: "review-pool"}})
		Expect(ValidateSpecLayers(layer)).To(Succeed())
		_, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).To(HaveOccurred())
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}, Normalize: func(spec Spec) (SpecNormalization, error) {
			spec.Sandbox.Mode = SandboxDocker
			return SpecNormalization{Spec: spec, Fields: FieldPresence{"/sandbox/mode": true}, Source: FieldSource{Kind: FieldSourceContext, Name: "runtime context"}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Sandbox).To(Equal(&SandboxRef{Mode: SandboxDocker, Backend: "review-pool"}))
		Expect(resolved.Trace).To(Equal([]SpecLayer{layer}))
	})

	It("normalizes the complete authored stack before saved gaps without replacing source ownership", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{"anthropic": {Mode: "api"}}}
		layer := PromptSpecLayer("project", Spec{Model: Model{Name: "sonnet"}, Setup: &shell.Setup{Cwd: ".", DotEnv: []string{".env"}, Env: []string{"EXAMPLE=value"}}})
		calls := 0
		result, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}, Saved: &saved,
			Normalize: func(spec Spec) (SpecNormalization, error) {
				calls++
				Expect(spec.Mode).To(BeEmpty())
				spec.Setup.Cwd = "/project"
				spec.Mode = ModeAgent
				return SpecNormalization{Spec: spec, Fields: FieldPresence{"/setup/cwd": true, "/mode": true}, Source: FieldSource{Kind: FieldSourceContext, Name: "runtime context"}}, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		Expect(result.Spec.Mode).To(Equal(ModeAgent))
		Expect(result.Spec.Setup.Cwd).To(Equal("/project"))
		Expect(result.Provenance["/setup/cwd"].Source.Name).To(Equal("project"))
		Expect(result.Provenance["/setup/cwd"].NormalizedBy.Name).To(Equal("runtime context"))
		Expect(result.Provenance["/setup/cwd"].NormalizedBy.Key).To(Equal("/setup/cwd"))
		Expect(result.Provenance["/setup/dotenv"].Source.Name).To(Equal("project"))
		Expect(result.Provenance["/setup/dotenv"].NormalizedBy).To(BeNil())
		Expect(result.Spec.Setup.Env).To(Equal(layer.Spec.Setup.Env))
		Expect(result.Trace).To(Equal([]SpecLayer{layer}))
		Expect(layer.Spec.Setup.Cwd).To(Equal("."))
	})
})

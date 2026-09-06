package cli

import (
	"os"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

func promptRequestForTest(options AIPromptOptions) (ai.Request, error) {
	spec, err := options.promptSpec()
	if err != nil {
		return ai.Request{}, err
	}
	spec.Prompt.User = options.Prompt
	resolved, err := runtimeProjectionForTest(options.AIRuntimeOptions, []api.SpecLayer{api.PromptSpecLayer("prompt input", spec)})
	return resolved.Request, err
}

func runtimeRequestForTest(options AIRuntimeOptions, prompt api.Prompt) (ai.Request, error) {
	resolved, err := runtimeProjectionForTest(options, []api.SpecLayer{api.PromptSpecLayer("prompt input", api.Spec{Prompt: prompt})})
	return resolved.Request, err
}

func providerConfigForTest(options AIProviderOptions) (ai.Config, error) {
	resolved, err := runtimeProjectionForTest(AIRuntimeOptions{AIProviderOptions: options}, nil)
	return resolved.Config, err
}

func runtimeLayersForTest(base api.Spec, options AIPromptOptions) (ai.Request, ai.Config, error) {
	resolved, err := runtimeProjectionForTest(options.AIRuntimeOptions, []api.SpecLayer{api.PromptSpecLayer("file", base)})
	return resolved.Request, resolved.Config, err
}

func runtimeProjectionForTest(options AIRuntimeOptions, layers []api.SpecLayer) (AIRuntimeResolved, error) {
	saved, err := loadSavedConfig()
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return AIRuntimeResolved{}, err
	}
	return options.Resolve(AIRuntimeResolveOptions{Layers: layers, Saved: saved, Cwd: cwd})
}

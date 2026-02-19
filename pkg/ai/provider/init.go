package provider

import "github.com/flanksource/captain/pkg/ai"

func init() {
	ai.RegisterProvider(ai.BackendClaudeCLI, func(model, _ string) ai.Provider {
		return NewClaudeCLI(model)
	})
	ai.RegisterProvider(ai.BackendCodexCLI, func(model, _ string) ai.Provider {
		return NewCodexCLI(model)
	})
	ai.RegisterProvider(ai.BackendGeminiCLI, func(model, _ string) ai.Provider {
		return NewGeminiCLI(model)
	})
	ai.RegisterProvider(ai.BackendAnthropic, func(model, apiKey string) ai.Provider {
		return NewAnthropic(model, apiKey)
	})
	ai.RegisterProvider(ai.BackendGemini, func(model, apiKey string) ai.Provider {
		return NewGemini(model, apiKey)
	})
}

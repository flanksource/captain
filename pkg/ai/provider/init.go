package provider

import "github.com/flanksource/captain/pkg/ai"

func init() {
	ai.RegisterProvider(ai.BackendClaudeCLI, func(cfg ai.Config) (ai.Provider, error) {
		return NewClaudeCLI(cfg.Model), nil
	})
	ai.RegisterProvider(ai.BackendCodexCLI, func(cfg ai.Config) (ai.Provider, error) {
		return NewCodexCLI(cfg.Model), nil
	})
	ai.RegisterProvider(ai.BackendGeminiCLI, func(cfg ai.Config) (ai.Provider, error) {
		return NewGeminiCLI(cfg.Model), nil
	})
	ai.RegisterProvider(ai.BackendAnthropic, func(cfg ai.Config) (ai.Provider, error) {
		return NewAnthropic(cfg), nil
	})
	ai.RegisterProvider(ai.BackendGemini, func(cfg ai.Config) (ai.Provider, error) {
		return NewGemini(cfg), nil
	})
	ai.RegisterProvider(ai.BackendOpenAI, func(cfg ai.Config) (ai.Provider, error) {
		return NewOpenAI(cfg), nil
	})
}

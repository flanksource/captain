package provider

import "github.com/flanksource/captain/pkg/ai"

func init() {
	ai.RegisterProvider(ai.BackendClaudeCLI, func(cfg ai.Config) ai.Provider {
		return NewClaudeCLI(cfg.Model)
	})
	ai.RegisterProvider(ai.BackendCodexCLI, func(cfg ai.Config) ai.Provider {
		return NewCodexCLI(cfg.Model)
	})
	ai.RegisterProvider(ai.BackendGeminiCLI, func(cfg ai.Config) ai.Provider {
		return NewGeminiCLI(cfg.Model)
	})
	ai.RegisterProvider(ai.BackendAnthropic, func(cfg ai.Config) ai.Provider {
		return NewAnthropic(cfg)
	})
	ai.RegisterProvider(ai.BackendGemini, func(cfg ai.Config) ai.Provider {
		return NewGemini(cfg)
	})
}

package cli

import (
	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("whoami disabled model visibility", func() {
	enabledAdapter := AdapterStatus{
		Backend:    string(ai.BackendCodexCLI),
		Type:       "cli",
		ModelCount: 3,
		Models:     []string{"current", "retired", "stable"},
		ModelDetails: []ai.ModelDef{
			{ID: "current"},
			{ID: "retired", Disabled: true},
			{ID: "stable"},
		},
	}
	disabledAdapter := AdapterStatus{
		Backend:    string(ai.BackendClaudeCmux),
		Type:       "cli",
		Disabled:   true,
		ModelCount: 1,
		Models:     []string{"disabled-backend-model"},
		ModelDetails: []ai.ModelDef{
			{ID: "disabled-backend-model", Disabled: true},
		},
	}

	It("filters disabled models by default without dropping their adapters", func() {
		filtered := filterWhoamiModels([]AdapterStatus{enabledAdapter, disabledAdapter}, false)

		Expect(filtered).To(Equal([]AdapterStatus{
			{
				Backend:    string(ai.BackendCodexCLI),
				Type:       "cli",
				ModelCount: 2,
				Models:     []string{"current", "stable"},
				ModelDetails: []ai.ModelDef{
					{ID: "current"},
					{ID: "stable"},
				},
			},
			{
				Backend:  string(ai.BackendClaudeCmux),
				Type:     "cli",
				Disabled: true,
			},
		}))
	})

	It("retains disabled models when explicitly requested", func() {
		adapters := []AdapterStatus{enabledAdapter, disabledAdapter}

		Expect(filterWhoamiModels(adapters, true)).To(Equal(adapters))
	})

	It("strikes included disabled models across rich formats and retains the plain marker", func() {
		pretty := (WhoamiResult{
			Adapters:    []AdapterStatus{enabledAdapter},
			showModels:  true,
			sampleLimit: 0,
		}).Pretty()

		Expect(pretty.ANSI()).To(ContainSubstring("\x1b[9m"))
		Expect(pretty.ANSI()).To(ContainSubstring("retired [disabled]"))
		Expect(pretty.Markdown()).To(ContainSubstring("~~retired [disabled]~~"))
		Expect(pretty.String()).To(ContainSubstring("retired [disabled]"))
	})

	It("applies the sample limit to the filtered model count", func() {
		filtered := filterWhoamiModels([]AdapterStatus{enabledAdapter}, false)
		pretty := (WhoamiResult{
			Adapters:    filtered,
			showModels:  true,
			sampleLimit: 1,
		}).Pretty().String()

		Expect(pretty).To(ContainSubstring("2 models"))
		Expect(pretty).To(ContainSubstring("- current"))
		Expect(pretty).To(ContainSubstring("... (+1 more)"))
		Expect(pretty).NotTo(ContainSubstring("retired"))
	})
})

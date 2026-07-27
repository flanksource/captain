package tools_test

import (
	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Tool preference resolution", func() {
	It("prefers an exact tool entry over its group", func() {
		mode, ok := tools.EffectivePreference(api.ToolPreferences{
			"billing":        api.ToolModeOff,
			"invoice_delete": api.ToolModeAsk,
		}, tools.ToolInfo{Name: "invoice_delete", Group: "billing"})

		Expect(ok).To(BeTrue())
		Expect(mode).To(Equal(api.ToolModeAsk))
	})

	It("normalizes only the canonical modes", func() {
		on, ok := tools.NormalizedPreference(api.ToolPreferences{"search": api.ToolModeOn}, "search")
		Expect(ok).To(BeTrue())
		Expect(on).To(Equal(api.ToolModeOn))

		off, ok := tools.NormalizedPreference(api.ToolPreferences{"search": api.ToolModeOff}, "search")
		Expect(ok).To(BeTrue())
		Expect(off).To(Equal(api.ToolModeOff))

		_, ok = tools.NormalizedPreference(api.ToolPreferences{"search": "enabled"}, "search")
		Expect(ok).To(BeFalse())
		_, ok = tools.NormalizedPreference(api.ToolPreferences{"search": "disabled"}, "search")
		Expect(ok).To(BeFalse())
	})
})

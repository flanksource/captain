package api_test

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Tool preferences", func() {
	It("survives a Spec JSON round trip", func() {
		in := api.Spec{
			Model:           api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic},
			Prompt:          api.Prompt{User: "inspect invoices"},
			ToolPreferences: api.ToolPreferences{"billing": api.ToolModeAsk, "invoice_delete": api.ToolModeOff},
		}

		encoded, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		var wire map[string]any
		Expect(json.Unmarshal(encoded, &wire)).To(Succeed())
		Expect(wire["toolPreferences"]).To(Equal(map[string]any{
			"billing":        "ask",
			"invoice_delete": "off",
		}))

		var out api.Spec
		Expect(json.Unmarshal(encoded, &out)).To(Succeed())
		Expect(out.ToolPreferences).To(Equal(in.ToolPreferences))
	})

	It("merges per-turn preferences key-wise, leaving untouched tools alone", func() {
		base := api.Spec{ToolPreferences: api.ToolPreferences{
			"billing": api.ToolModeAsk,
			"search":  api.ToolModeOff,
		}}
		override := api.Spec{ToolPreferences: api.ToolPreferences{
			"billing": api.ToolModeOn,
		}}

		// Each key is an independent decision, so an override that speaks about
		// billing says nothing about search — it must not silently re-enable it.
		Expect(base.Merge(override).ToolPreferences).To(Equal(api.ToolPreferences{
			"billing": api.ToolModeOn,
			"search":  api.ToolModeOff,
		}))
		Expect(base.Merge(api.Spec{}).ToolPreferences).To(Equal(base.ToolPreferences))
	})

	It("rejects an unknown preference before provider execution", func() {
		spec := api.Spec{
			Model:           api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic},
			Prompt:          api.Prompt{User: "inspect invoices"},
			ToolPreferences: api.ToolPreferences{"billing": "sometimes"},
		}

		Expect(spec.Validate()).To(MatchError(ContainSubstring(`invalid tool preference "sometimes" for "billing"`)))
	})

	It("rejects removed enabled and disabled labels", func() {
		for _, mode := range []api.ToolMode{"enabled", "disabled"} {
			spec := api.Spec{
				Model:           api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic},
				Prompt:          api.Prompt{User: "inspect invoices"},
				ToolPreferences: api.ToolPreferences{"billing": mode},
			}
			Expect(spec.Validate()).To(MatchError(ContainSubstring(`invalid tool preference`)))

			permissions := api.Permissions{Tools: api.Tools{Modes: map[string]api.ToolMode{"billing": mode}}}
			Expect(permissions.Validate()).To(MatchError(ContainSubstring(`invalid tool mode`)))
		}
	})
})

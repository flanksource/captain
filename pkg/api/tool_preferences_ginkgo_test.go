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
			Model:           api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI},
			Prompt:          api.Prompt{User: "inspect invoices"},
			ToolPreferences: api.ToolPreferences{"billing": api.ToolPolicyAsk, "invoice_delete": api.ToolPolicyDeny},
		}

		encoded, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		var wire map[string]any
		Expect(json.Unmarshal(encoded, &wire)).To(Succeed())
		Expect(wire["toolPreferences"]).To(Equal(map[string]any{
			"billing":        "ask",
			"invoice_delete": "deny",
		}))

		var out api.Spec
		Expect(json.Unmarshal(encoded, &out)).To(Succeed())
		Expect(out.ToolPreferences).To(Equal(in.ToolPreferences))
	})

	// This encoding has no separate allow list, so "on" was always its way of
	// saying auto-run and must decode to allow — not to auto, which is what the
	// legacy permissions.tools modes map means by the same word.
	It("decodes the legacy on/off spellings as allow/deny", func() {
		var out api.Spec
		Expect(json.Unmarshal([]byte(
			`{"toolPreferences":{"billing":"on","search":"off","audit":"auto"}}`), &out)).To(Succeed())

		Expect(out.ToolPreferences).To(Equal(api.ToolPreferences{
			"billing": api.ToolPolicyAllow,
			"search":  api.ToolPolicyDeny,
			"audit":   api.ToolPolicyAuto,
		}))

		// Decoded once, they are re-emitted in the canonical vocabulary only.
		encoded, err := json.Marshal(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"billing":"allow"`))
		Expect(string(encoded)).NotTo(ContainSubstring(`"on"`))
	})

	It("merges per-turn preferences key-wise, leaving untouched tools alone", func() {
		base := api.Spec{ToolPreferences: api.ToolPreferences{
			"billing": api.ToolPolicyAsk,
			"search":  api.ToolPolicyDeny,
		}}
		override := api.Spec{ToolPreferences: api.ToolPreferences{
			"billing": api.ToolPolicyAllow,
		}}

		// Each key is an independent decision, so an override that speaks about
		// billing says nothing about search — it must not silently re-enable it.
		Expect(base.Merge(override).ToolPreferences).To(Equal(api.ToolPreferences{
			"billing": api.ToolPolicyAllow,
			"search":  api.ToolPolicyDeny,
		}))
		Expect(base.Merge(api.Spec{}).ToolPreferences).To(Equal(base.ToolPreferences))
	})

	It("rejects an unknown preference before provider execution", func() {
		spec := api.Spec{
			Model:           api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI},
			Prompt:          api.Prompt{User: "inspect invoices"},
			ToolPreferences: api.ToolPreferences{"billing": "sometimes"},
		}

		Expect(spec.Validate()).To(MatchError(ContainSubstring(`invalid tool preference "sometimes" for "billing"`)))
	})

	It("rejects an unknown preference at the decode boundary too", func() {
		var out api.Spec
		Expect(json.Unmarshal([]byte(`{"toolPreferences":{"billing":"sometimes"}}`), &out)).
			To(MatchError(ContainSubstring(`invalid tool preference "sometimes" for "billing"`)))
	})

	It("rejects removed enabled and disabled labels", func() {
		for _, policy := range []api.ToolPolicy{"enabled", "disabled"} {
			spec := api.Spec{
				Model:           api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI},
				Prompt:          api.Prompt{User: "inspect invoices"},
				ToolPreferences: api.ToolPreferences{"billing": policy},
			}
			Expect(spec.Validate()).To(MatchError(ContainSubstring(`invalid tool preference`)))

			var permissions api.Permissions
			Expect(json.Unmarshal([]byte(
				`{"tools":{"modes":{"billing":"`+string(policy)+`"}}}`), &permissions)).
				To(MatchError(ContainSubstring(`invalid tool policy`)))
		}
	})
})

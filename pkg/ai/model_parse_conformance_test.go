package ai

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// The parse conformance suite is the single behavioural contract for turning a
// user-written model string into a concrete model/backend/effort. It pins the
// two entry points that reach the parser:
//
//   - specPath        — `captain ai --model X` and spec/`.captain.yaml` decoding:
//     api.Model.Expand (the compact grammar) then ai.Resolve.
//     See pkg/cli/provider_defaults.go and pkg/cli/ai.go.
//   - frontmatterPath — prompt frontmatter: api.Model.ExpandCSV (no colon
//     grammar at all) then ai.Resolve.
//     See pkg/cli/prompt_render.go.
//
// These two ran different parsers over the same syntax and disagreed about which
// models existed. They now share one parser (pkg/api/registry), so every case
// below asserts both paths agree — that agreement is the contract.

func specPath(s string) (api.Model, error) {
	m, err := api.Model{Name: s}.Expand()
	if err != nil {
		return api.Model{}, err
	}
	return Resolve(m)
}

func frontmatterPath(s string) (api.Model, error) {
	return Resolve(api.Model{Name: s}.ExpandCSV())
}

// parseCase is one model string and the model it must resolve to, on both entry
// points. wantErr instead asserts both fail with a message containing it.
type parseCase struct {
	in      string
	want    api.Model
	wantErr string
}

// model builds the expected resolution: the driver-ready id, the family that
// owns it, and the mechanism serving it. The provider is spelled out rather than
// left implicit because it is exactly what a name determines — and the one thing
// a caller never gets to choose.
func model(name string, provider *api.ModelProvider, mode api.RuntimeMode, effort api.Effort) api.Model {
	return api.Model{Name: name, Provider: provider, Mode: mode, Effort: effort}
}

// agreedCases are inputs both entry points already resolve identically. These
// are pure regression pins: the unified parser must not change any of them.
var agreedCases = []parseCase{
	// Family aliases resolve to the exact current registry model.
	{in: "sonnet", want: model("claude-sonnet-5", api.Anthropic, api.ModeAgent, "")},
	{in: "opus", want: model("claude-opus-5", api.Anthropic, api.ModeAgent, "")},
	{in: "fable", want: model("claude-fable-5", api.Anthropic, api.ModeAgent, "")},

	// A superseded exact id is rewritten to its successor.
	{in: "claude-sonnet-4-5", want: model("claude-sonnet-4-6", api.Anthropic, api.ModeAgent, "")},

	// The catalog prefix is stripped; "models/" likewise.
	{in: "anthropic/claude-sonnet-5", want: model("claude-sonnet-5", api.Anthropic, api.ModeAgent, "")},
	{in: "googleai/gemini-3.5-flash", want: model("gemini-3.5-flash", api.Google, api.ModeAPI, "")},
	{in: "models/gemini-3.5-flash", want: model("gemini-3.5-flash", api.Google, api.ModeAPI, "")},

	// Mode prefix and effort suffix.
	{in: "cli:sonnet:high", want: model("claude-sonnet-5", api.Anthropic, api.ModeCLI, api.EffortHigh)},
	{in: "agent:sonnet", want: model("claude-sonnet-5", api.Anthropic, api.ModeAgent, "")},

	// Codex codenames are catalog aliases, so they resolve wherever a model name
	// is accepted. `--model agent:sol` used to error while the identical value in
	// prompt frontmatter ran: the compact grammar could not see pkg/ai's alias
	// table. This is the divergence the single parser exists to remove.
	{in: "agent:sol", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAgent, "")},
	{in: "agent:sol:high", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAgent, api.EffortHigh)},
	{in: "api:sol", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAPI, "")},
	// A bare codename resolves too. It used to fail on both paths only because
	// the bare path went through an alias-blind claim — an accident of which
	// parser ran, not a decision.
	{in: "sol", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAgent, "")},
	{in: "terra", want: model("gpt-5.6-terra", api.OpenAI, api.ModeAgent, "")},
	{in: "luna", want: model("gpt-5.6-luna", api.OpenAI, api.ModeAgent, "")},

	// A bare family sentinel resolves to that family's latest model on the mode
	// the provider defaults to. It used to be asymmetric — "codex" forced the CLI
	// and "claude" stayed a literal sentinel — because the name itself carried a
	// mode. It no longer does, so both now read the same way.
	{in: "codex", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAgent, "")},
	{in: "claude", want: model("claude-opus-5", api.Anthropic, api.ModeAgent, "")},

	// A sentinel with an explicit mode resolves too. This does NOT go through the
	// agent-sentinel shortcut (that one only fires off the API mode), so it lands
	// on the provider's emptyFamily — which must be a family name. An id there
	// matched no catalog row and left "api:codex" as the literal "codex".
	{in: "api:codex", want: model("gpt-5.6-sol", api.OpenAI, api.ModeAPI, "")},
	{in: "cli:codex", want: model("gpt-5.6-sol", api.OpenAI, api.ModeCLI, "")},

	// A multi-slash id resolves off its LAST segment and keeps its name verbatim,
	// so OpenRouter-style proxied names survive. (api.ProviderFor alone cannot do
	// this; inferModelBackend's last-slash retry is what makes it work.)
	{in: "openrouter/anthropic/claude-x", want: model("openrouter/anthropic/claude-x", api.Anthropic, api.ModeAgent, "")},

	{in: "gemini-3.5-flash", want: model("gemini-3.5-flash", api.Google, api.ModeAPI, "")},
	{in: "deepseek-chat", want: model("deepseek-chat", api.DeepSeek, api.ModeAPI, "")},
	// The bare "gpt-5.6" id exists only on the api mode, so with no mode selected
	// it lands on openai's agent default and fails naming the cell. Selecting the
	// mode explicitly is what runs it — see the "api:sol" cases above.
	{in: "gpt-5.6", wantErr: `model "gpt-5.6" is not available on openai agent`},
	{in: "o3", want: model("o3", api.OpenAI, api.ModeAgent, "")},

	// Unknown models fail loud on both paths, telling the user how to recover.
	{in: "totally-unknown", wantErr: "unable to infer a provider from the model name"},

	// sora is a video model captain cannot run. It is claimed by no provider, so
	// it fails loud rather than resolving to something unusable.
	{in: "sora", wantErr: "unable to infer a provider from the model name"},
	{in: "sora-2", wantErr: "unable to infer a provider from the model name"},

	// grok mode was removed from the codex CLI, so no provider claims it. Pinned
	// as failing rather than deleted: silently re-claiming grok would route models
	// to a backend captain no longer drives.
	{in: "grok-2", wantErr: "unable to infer a provider from the model name"},
	{in: "grok-code-fast-1", wantErr: "unable to infer a provider from the model name"},
}

func expectModel(got api.Model, want api.Model) {
	GinkgoHelper()
	Expect(got.Name).To(Equal(want.Name), "model name")
	Expect(got.Provider).To(Equal(want.Provider), "provider")
	Expect(got.Mode).To(Equal(want.Mode), "mode")
	Expect(got.Effort).To(Equal(want.Effort), "effort")
}

var _ = Describe("model parse conformance", func() {
	Describe("inputs both entry points agree on", func() {
		for _, tc := range agreedCases {
			tc := tc
			label := fmt.Sprintf("%q", tc.in)
			if tc.wantErr != "" {
				It(label+" fails loud on both paths", func() {
					_, specErr := specPath(tc.in)
					_, fmErr := frontmatterPath(tc.in)
					Expect(specErr).To(MatchError(ContainSubstring(tc.wantErr)))
					Expect(fmErr).To(MatchError(ContainSubstring(tc.wantErr)))
				})
				continue
			}
			It(label+" resolves identically on both paths", func() {
				got, err := specPath(tc.in)
				Expect(err).NotTo(HaveOccurred())
				expectModel(got, tc.want)

				got, err = frontmatterPath(tc.in)
				Expect(err).NotTo(HaveOccurred())
				expectModel(got, tc.want)
			})
		}
	})

	Describe("comma-separated fallback chains", func() {
		It("keeps the primary first and resolves each element independently", func() {
			got, err := specPath("sonnet,cli:opus:high")
			Expect(err).NotTo(HaveOccurred())
			expectModel(got, model("claude-sonnet-5", api.Anthropic, api.ModeAgent, ""))
			Expect(got.Fallbacks).To(HaveLen(1))
			expectModel(got.Fallbacks[0], model("claude-opus-5", api.Anthropic, api.ModeCLI, api.EffortHigh))
		})
	})

	Describe("wildcard selectors", func() {
		It("is rejected for a single model", func() {
			_, err := frontmatterPath("*:fable")
			Expect(err).To(MatchError(ContainSubstring("only valid for --multi-models")))
		})

		It("fans out to every mode of the claimed family", func() {
			models, err := ResolveMulti([]string{"*:fable"}, api.Model{Name: "sonnet"})
			Expect(err).NotTo(HaveOccurred())
			modes := make([]api.RuntimeMode, 0, len(models))
			for _, m := range models {
				Expect(m.Name).To(Equal("claude-fable-5"))
				Expect(m.Provider).To(Equal(api.Anthropic))
				modes = append(modes, m.Mode)
			}
			Expect(modes).To(ContainElement(api.ModeAPI))
			Expect(modes).To(ContainElement(api.ModeAgent))
		})
	})
})

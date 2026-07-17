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
//     api.Model.Expand (the compact grammar) then ResolveModelSelectors.
//     See pkg/cli/provider_defaults.go and pkg/cli/ai.go.
//   - frontmatterPath — prompt frontmatter: api.Model.ExpandCSV (no colon
//     grammar at all) then ResolveModelSelectors.
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
	return ResolveModelSelectors(m)
}

func frontmatterPath(s string) (api.Model, error) {
	return ResolveModelSelectors(api.Model{Name: s}.ExpandCSV())
}

// parseCase is one model string and the model it must resolve to, on both entry
// points. wantErr instead asserts both fail with a message containing it.
type parseCase struct {
	in      string
	want    api.Model
	wantErr string
}

func model(name string, backend api.Backend, effort api.Effort) api.Model {
	return api.Model{Name: name, Backend: backend, Effort: effort}
}

// agreedCases are inputs both entry points already resolve identically. These
// are pure regression pins: the unified parser must not change any of them.
var agreedCases = []parseCase{
	// Family aliases resolve to the exact current registry model.
	{in: "sonnet", want: model("claude-sonnet-5", api.BackendAnthropic, "")},
	{in: "opus", want: model("claude-opus-4-8", api.BackendAnthropic, "")},
	{in: "fable", want: model("claude-fable-5", api.BackendAnthropic, "")},

	// A superseded exact id is rewritten to its successor.
	{in: "claude-sonnet-4-5", want: model("claude-sonnet-4-6", api.BackendAnthropic, "")},

	// The catalog prefix is stripped; "models/" likewise.
	{in: "anthropic/claude-sonnet-5", want: model("claude-sonnet-5", api.BackendAnthropic, "")},
	{in: "googleai/gemini-3.5-flash", want: model("gemini-3.5-flash", api.BackendGemini, "")},
	{in: "models/gemini-3.5-flash", want: model("gemini-3.5-flash", api.BackendGemini, "")},

	// Mode prefix and effort suffix.
	{in: "cli:sonnet:high", want: model("claude-sonnet-5", api.BackendClaudeCLI, api.EffortHigh)},
	{in: "agent:sonnet", want: model("claude-sonnet-5", api.BackendClaudeAgent, "")},

	// Codex codenames are catalog aliases, so they resolve wherever a model name
	// is accepted. `--model agent:sol` used to error while the identical value in
	// prompt frontmatter ran: the compact grammar could not see pkg/ai's alias
	// table. This is the divergence the single parser exists to remove.
	{in: "agent:sol", want: model("gpt-5.6-sol", api.BackendCodexAgent, "")},
	{in: "agent:sol:high", want: model("gpt-5.6-sol", api.BackendCodexAgent, api.EffortHigh)},
	{in: "api:sol", want: model("gpt-5.6-sol", api.BackendOpenAI, "")},
	// A bare codename resolves too. It used to fail on both paths only because
	// the bare path went through an alias-blind claim — an accident of which
	// parser ran, not a decision.
	{in: "sol", want: model("gpt-5.6-sol", api.BackendOpenAI, "")},
	{in: "terra", want: model("gpt-5.6-terra", api.BackendOpenAI, "")},
	{in: "luna", want: model("gpt-5.6-luna", api.BackendOpenAI, "")},

	// Bare CLI sentinels are asymmetric and must stay that way: "codex" resolves
	// to the latest codex model on the CLI, "claude" stays a literal sentinel on
	// the API backend.
	{in: "codex", want: model("gpt-5.6-sol", api.BackendCodexCLI, "")},
	{in: "claude", want: model("claude", api.BackendAnthropic, "")},

	// grok is served through the codex CLI and passes through verbatim.
	{in: "grok-2", want: model("grok-2", api.BackendCodexCLI, "")},
	{in: "grok-code-fast-1", want: model("grok-code-fast-1", api.BackendCodexCLI, "")},

	// A multi-slash id resolves off its LAST segment and keeps its name verbatim,
	// so OpenRouter-style proxied names survive. (api.InferBackend alone cannot do
	// this; inferModelBackend's last-slash retry is what makes it work.)
	{in: "openrouter/anthropic/claude-x", want: model("openrouter/anthropic/claude-x", api.BackendAnthropic, "")},

	{in: "gemini-3.5-flash", want: model("gemini-3.5-flash", api.BackendGemini, "")},
	{in: "deepseek-chat", want: model("deepseek-chat", api.BackendDeepSeek, "")},
	{in: "gpt-5.6", want: model("gpt-5.6", api.BackendOpenAI, "")},
	{in: "o3", want: model("o3", api.BackendOpenAI, "")},

	// Unknown models fail loud on both paths, telling the user how to recover.
	{in: "totally-unknown", wantErr: "pass an explicit backend"},

	// sora is a video model captain cannot run. It is claimed by no provider, so
	// it fails loud rather than resolving to something unusable.
	{in: "sora", wantErr: "pass an explicit backend"},
	{in: "sora-2", wantErr: "pass an explicit backend"},
}

func expectModel(got api.Model, want api.Model) {
	GinkgoHelper()
	Expect(got.Name).To(Equal(want.Name), "model name")
	Expect(got.Backend).To(Equal(want.Backend), "backend")
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
			expectModel(got, model("claude-sonnet-5", api.BackendAnthropic, ""))
			Expect(got.Fallbacks).To(HaveLen(1))
			expectModel(got.Fallbacks[0], model("claude-opus-4-8", api.BackendClaudeCLI, api.EffortHigh))
		})
	})

	Describe("wildcard selectors", func() {
		It("is rejected for a single model", func() {
			_, err := frontmatterPath("*:fable")
			Expect(err).To(MatchError(ContainSubstring("only valid for --multi-models")))
		})

		It("fans out to every backend of the claimed family", func() {
			models, err := ResolveRuntimeSelectors([]string{"*:fable"}, api.Model{Name: "sonnet"})
			Expect(err).NotTo(HaveOccurred())
			backends := make([]api.Backend, 0, len(models))
			for _, m := range models {
				Expect(m.Name).To(Equal("claude-fable-5"))
				backends = append(backends, m.Backend)
			}
			Expect(backends).To(ContainElement(api.BackendAnthropic))
			Expect(backends).To(ContainElement(api.BackendClaudeAgent))
		})
	})
})

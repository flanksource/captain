package registry

import "strings"

// The provider descriptors. Everything captain used to rediscover with string
// prefixes and per-backend switches is a field here.
//
// Media types come from what each adapter can actually carry; Interrupt/Steer
// mirror the InterruptibleProvider/SteerableProvider implementations in
// pkg/ai/provider (steer: claude-agent only; interrupt: claude-agent and
// codex-agent). Streaming is true everywhere because every adapter implements
// ExecuteStream; it is declared rather than assumed so a future non-streaming
// adapter has an honest place to say so.
var (
	Anthropic = &Provider{
		Name:          "anthropic",
		AgentName:     "claude",
		CatalogPrefix: "anthropic",
		PricingPrefix: "anthropic",
		EnvVars:       []string{"ANTHROPIC_API_KEY"},
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI:   {Backend: BackendAnthropic, Streaming: true, CallerTools: true, MediaTypes: []string{"image/*"}},
			ModeCLI:   {Backend: BackendClaudeCLI, Streaming: true, Resume: true},
			ModeAgent: {Backend: BackendClaudeAgent, Streaming: true, Resume: true, Interrupt: true, Steer: true, CallerTools: true, MediaTypes: []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}},
			ModeCmux:  {Backend: BackendClaudeCmux, Streaming: true, Resume: true, Keyless: true},
		},
		modeTokens: sortModeTokens([]modeToken{
			{prefix: "claude-agent", mode: ModeAgent},
			{prefix: "claude-code", mode: ModeCLI},
		}),
		claimPrefixes: []string{"claude", "opus", "sonnet", "haiku", "fable"},
		identityTrim:  []string{"claude-agent-", "claude-code-", "claude-"},
		families:      []string{"fable", "opus", "sonnet", "haiku"},
		emptyTokens:   []string{""},
		emptyFamily:   "opus",
		genConfig:     anthropicGenerationConfig,
	}

	OpenAI = &Provider{
		Name:          "openai",
		AgentName:     "codex",
		CatalogPrefix: "openai",
		PricingPrefix: "openai",
		EnvVars:       []string{"OPENAI_API_KEY"},
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI:   {Backend: BackendOpenAI, Streaming: true, CallerTools: true, MediaTypes: []string{"image/*"}},
			ModeCLI:   {Backend: BackendCodexCLI, Streaming: true, Resume: true, MediaTypes: []string{"image/*"}},
			ModeAgent: {Backend: BackendCodexAgent, Streaming: true, Resume: true, Interrupt: true, CallerTools: true, MediaTypes: []string{"image/*"}},
			ModeCmux:  {Backend: BackendCodexCmux, Streaming: true, Resume: true, Keyless: true},
		},
		// A bare "codex" is the CLI, not the API — the asymmetry with "claude"
		// (which stays on the API) is long-standing user-visible behaviour.
		// "grok-" is served through the codex CLI.
		modeTokens: sortModeTokens([]modeToken{
			{prefix: "codex-agent", mode: ModeAgent},
			{prefix: "codex", mode: ModeCLI},
		}),
		claimPrefixes: []string{"gpt-", "o1", "o3", "o4"},
		identityTrim:  []string{"codex-agent-", "codex-"},
		families:      []string{"gpt"},
		emptyTokens:   []string{"", "codex"},
		// A family name, not a model id: emptyFamily is matched against
		// KnownModel.Family, so an id here matches no row and the token falls
		// through unresolved ("api:codex" stayed the literal "codex"). Which gpt
		// model a bare "codex" lands on is decided by priority in the catalog —
		// gpt-5.6-sol carries priority 1 — not by naming it here.
		emptyFamily: "gpt",
		genConfig:   openaiGenerationConfig,
	}

	Google = &Provider{
		Name:      "google",
		AgentName: "gemini",
		// googleai for catalog/menu/genkit ids, google for pricing keys. These
		// are independent on purpose; see the Provider doc.
		CatalogPrefix: "googleai",
		PricingPrefix: "google",
		EnvVars:       []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI: {Backend: BackendGemini, Streaming: true, CallerTools: true, MediaTypes: []string{"image/*", "audio/*", "video/*", "application/pdf"}},
			ModeCLI: {Backend: BackendGeminiCLI, Streaming: true},
		},
		modeTokens: sortModeTokens([]modeToken{
			{prefix: "gemini-cli", mode: ModeCLI},
		}),
		claimPrefixes: []string{"gemini", "models/gemini"},
		identityTrim:  []string{"gemini-cli-"},
		families:      []string{"gemini"},
		genConfig:     googleGenerationConfig,
	}

	DeepSeek = &Provider{
		Name:          "deepseek",
		AgentName:     "deepseek",
		CatalogPrefix: "deepseek",
		PricingPrefix: "deepseek",
		EnvVars:       []string{"DEEPSEEK_API_KEY"},
		modes: map[RuntimeMode]ModeCapabilities{
			// DeepSeek selects reasoning by model id (deepseek-reasoner vs
			// deepseek-chat) and ships no attachment support.
			ModeAPI: {Backend: BackendDeepSeek, Streaming: true, CallerTools: true},
		},
		claimPrefixes: []string{"deepseek"},
		families:      []string{"deepseek"},
	}
)

// Providers returns the provider families in canonical claim order. Parse walks
// this slice and the FIRST provider to claim a token wins, so the order is a
// contract, not an accident. The claim prefixes are disjoint today; the order
// also fixes AllBackends ordering.
func Providers() []*Provider {
	return []*Provider{Anthropic, OpenAI, Google, DeepSeek}
}

// ProviderByName resolves a provider by Name, CatalogPrefix, or PricingPrefix,
// so both "google" and "googleai" find Google.
func ProviderByName(name string) (*Provider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range Providers() {
		if name == p.Name || name == p.CatalogPrefix || name == p.PricingPrefix {
			return p, true
		}
	}
	return nil, false
}

// ProviderFor returns the descriptor that owns a backend, and the mode that
// backend represents. It replaces Backend.Provider, Backend.Kind, Backend.Family,
// registryProviderForBackend, and modelSourceBackend — one reverse lookup over
// the same table the forward direction uses.
func ProviderFor(b Backend) (*Provider, RuntimeMode, bool) {
	for _, p := range Providers() {
		for _, mode := range p.Modes() {
			if p.modes[mode].Backend == b {
				return p, mode, true
			}
		}
	}
	return nil, "", false
}

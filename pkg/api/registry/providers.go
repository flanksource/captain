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
		DefaultMode:   ModeAgent,
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI:   {Streaming: true, CallerTools: true, MediaTypes: []string{"image/*"}, SchemaDialect: SchemaDialectAnthropic},
			ModeCLI:   {Streaming: true, Resume: true, ToolPolicy: true, RequiredBinary: "claude", SchemaDialect: SchemaDialectAnthropic, RunsThroughClaudeCode: true},
			ModeAgent: {Streaming: true, Resume: true, Interrupt: true, Steer: true, CallerTools: true, ToolPolicy: true, MediaTypes: []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}, RequiredBinary: "tsx", SchemaDialect: SchemaDialectAnthropic, RunsThroughClaudeCode: true},
			// cmux submits no response format, so it is in neither schema subset.
			ModeCmux: {Streaming: true, Resume: true, Keyless: true, ToolPolicy: true, RequiredBinary: "claude"},
		},
		claimPrefixes: []string{"claude", "opus", "sonnet", "haiku", "fable"},
		identityTrim:  []string{"claude-"},
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
		DefaultMode:   ModeAgent,
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI:   {Streaming: true, CallerTools: true, MediaTypes: []string{"image/*"}, SchemaDialect: SchemaDialectOpenAI},
			ModeCLI:   {Streaming: true, Resume: true, MediaTypes: []string{"image/*"}, RequiredBinary: "codex", SchemaDialect: SchemaDialectOpenAI},
			ModeAgent: {Streaming: true, Resume: true, Interrupt: true, CallerTools: true, MediaTypes: []string{"image/*"}, RequiredBinary: "codex", SchemaDialect: SchemaDialectOpenAI},
			// Prompt-only cmux sessions submit no response format to OpenAI.
			ModeCmux: {Streaming: true, Resume: true, Keyless: true, RequiredBinary: "codex"},
		},
		// "codex" claims the FAMILY (OpenAI's coding model), not a mode. It used
		// to reach OpenAI through a mode token that also forced the CLI.
		claimPrefixes: []string{"gpt-", "o1", "o3", "o4", "codex"},
		identityTrim:  []string{"codex-"},
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
		// No agent cell; its cli cell carries no tools, resume or media types.
		DefaultMode: ModeAPI,
		modes: map[RuntimeMode]ModeCapabilities{
			ModeAPI: {Streaming: true, CallerTools: true, MediaTypes: []string{"image/*", "audio/*", "video/*", "application/pdf"}},
			ModeCLI: {Streaming: true, RequiredBinary: "gemini"},
		},
		claimPrefixes: []string{"gemini", "models/gemini"},
		families:      []string{"gemini"},
		genConfig:     googleGenerationConfig,
	}

	DeepSeek = &Provider{
		Name:          "deepseek",
		AgentName:     "deepseek",
		CatalogPrefix: "deepseek",
		PricingPrefix: "deepseek",
		EnvVars:       []string{"DEEPSEEK_API_KEY"},
		DefaultMode:   ModeAPI,
		modes: map[RuntimeMode]ModeCapabilities{
			// DeepSeek selects reasoning by model id (deepseek-reasoner vs
			// deepseek-chat) and ships no attachment support.
			ModeAPI: {Streaming: true, CallerTools: true},
		},
		claimPrefixes: []string{"deepseek"},
		families:      []string{"deepseek"},
	}
)

// Providers returns the provider families in canonical claim order. Parse walks
// this slice and the FIRST provider to claim a token wins, so the order is a
// contract, not an accident. The claim prefixes are disjoint today; the order
// also fixes AllRuntimes ordering.
func Providers() []*Provider {
	return []*Provider{Anthropic, OpenAI, Google, DeepSeek}
}

// ProviderByName resolves a provider by Name, AgentName, CatalogPrefix, or
// PricingPrefix, so "google", "gemini", and "googleai" all find Google.
func ProviderByName(name string) (*Provider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range Providers() {
		if name == p.Name || name == p.AgentName || name == p.CatalogPrefix || name == p.PricingPrefix {
			return p, true
		}
	}
	return nil, false
}

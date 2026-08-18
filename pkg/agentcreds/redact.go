package agentcreds

import (
	"encoding/json"
	"fmt"
	"time"
)

// Redaction here is allowlist-shaped: every output document is rebuilt from
// named fields rather than produced by deleting known-bad keys from the input.
// A denylist would leak by default the first time a provider adds a field, and
// these documents are handed to a sandbox that captain does not trust.

// claudeSource is the subset of the Keychain blob (or ~/.claude/.credentials.json)
// that is read. Fields absent here are dropped, including the whole mcpOAuth map:
// it holds accessToken/refreshToken/clientSecret triples for every MCP server the
// user has authorized, which have nothing to do with the agent's own login.
type claudeSource struct {
	ClaudeAiOauth struct {
		AccessToken      string   `json:"accessToken"`
		ExpiresAt        int64    `json:"expiresAt"`
		Scopes           []string `json:"scopes,omitempty"`
		SubscriptionType string   `json:"subscriptionType,omitempty"`
		RateLimitTier    string   `json:"rateLimitTier,omitempty"`
	} `json:"claudeAiOauth"`
}

// claudeRedacted is what the sandbox receives. refreshToken and
// refreshTokenExpiresAt are absent by construction.
type claudeRedacted struct {
	ClaudeAiOauth claudeOauthRedacted `json:"claudeAiOauth"`
}

type claudeOauthRedacted struct {
	AccessToken      string   `json:"accessToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

// RedactClaude strips the refresh token from a Claude Code credential document
// and reports when what remains stops working.
func RedactClaude(raw []byte) (Credential, error) {
	var source claudeSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return Credential{}, fmt.Errorf("parse claude credentials: %w", err)
	}
	oauth := source.ClaudeAiOauth
	if oauth.AccessToken == "" {
		return Credential{}, fmt.Errorf("claude credentials carry no claudeAiOauth.accessToken; run `claude` on this host to log in")
	}
	if oauth.ExpiresAt == 0 {
		return Credential{}, fmt.Errorf("claude credentials carry no claudeAiOauth.expiresAt, so a republish cannot be scheduled")
	}
	payload, err := json.MarshalIndent(claudeRedacted{ClaudeAiOauth: claudeOauthRedacted(oauth)}, "", "  ")
	if err != nil {
		return Credential{}, fmt.Errorf("marshal redacted claude credentials: %w", err)
	}
	return Credential{
		Provider:  ProviderClaude,
		Filename:  ClaudeFilename,
		Payload:   append(payload, '\n'),
		ExpiresAt: epochMillis(oauth.ExpiresAt),
	}, nil
}

// codexSource mirrors ~/.codex/auth.json. OPENAI_API_KEY is a *string so the
// distinction between "absent" and "explicitly null" survives the round trip —
// codex writes null in ChatGPT-subscription mode and reads it back.
type codexSource struct {
	AuthMode     string  `json:"auth_mode,omitempty"`
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id,omitempty"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh,omitempty"`
}

type codexRedacted struct {
	AuthMode     string       `json:"auth_mode,omitempty"`
	OpenAIAPIKey *string      `json:"OPENAI_API_KEY"`
	Tokens       *codexTokens `json:"tokens,omitempty"`
	LastRefresh  string       `json:"last_refresh,omitempty"`
}

// codexTokens keeps refresh_token as a present-but-empty string rather than
// omitting the key: codex-rs models TokenData.refresh_token as a non-optional
// String, so dropping the field risks failing deserialization outright.
//
// Verified against the real CLI with hack/credspike.go — `codex login status`
// reports "Logged in using ChatGPT" against a document redacted this way.
type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

// RedactCodex strips the refresh token from a codex credential document.
//
// An API-key login has no tokens block at all and no expiry; it is passed
// through unchanged with a far-future expiry, because there is nothing to
// refresh and nothing to strip.
func RedactCodex(raw []byte, now time.Time) (Credential, error) {
	var source codexSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return Credential{}, fmt.Errorf("parse codex auth.json: %w", err)
	}

	redacted := codexRedacted{
		AuthMode:     source.AuthMode,
		OpenAIAPIKey: source.OpenAIAPIKey,
		LastRefresh:  source.LastRefresh,
	}
	expiry := now.Add(apiKeyCredentialLifetime)

	if source.Tokens != nil && source.Tokens.AccessToken != "" {
		tokenExpiry, err := jwtExpiry(source.Tokens.AccessToken)
		if err != nil {
			return Credential{}, fmt.Errorf("read codex access_token expiry: %w", err)
		}
		expiry = tokenExpiry
		redacted.Tokens = &codexTokens{
			IDToken:     source.Tokens.IDToken,
			AccessToken: source.Tokens.AccessToken,
			AccountID:   source.Tokens.AccountID,
			// RefreshToken deliberately left as the zero value.
		}
	} else if source.OpenAIAPIKey == nil || *source.OpenAIAPIKey == "" {
		return Credential{}, fmt.Errorf("codex auth.json has neither tokens.access_token nor OPENAI_API_KEY; run `codex login` on this host")
	}

	payload, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return Credential{}, fmt.Errorf("marshal redacted codex auth: %w", err)
	}
	return Credential{
		Provider:  ProviderCodex,
		Filename:  CodexFilename,
		Payload:   append(payload, '\n'),
		ExpiresAt: expiry,
	}, nil
}

// apiKeyCredentialLifetime is the nominal expiry given to a credential that
// carries no expiring token. An API key does not lapse, but every consumer
// schedules off ExpiresAt, so it needs a value; a day keeps the republish loop
// running without pretending the key is eternal.
const apiKeyCredentialLifetime = 24 * time.Hour

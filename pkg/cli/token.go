// The captain token command group: mint, list and revoke the bearer
// credentials that let something off this box reach `captain serve`.
//
// The whole group is local-only. RegisterExecutionRoutes publishes cobra
// commands as REST under /api/v1, so a published `create` would let anyone who
// could already reach the API mint themselves a durable credential — the
// bootstrap hole the tokens exist to close.
package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// TokenHelp documents the group, because which of bound/pool/scope to pick is
// the part that is not guessable from the flags.
func TokenHelp() api.Textable {
	return clicky.Text("Captain API tokens", "font-bold text-blue-400").NewLine().NewLine().
		AddText("A token authenticates a caller that is not on this machine. Requests from", "text-gray-400").NewLine().
		AddText("127.0.0.1 need none, so the local webapp, CLI and hooks are unaffected.", "text-gray-400").NewLine().NewLine().
		AddText("Scopes:", "font-bold text-blue-400").NewLine().
		AddText("  git", "text-green-400").
		AddText("  — push to a served repository, and nothing else", "text-gray-500").NewLine().
		AddText("  api", "text-green-400").
		AddText("  — the HTTP API, which executes captain commands", "text-gray-500").NewLine().NewLine().
		AddText("A git token names who is pushing, in one of two ways:", "font-bold text-blue-400").NewLine().
		AddText("  captain token create worker-01", "text-green-400").
		AddText("               — bound to one agent", "text-gray-500").NewLine().
		AddText("  captain token create prod-pool --pool --max-agents 5", "text-green-400").
		AddText(" — one pool, many members", "text-gray-500").NewLine().NewLine().
		AddText("Pool members share a secret, so any member can act as any other; their ref", "text-gray-400").NewLine().
		AddText("namespaces are owned by the pool rather than the individual. Prefer a bound", "text-gray-400").NewLine().
		AddText("token unless one credential has to serve a scaled deployment.", "text-gray-400").NewLine().NewLine().
		AddText("Tokens are durable: valid until they expire or are revoked, so a restarting", "text-gray-400").NewLine().
		AddText("sidecar re-presents the same credential instead of needing a new one.", "text-gray-400").NewLine().
		AddText("The secret is shown once, at creation, and cannot be recovered afterwards.", "text-gray-400").NewLine()
}

type TokenCreateOptions struct {
	Name      string `args:"true" help:"Name for the token; a pool derives its member names from it"`
	Scope     string `flag:"scope" help:"What the token may reach: git or api" default:"git"`
	Agent     string `flag:"agent" help:"Agent this token speaks for (defaults to the token name)"`
	Pool      bool   `flag:"pool" help:"Serve many agents from one token, naming each member as it arrives"`
	MaxAgents int    `flag:"max-agents" help:"Cap a pool's members; 0 leaves it unbounded"`
	Expires   string `flag:"expires" help:"Lifetime, e.g. 90d or 720h; empty never expires"`
}

// TokenCreateResult is the one and only sighting of the credential.
//
// Token is a plain string rather than a SensitiveString on purpose: a redacted
// field would defeat the command, which exists to reveal the secret exactly
// once. Nothing stored can reconstruct it, so a caller that loses it mints
// again.
type TokenCreateResult struct {
	TokenID   string     `json:"tokenId" pretty:"label=Token ID"`
	Name      string     `json:"name" pretty:"label=Name"`
	Scope     string     `json:"scope" pretty:"label=Scope"`
	Agent     string     `json:"agent,omitempty" pretty:"label=Agent"`
	Pool      bool       `json:"pool,omitempty" pretty:"label=Pool"`
	MaxAgents int        `json:"maxAgents,omitempty" pretty:"label=Max agents"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty" pretty:"label=Expires"`
	Token     string     `json:"token" pretty:"label=Token"`
}

func RunTokenCreate(ctx context.Context, opts TokenCreateOptions) (any, error) {
	scope, err := captaintoken.ParseScope(opts.Scope)
	if err != nil {
		return nil, err
	}
	expiresAt, err := parseTokenLifetime(opts.Expires)
	if err != nil {
		return nil, err
	}
	input := database.CreateAPITokenInput{
		Name: strings.TrimSpace(opts.Name), Scope: scope, Agent: strings.TrimSpace(opts.Agent),
		Pool: opts.Pool, MaxAgents: opts.MaxAgents, ExpiresAt: expiresAt,
	}
	// The common case is one token per agent, named after it. A pool names its
	// members as they arrive, and an api token speaks for no agent at all.
	if input.Agent == "" && scope == captaintoken.ScopeGit && !opts.Pool {
		input.Agent = input.Name
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return nil, err
	}
	token, secret, err := db.CreateAPIToken(ctx, input)
	if err != nil {
		return nil, err
	}
	return TokenCreateResult{
		TokenID: token.TokenID, Name: token.Name, Scope: string(token.Scope),
		Agent: token.Agent, Pool: token.Pool, MaxAgents: token.MaxAgents,
		ExpiresAt: token.ExpiresAt, Token: secret.Value(),
	}, nil
}

// parseTokenLifetime accepts a Go duration or a day count, because a credential
// lifetime is naturally expressed in days and time.ParseDuration stops at hours.
func parseTokenLifetime(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	lifetime, err := parseLifetimeDuration(trimmed)
	if err != nil {
		return nil, fmt.Errorf("lifetime %q: expected a day count like 90d, or a duration like 720h", value)
	}
	if lifetime <= 0 {
		return nil, fmt.Errorf("lifetime %q must be positive; omit --expires for a token that never expires", value)
	}
	expiresAt := time.Now().UTC().Add(lifetime)
	return &expiresAt, nil
}

func parseLifetimeDuration(value string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(value, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		return time.Duration(count) * 24 * time.Hour, nil
	}
	return time.ParseDuration(value)
}

type TokenListOptions struct {
	Scope   string `flag:"scope" help:"Only tokens with this scope: git or api"`
	Agent   string `flag:"agent" help:"Only tokens that speak for this agent, bound or pooled"`
	Revoked bool   `flag:"revoked" help:"Include revoked tokens"`
	Limit   int    `flag:"limit" help:"Maximum tokens to list" default:"100"`
}

type TokenListEntry struct {
	TokenID string `json:"tokenId" pretty:"label=Token ID"`
	Name    string `json:"name" pretty:"label=Name"`
	Scope   string `json:"scope" pretty:"label=Scope"`
	// Agent names a bound token's identity, or a pool's members joined, so one
	// column answers "who can push as this?" for both shapes.
	Agent      string     `json:"agent,omitempty" pretty:"label=Agent"`
	Status     string     `json:"status" pretty:"label=Status"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty" pretty:"label=Expires"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" pretty:"label=Last used"`
	CreatedAt  time.Time  `json:"createdAt" pretty:"label=Created"`
}

// RunTokenList always returns a slice — an empty result renders as [] in JSON
// rather than null, so a consumer can iterate it unconditionally.
func RunTokenList(ctx context.Context, opts TokenListOptions) (any, error) {
	filter := database.ListAPITokensFilter{
		Agent: opts.Agent, IncludeRevoked: opts.Revoked, Limit: opts.Limit,
	}
	if strings.TrimSpace(opts.Scope) != "" {
		scope, err := captaintoken.ParseScope(opts.Scope)
		if err != nil {
			return nil, err
		}
		filter.Scope = scope
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := db.ListAPITokens(ctx, filter)
	if err != nil {
		return nil, err
	}
	entries := make([]TokenListEntry, 0, len(tokens))
	for _, token := range tokens {
		entries = append(entries, TokenListEntry{
			TokenID: token.TokenID, Name: token.Name, Scope: string(token.Scope),
			Agent: tokenAgentColumn(token), Status: tokenStatus(token),
			ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, CreatedAt: token.CreatedAt,
		})
	}
	return entries, nil
}

func tokenAgentColumn(token database.APIToken) string {
	if !token.Pool {
		return token.Agent
	}
	if len(token.PoolAgents) == 0 {
		return "(pool, no members yet)"
	}
	return strings.Join(token.PoolAgents, ", ")
}

// tokenStatus reports why a token cannot be used, so a listing distinguishes a
// credential that lapsed from one that was deliberately retired.
func tokenStatus(token database.APIToken) string {
	switch {
	case token.RevokedAt != nil && token.RevocationReason != "":
		return "revoked: " + token.RevocationReason
	case token.RevokedAt != nil:
		return "revoked"
	case token.ExpiresAt != nil && !time.Now().Before(*token.ExpiresAt):
		return "expired"
	default:
		return "active"
	}
}

type TokenRevokeOptions struct {
	TokenID string `args:"true" help:"Token ID to revoke, as shown by captain token list"`
	Reason  string `flag:"reason" help:"Why it was revoked, recorded alongside the token"`
}

type TokenRevokeResult struct {
	TokenID string `json:"tokenId" pretty:"label=Token ID"`
	Name    string `json:"name" pretty:"label=Name"`
	Revoked bool   `json:"revoked" pretty:"label=Revoked"`
	Reason  string `json:"reason,omitempty" pretty:"label=Reason"`
}

func RunTokenRevoke(ctx context.Context, opts TokenRevokeOptions) (any, error) {
	db, err := captainServeDB(ctx)
	if err != nil {
		return nil, err
	}
	if err := db.RevokeAPIToken(ctx, opts.TokenID, opts.Reason); err != nil {
		return nil, err
	}
	// Effective for requests that arrive after now: the verifier re-reads the
	// row on every presentation, and caches only the KDF result (R8.5).
	token, err := db.GetAPIToken(ctx, opts.TokenID)
	if err != nil {
		return nil, err
	}
	return TokenRevokeResult{
		TokenID: token.TokenID, Name: token.Name,
		Revoked: true, Reason: token.RevocationReason,
	}, nil
}

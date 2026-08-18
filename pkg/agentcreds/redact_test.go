package agentcreds_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The fixtures use fixed instants so every expiry assertion compares against a
// value computed by hand here, never against the parser's own output.
var (
	claudeExpiry = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	codexExpiry  = time.Date(2026, 8, 17, 13, 30, 0, 0, time.UTC)
	fixedNow     = time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
)

// jwtWithExp builds a signature-less JWT whose exp claim is at instant.
// The exp claim is seconds; the Claude fixture below uses milliseconds. The two
// units differing is the whole reason these tests exist.
func jwtWithExp(instant time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d,"sub":"user-fixture"}`, instant.Unix())))
	return header + "." + claims + ".not-a-real-signature"
}

func claudeFixture() []byte {
	return []byte(fmt.Sprintf(`{
	  "claudeAiOauth": {
	    "accessToken": "sk-ant-oat-fixture-access",
	    "refreshToken": "sk-ant-ort-fixture-refresh",
	    "expiresAt": %d,
	    "refreshTokenExpiresAt": %d,
	    "scopes": ["user:inference", "user:profile"],
	    "subscriptionType": "max",
	    "rateLimitTier": "default_claude_max_20x"
	  },
	  "mcpOAuth": {
	    "example-server|0123456789abcdef": {
	      "accessToken": "mcp-fixture-access",
	      "refreshToken": "mcp-fixture-refresh",
	      "clientSecret": "mcp-fixture-client-secret",
	      "serverName": "example-server"
	    }
	  }
	}`, claudeExpiry.UnixMilli(), claudeExpiry.Add(30*24*time.Hour).UnixMilli()))
}

func codexPlanFixture() []byte {
	return []byte(fmt.Sprintf(`{
	  "auth_mode": "chatgpt",
	  "OPENAI_API_KEY": null,
	  "tokens": {
	    "id_token": %q,
	    "access_token": %q,
	    "refresh_token": "codex-fixture-refresh",
	    "account_id": "00000000-0000-4000-8000-000000000000"
	  },
	  "last_refresh": "2026-08-17T10:30:00.000000000Z"
	}`, jwtWithExp(codexExpiry.Add(time.Hour)), jwtWithExp(codexExpiry)))
}

var _ = Describe("RedactClaude", func() {
	It("removes the refresh token and the whole mcpOAuth map", func() {
		credential, err := agentcreds.RedactClaude(claudeFixture())
		Expect(err).NotTo(HaveOccurred())

		var got map[string]any
		Expect(json.Unmarshal(credential.Payload, &got)).To(Succeed())
		Expect(got).To(HaveLen(1), "only claudeAiOauth survives redaction")
		Expect(got).To(HaveKey("claudeAiOauth"))
		Expect(got).NotTo(HaveKey("mcpOAuth"))

		oauth := got["claudeAiOauth"].(map[string]any)
		Expect(oauth).To(Equal(map[string]any{
			"accessToken":      "sk-ant-oat-fixture-access",
			"expiresAt":        float64(claudeExpiry.UnixMilli()),
			"scopes":           []any{"user:inference", "user:profile"},
			"subscriptionType": "max",
			"rateLimitTier":    "default_claude_max_20x",
		}))
	})

	It("never lets a secret survive anywhere in the payload", func() {
		credential, err := agentcreds.RedactClaude(claudeFixture())
		Expect(err).NotTo(HaveOccurred())
		for _, secret := range []string{
			"sk-ant-ort-fixture-refresh",
			"mcp-fixture-access",
			"mcp-fixture-refresh",
			"mcp-fixture-client-secret",
		} {
			Expect(string(credential.Payload)).NotTo(ContainSubstring(secret))
		}
	})

	It("reads expiresAt as epoch milliseconds", func() {
		credential, err := agentcreds.RedactClaude(claudeFixture())
		Expect(err).NotTo(HaveOccurred())
		Expect(credential.ExpiresAt).To(BeTemporally("==", claudeExpiry))
		Expect(credential.Provider).To(Equal(agentcreds.ProviderClaude))
		Expect(credential.Filename).To(Equal(agentcreds.ClaudeFilename))
		Expect(credential.RelPath()).To(Equal(".credentials.json"))
	})

	It("refuses a document with no access token", func() {
		_, err := agentcreds.RedactClaude([]byte(`{"claudeAiOauth":{"expiresAt":1}}`))
		Expect(err).To(MatchError(ContainSubstring("no claudeAiOauth.accessToken")))
	})

	It("refuses a document with no expiry, because a republish cannot be scheduled", func() {
		_, err := agentcreds.RedactClaude([]byte(`{"claudeAiOauth":{"accessToken":"x"}}`))
		Expect(err).To(MatchError(ContainSubstring("no claudeAiOauth.expiresAt")))
	})
})

var _ = Describe("RedactCodex", func() {
	It("blanks the refresh token but keeps the key present", func() {
		credential, err := agentcreds.RedactCodex(codexPlanFixture(), fixedNow)
		Expect(err).NotTo(HaveOccurred())

		var got struct {
			AuthMode     string  `json:"auth_mode"`
			OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
			Tokens       struct {
				IDToken      string  `json:"id_token"`
				AccessToken  string  `json:"access_token"`
				RefreshToken *string `json:"refresh_token"`
				AccountID    string  `json:"account_id"`
			} `json:"tokens"`
			LastRefresh string `json:"last_refresh"`
		}
		Expect(json.Unmarshal(credential.Payload, &got)).To(Succeed())

		Expect(got.AuthMode).To(Equal("chatgpt"))
		Expect(got.OpenAIAPIKey).To(BeNil(), "null must survive as null, not vanish")
		Expect(got.Tokens.AccessToken).To(Equal(jwtWithExp(codexExpiry)))
		Expect(got.Tokens.AccountID).To(Equal("00000000-0000-4000-8000-000000000000"))
		Expect(got.LastRefresh).To(Equal("2026-08-17T10:30:00.000000000Z"))

		Expect(got.Tokens.RefreshToken).NotTo(BeNil(),
			"codex-rs models refresh_token as a non-optional String; omitting the key fails deserialization")
		Expect(*got.Tokens.RefreshToken).To(BeEmpty())
		Expect(string(credential.Payload)).NotTo(ContainSubstring("codex-fixture-refresh"))
	})

	It("reads expiry from the access_token exp claim, in seconds", func() {
		credential, err := agentcreds.RedactCodex(codexPlanFixture(), fixedNow)
		Expect(err).NotTo(HaveOccurred())
		Expect(credential.ExpiresAt).To(BeTemporally("==", codexExpiry))
		Expect(credential.Provider).To(Equal(agentcreds.ProviderCodex))
		Expect(credential.RelPath()).To(Equal("auth.json"))
	})

	It("passes an API-key login through with a relative expiry", func() {
		credential, err := agentcreds.RedactCodex(
			[]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fixture-api-key"}`), fixedNow)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(credential.Payload)).To(ContainSubstring("sk-fixture-api-key"))
		Expect(credential.ExpiresAt).To(BeTemporally("==", fixedNow.Add(24*time.Hour)))
	})

	It("refuses a document carrying neither tokens nor an API key", func() {
		_, err := agentcreds.RedactCodex([]byte(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null}`), fixedNow)
		Expect(err).To(MatchError(ContainSubstring("run `codex login`")))
	})

	It("refuses an access token that is not a JWT, rather than guessing an expiry", func() {
		_, err := agentcreds.RedactCodex(
			[]byte(`{"tokens":{"access_token":"opaque-not-a-jwt","refresh_token":"r"}}`), fixedNow)
		Expect(err).To(MatchError(ContainSubstring("not a JWT")))
	})
})

var _ = Describe("Credential.Expired", func() {
	It("treats the expiry instant itself as expired", func() {
		credential := agentcreds.Credential{ExpiresAt: claudeExpiry}
		Expect(credential.Expired(claudeExpiry.Add(-time.Second))).To(BeFalse())
		Expect(credential.Expired(claudeExpiry)).To(BeTrue())
		Expect(credential.Expired(claudeExpiry.Add(time.Second))).To(BeTrue())
	})
})

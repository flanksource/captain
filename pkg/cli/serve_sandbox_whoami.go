package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

const agentWhoamiResponseLimit = 8 << 20

type agentWhoamiHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type agentWhoamiTarget struct {
	url   string
	token text.SensitiveString
}

func handleGitAgentWhoami() http.Handler {
	return handleGitAgentWhoamiWithClient(&http.Client{Timeout: 30 * time.Second})
}

func handleGitAgentWhoamiWithClient(client agentWhoamiHTTPClient) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalRequest(r, "agent inspection requests"); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if name == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		target, err := resolveAgentWhoamiTarget(sandboxBackendParam(r), name)
		if err != nil {
			http.Error(w, err.Error(), gitAgentWhoamiTargetStatus(err))
			return
		}
		result, err := requestAgentWhoami(r.Context(), client, target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

func resolveAgentWhoamiTarget(backendName, agentName string) (agentWhoamiTarget, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return agentWhoamiTarget{}, err
	}
	entry, err := enrolledAgent(cfg, backendName, agentName)
	if err != nil {
		return agentWhoamiTarget{}, err
	}
	endpoint, _ := entry["url"].(string)
	if gitagent.EndpointScheme(endpoint) != "https" {
		return agentWhoamiTarget{}, fmt.Errorf(
			"agent %q does not expose the HTTPS whoami endpoint", agentName)
	}
	whoamiURL, err := gitAgentWhoamiURL(endpoint)
	if err != nil {
		return agentWhoamiTarget{}, err
	}
	tokenPath, _ := entry["tokenPath"].(string)
	if strings.TrimSpace(tokenPath) == "" {
		return agentWhoamiTarget{}, fmt.Errorf("agent %q has no dispatch token", agentName)
	}
	token, err := gitagent.ReadTokenFile(tokenPath)
	if err != nil {
		return agentWhoamiTarget{}, fmt.Errorf("agent %q has an unreadable dispatch token", agentName)
	}
	return agentWhoamiTarget{url: whoamiURL, token: token}, nil
}

func gitAgentWhoamiURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("agent endpoint %q must be https://host/path", endpoint)
	}
	parsed.Path = gitagent.AgentWhoamiPath
	parsed.RawPath = ""
	parsed.RawQuery = url.Values{
		"disabled": {"true"},
		"limit":    {"0"},
		"models":   {"true"},
	}.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func requestAgentWhoami(ctx context.Context, client agentWhoamiHTTPClient, target agentWhoamiTarget) (WhoamiResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return WhoamiResult{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+target.token.Value())
	response, err := client.Do(request)
	if err != nil {
		return WhoamiResult{}, fmt.Errorf("agent whoami request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, agentWhoamiResponseLimit+1))
	if err != nil {
		return WhoamiResult{}, fmt.Errorf("read agent whoami response: %w", err)
	}
	if len(payload) > agentWhoamiResponseLimit {
		return WhoamiResult{}, fmt.Errorf("agent whoami response exceeds %d bytes", agentWhoamiResponseLimit)
	}
	if response.StatusCode != http.StatusOK {
		return WhoamiResult{}, fmt.Errorf("agent whoami returned %s: %s",
			response.Status, strings.TrimSpace(string(payload)))
	}
	var result WhoamiResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return WhoamiResult{}, fmt.Errorf("decode agent whoami response: %w", err)
	}
	return result, nil
}

func gitAgentWhoamiTargetStatus(err error) int {
	if strings.Contains(err.Error(), "is not enrolled") || strings.Contains(err.Error(), "has no enrolled agents") {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

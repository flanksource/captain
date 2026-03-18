package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func acquireGitHubToken(_ context.Context, _ GitHubTokenConfig) (*TokenResult, error) {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return &TokenResult{
			Provider: "github",
			EnvVars:  map[string]string{"GITHUB_TOKEN": token},
		}, nil
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return &TokenResult{
			Provider: "github",
			EnvVars:  map[string]string{"GITHUB_TOKEN": token},
		}, nil
	}

	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return nil, fmt.Errorf("no GITHUB_TOKEN/GH_TOKEN set and 'gh auth token' failed: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return nil, fmt.Errorf("'gh auth token' returned empty token")
	}
	return &TokenResult{
		Provider: "github",
		EnvVars:  map[string]string{"GITHUB_TOKEN": token},
	}, nil
}

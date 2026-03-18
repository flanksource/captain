package sandbox

import (
	"context"
	"fmt"
)

func acquireAzureToken(_ context.Context, config AzureTokenConfig) (*TokenResult, error) {
	if config.ClientID == "" || config.TenantID == "" {
		return nil, fmt.Errorf("azure requires clientID and tenantID")
	}

	envVars := map[string]string{
		"AZURE_CLIENT_ID": config.ClientID,
		"AZURE_TENANT_ID": config.TenantID,
	}
	if config.ClientSecret != "" {
		envVars["AZURE_CLIENT_SECRET"] = config.ClientSecret
	}

	return &TokenResult{
		Provider: "azure",
		EnvVars:  envVars,
	}, nil
}

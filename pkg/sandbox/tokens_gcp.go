package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2/google"
)

func acquireGCPToken(ctx context.Context, config GCPTokenConfig, credDir string) (*TokenResult, error) {
	credPath := config.Credentials
	if credPath == "" {
		credPath = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}
	if credPath == "" {
		return nil, fmt.Errorf("no GCP credentials path specified and GOOGLE_APPLICATION_CREDENTIALS not set")
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("reading GCP credentials file %s: %w", credPath, err)
	}

	creds, err := google.CredentialsFromJSON(ctx, data, "https://www.googleapis.com/auth/cloud-platform") //nolint:staticcheck
	if err != nil {
		return nil, fmt.Errorf("parsing GCP credentials: %w", err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("obtaining GCP token: %w", err)
	}

	adcFile := filepath.Join(credDir, "gcloud", "application_default_credentials.json")
	adcContent, err := json.MarshalIndent(map[string]string{
		"type":          "authorized_user",
		"access_token":  token.AccessToken,
		"token_type":    token.TokenType,
		"refresh_token": token.RefreshToken,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling GCP ADC: %w", err)
	}

	if err := atomicWriteFile(adcFile, adcContent, 0o600); err != nil {
		return nil, fmt.Errorf("writing GCP ADC file: %w", err)
	}

	envVars := map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": adcFile,
	}
	if config.Project != "" {
		envVars["CLOUDSDK_CORE_PROJECT"] = config.Project
	}

	expiry := token.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(1 * time.Hour)
	}

	return &TokenResult{
		Provider:   "gcp",
		EnvVars:    envVars,
		WritePaths: []string{filepath.Join(credDir, "gcloud")},
		Expiry:     expiry,
	}, nil
}

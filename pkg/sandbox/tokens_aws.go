package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func acquireAWSToken(ctx context.Context, config AWSTokenConfig, credDir string) (*TokenResult, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if config.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(config.Profile))
	}
	if config.Region != "" {
		opts = append(opts, awsconfig.WithRegion(config.Region))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	if config.AssumeRole != "" {
		stsClient := sts.NewFromConfig(cfg)
		cfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(stsClient, config.AssumeRole),
		)
	}

	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieving AWS credentials: %w", err)
	}

	credFile := filepath.Join(credDir, ".aws", "credentials")
	content := fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n",
		creds.AccessKeyID, creds.SecretAccessKey)
	if creds.SessionToken != "" {
		content += fmt.Sprintf("aws_session_token = %s\n", creds.SessionToken)
	}

	if err := atomicWriteFile(credFile, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("writing AWS credentials file: %w", err)
	}

	envVars := map[string]string{
		"AWS_SHARED_CREDENTIALS_FILE": credFile,
		"AWS_EC2_METADATA_DISABLED":   "true",
	}
	if config.Region != "" {
		envVars["AWS_DEFAULT_REGION"] = config.Region
	}

	expiry := creds.Expires
	if expiry.IsZero() {
		expiry = time.Now().Add(1 * time.Hour)
	}

	return &TokenResult{
		Provider:   "aws",
		EnvVars:    envVars,
		WritePaths: []string{filepath.Join(credDir, ".aws")},
		Expiry:     expiry,
	}, nil
}

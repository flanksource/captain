package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/credsync"
)

// CredentialsOptions are the flags shared by `captain sandbox credentials`
// subcommands. Each overrides the matching ~/.captain.yaml setting, so a
// destination can be tried once without editing the file first.
type CredentialsOptions struct {
	Providers []string      `flag:"provider" help:"Credential providers to publish (claude, codex). Defaults to every configured provider"`
	Directory string        `flag:"directory" help:"Publish into this host directory instead of the configured destinations"`
	Namespace string        `flag:"namespace" help:"Publish into a Kubernetes Secret in this namespace"`
	Secret    string        `flag:"secret" help:"Name of the Kubernetes Secret to publish into"`
	Context   string        `flag:"kube-context" help:"kubeconfig context for the Kubernetes destination"`
	Margin    time.Duration `flag:"refresh-margin" help:"How far before expiry to republish"`
}

// CredentialStatus reports what would be published and where, without
// publishing. It carries expiry and size, never a credential value.
type CredentialStatus struct {
	Provider  string    `json:"provider" pretty:"label=Provider"`
	Source    string    `json:"source" pretty:"label=Source"`
	Key       string    `json:"key" pretty:"label=Key"`
	ExpiresAt time.Time `json:"expiresAt" pretty:"label=Expires"`
	ExpiresIn string    `json:"expiresIn" pretty:"label=Expires in"`
	Expired   bool      `json:"expired" pretty:"label=Expired"`
	Targets   []string  `json:"targets,omitempty" pretty:"label=Targets"`
}

// RunCredentialsStatus reads each provider and reports its lifetime.
//
// A provider that cannot be read is reported as a row with its reason rather
// than aborting the command: seeing "codex: not logged in" beside a healthy
// claude row is the whole point of a status command.
func RunCredentialsStatus(ctx context.Context, opts CredentialsOptions) ([]CredentialStatus, error) {
	reader, err := agentcreds.OSReader()
	if err != nil {
		return nil, err
	}
	providers, err := resolveCredentialProviders(opts.Providers)
	if err != nil {
		return nil, err
	}
	targets, err := describeCredentialTargets(opts)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	rows := make([]CredentialStatus, 0, len(providers))
	for _, provider := range providers {
		row := CredentialStatus{
			Provider: string(provider),
			Source:   credentialSourceLabel(reader, provider),
			Targets:  targets,
		}
		credential, err := reader.Read(ctx, provider)
		if err != nil {
			row.Expired = true
			row.ExpiresIn = firstLine(err.Error())
			rows = append(rows, row)
			continue
		}
		row.Key = credential.Filename
		row.ExpiresAt = credential.ExpiresAt
		row.Expired = credential.Expired(now)
		row.ExpiresIn = time.Until(credential.ExpiresAt).Round(time.Second).String()
		rows = append(rows, row)
	}
	return rows, nil
}

// RunCredentialsSync publishes once, for deploy time and for debugging.
func RunCredentialsSync(ctx context.Context, opts CredentialsOptions) (credsync.Result, error) {
	publisher, err := buildCredentialPublisher(opts)
	if err != nil {
		return credsync.Result{}, err
	}
	return publisher.PublishOnce(ctx)
}

// buildCredentialPublisher assembles a publisher from the flags, falling back to
// ~/.captain.yaml when no destination is passed.
func buildCredentialPublisher(opts CredentialsOptions) (credsync.Publisher, error) {
	reader, err := agentcreds.OSReader()
	if err != nil {
		return credsync.Publisher{}, err
	}
	providers, err := resolveCredentialProviders(opts.Providers)
	if err != nil {
		return credsync.Publisher{}, err
	}

	saved, _, err := captainconfig.Load()
	if err != nil {
		return credsync.Publisher{}, err
	}
	margin := opts.Margin
	if margin == 0 {
		margin = saved.Credentials.RefreshMargin
	}

	targets, err := credentialTargetsFromFlags(opts)
	if err != nil {
		return credsync.Publisher{}, err
	}
	if len(targets) == 0 {
		targets, err = credentialTargetsFromConfig(saved.Credentials)
		if err != nil {
			return credsync.Publisher{}, err
		}
	}
	if len(targets) == 0 {
		configPath, _ := captainconfig.Path()
		return credsync.Publisher{}, fmt.Errorf(
			"no credential destination configured; pass --directory or --namespace, or add a credentials.publish entry to %s",
			configPath)
	}

	return credsync.Publisher{
		Reader:    reader,
		Providers: providers,
		Targets:   targets,
		Margin:    margin,
	}, nil
}

// credentialTargetsFromFlags builds the destinations named on the command line.
func credentialTargetsFromFlags(opts CredentialsOptions) ([]credsync.Target, error) {
	var targets []credsync.Target
	if directory := strings.TrimSpace(opts.Directory); directory != "" {
		resolved, err := captainconfig.CredentialPublish{Directory: directory}.ResolvedDirectory()
		if err != nil {
			return nil, err
		}
		targets = append(targets, credsync.DirectoryTarget{Path: resolved})
	}
	if namespace := strings.TrimSpace(opts.Namespace); namespace != "" {
		client, resolvedNamespace, err := kubernetesClient(kubeClientOptions{
			Context:   opts.Context,
			Namespace: namespace,
		})
		if err != nil {
			return nil, err
		}
		targets = append(targets, credsync.KubernetesTarget{
			Client: client, Namespace: resolvedNamespace, Secret: opts.Secret,
		})
	}
	return targets, nil
}

// credentialTargetsFromConfig builds the destinations declared in
// ~/.captain.yaml. A cluster that cannot be reached is an error rather than a
// skipped destination, so a supervisor never reports success having published
// to only some of its configured targets.
func credentialTargetsFromConfig(defaults captainconfig.CredentialDefaults) ([]credsync.Target, error) {
	if err := defaults.Validate(); err != nil {
		return nil, err
	}
	var targets []credsync.Target
	for _, publish := range defaults.Publish {
		if publish.Kubernetes != nil {
			client, namespace, err := kubernetesClient(kubeClientOptions{
				Context:   publish.Kubernetes.Context,
				Namespace: publish.Kubernetes.Namespace,
			})
			if err != nil {
				return nil, err
			}
			targets = append(targets, credsync.KubernetesTarget{
				Client: client, Namespace: namespace, Secret: publish.Kubernetes.Secret,
			})
			continue
		}
		directory, err := publish.ResolvedDirectory()
		if err != nil {
			return nil, err
		}
		targets = append(targets, credsync.DirectoryTarget{Path: directory})
	}
	return targets, nil
}

// describeCredentialTargets names the destinations for status output without
// requiring them to be reachable — status must still work when the cluster is
// unavailable, which is exactly when someone runs it.
func describeCredentialTargets(opts CredentialsOptions) ([]string, error) {
	var names []string
	if directory := strings.TrimSpace(opts.Directory); directory != "" {
		names = append(names, "directory "+directory)
	}
	if namespace := strings.TrimSpace(opts.Namespace); namespace != "" {
		names = append(names, fmt.Sprintf("secret %s/%s", namespace, credentialSecretName(opts.Secret)))
	}
	if len(names) > 0 {
		return names, nil
	}

	saved, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	for _, publish := range saved.Credentials.Publish {
		if publish.Kubernetes != nil {
			names = append(names, fmt.Sprintf("secret %s/%s",
				publish.Kubernetes.Namespace, credentialSecretName(publish.Kubernetes.Secret)))
			continue
		}
		names = append(names, "directory "+publish.Directory)
	}
	return names, nil
}

func credentialSecretName(name string) string {
	if strings.TrimSpace(name) == "" {
		return credsync.DefaultSecretName
	}
	return name
}

// resolveCredentialProviders parses the requested provider names, defaulting to
// every supported provider.
func resolveCredentialProviders(names []string) ([]agentcreds.Provider, error) {
	if len(names) == 0 {
		return agentcreds.Providers(), nil
	}
	providers := make([]agentcreds.Provider, 0, len(names))
	for _, name := range names {
		provider, err := agentcreds.ParseProvider(name)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// credentialSourceLabel names where the credential is being read from, which is
// the first thing to check when a provider reads as unavailable.
func credentialSourceLabel(reader agentcreds.Reader, provider agentcreds.Provider) string {
	if provider == agentcreds.ProviderCodex {
		return reader.CodexPath()
	}
	if reader.ReadKeychain != nil {
		return "keychain: Claude Code-credentials"
	}
	return reader.ClaudePath()
}

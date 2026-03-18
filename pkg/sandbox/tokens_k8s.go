package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func acquireK8sToken(_ context.Context, config K8sTokenConfig, credDir string) (*TokenResult, error) {
	loadRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	if config.Context != "" {
		configOverrides.CurrentContext = config.Context
	}

	kubeConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadRules, configOverrides).RawConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	contextName := config.Context
	if contextName == "" {
		contextName = kubeConfig.CurrentContext
	}
	if contextName == "" {
		return nil, fmt.Errorf("no kubernetes context specified and no current context set")
	}

	ctxEntry, ok := kubeConfig.Contexts[contextName]
	if !ok {
		return nil, fmt.Errorf("kubernetes context %q not found", contextName)
	}

	minimal := clientcmdapi.NewConfig()
	minimal.CurrentContext = contextName
	minimal.Contexts[contextName] = ctxEntry

	if cluster, ok := kubeConfig.Clusters[ctxEntry.Cluster]; ok {
		minimal.Clusters[ctxEntry.Cluster] = cluster
	}
	if authInfo, ok := kubeConfig.AuthInfos[ctxEntry.AuthInfo]; ok {
		minimal.AuthInfos[ctxEntry.AuthInfo] = authInfo
	}

	kubeconfigPath := filepath.Join(credDir, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(kubeconfigPath), 0o700); err != nil {
		return nil, fmt.Errorf("creating .kube dir: %w", err)
	}
	if err := clientcmd.WriteToFile(*minimal, kubeconfigPath); err != nil {
		return nil, fmt.Errorf("writing kubeconfig: %w", err)
	}

	return &TokenResult{
		Provider:   "kubernetes",
		EnvVars:    map[string]string{"KUBECONFIG": kubeconfigPath},
		WritePaths: []string{filepath.Join(credDir, ".kube")},
	}, nil
}

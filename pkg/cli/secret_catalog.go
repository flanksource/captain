package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/flanksource/captain/pkg/ai"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type secretResource struct {
	Name string   `json:"name"`
	Keys []string `json:"keys,omitempty"`
}

type secretKeyPreview struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func handleSecretResources() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, err := secretKindFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		client, namespace, err := kubernetesClient(kubeClientOptions{Namespace: r.URL.Query().Get("namespace")})
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		resources, err := listSecretResources(r.Context(), client, namespace, kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, resources)
	}
}

func handleSecretPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, err := secretKindFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		client, namespace, err := kubernetesClient(kubeClientOptions{Namespace: r.URL.Query().Get("namespace")})
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		previews, err := loadSecretPreview(r.Context(), client, namespace, kind, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, previews)
	}
}

func secretKindFromRequest(r *http.Request) (string, error) {
	switch kind := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("kind"))); kind {
	case "", "secret":
		return "secret", nil
	case "configmap":
		return "configmap", nil
	default:
		return "", fmt.Errorf("kind must be secret or configmap")
	}
}

// kubeClientOptions selects which cluster and namespace to act on. Both are
// optional; empty means "whatever the kubeconfig's current context says".
type kubeClientOptions struct {
	// Context names a kubeconfig context. Deploy sets it so an operator can see
	// and pin which cluster a sidecar lands in.
	Context string
	// Namespace overrides the context's own namespace.
	Namespace string
}

// kubernetesClient builds a client for the selected cluster, returning the
// resolved namespace alongside it.
//
// This is deliberately not commons-db's kubernetes.NewClient: that helper
// returns a fake in-memory clientset with a nil error when neither a kubeconfig
// nor in-cluster config resolves, so a write would silently succeed against
// nothing. Here an unresolvable cluster is an error.
func kubernetesClient(opts kubeClientOptions) (kubernetes.Interface, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: strings.TrimSpace(opts.Context)}
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	namespace, _, err := config.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("loading kube namespace: %w", err)
	}
	if namespace = strings.TrimSpace(namespace); namespace == "" {
		namespace = "default"
	}
	if override := strings.TrimSpace(opts.Namespace); override != "" {
		namespace = override
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, "", fmt.Errorf("creating kubernetes client: %w", err)
	}
	return client, namespace, nil
}

// kubernetesDynamicClient builds an untyped client for the selected cluster.
//
// cert-manager's types are not in client-go, and taking a dependency on its Go
// module to read one list of names would pull a whole API surface captain never
// otherwise touches.
func kubernetesDynamicClient(opts kubeClientOptions) (dynamic.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: strings.TrimSpace(opts.Context)}
	restConfig, err := clientcmd.
		NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).
		ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return client, nil
}

func listSecretResources(ctx context.Context, client kubernetes.Interface, namespace, kind string) ([]secretResource, error) {
	switch kind {
	case "secret":
		list, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing secrets: %w", err)
		}
		out := make([]secretResource, 0, len(list.Items))
		for _, item := range list.Items {
			out = append(out, secretResource{Name: item.Name, Keys: sortedByteMapKeys(item.Data)})
		}
		sortSecretResources(out)
		return out, nil
	case "configmap":
		list, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing configmaps: %w", err)
		}
		out := make([]secretResource, 0, len(list.Items))
		for _, item := range list.Items {
			out = append(out, secretResource{Name: item.Name, Keys: sortedConfigMapKeys(item)})
		}
		sortSecretResources(out)
		return out, nil
	default:
		return nil, fmt.Errorf("kind must be secret or configmap")
	}
}

func loadSecretPreview(ctx context.Context, client kubernetes.Interface, namespace, kind, name string) ([]secretKeyPreview, error) {
	switch kind {
	case "secret":
		item, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting secret %s: %w", name, err)
		}
		return previewsForByteData(item.Data), nil
	case "configmap":
		item, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting configmap %s: %w", name, err)
		}
		return previewsForConfigMap(*item), nil
	default:
		return nil, fmt.Errorf("kind must be secret or configmap")
	}
}

func previewsForByteData(data map[string][]byte) []secretKeyPreview {
	keys := sortedByteMapKeys(data)
	out := make([]secretKeyPreview, 0, len(keys))
	for _, key := range keys {
		out = append(out, secretKeyPreview{Key: key, Value: maskPreviewBytes(data[key])})
	}
	return out
}

func previewsForConfigMap(item corev1.ConfigMap) []secretKeyPreview {
	keys := sortedConfigMapKeys(item)
	out := make([]secretKeyPreview, 0, len(keys))
	for _, key := range keys {
		if value, ok := item.Data[key]; ok {
			out = append(out, secretKeyPreview{Key: key, Value: ai.MaskKey(value)})
			continue
		}
		out = append(out, secretKeyPreview{Key: key, Value: byteCountPreview(item.BinaryData[key])})
	}
	return out
}

func sortedByteMapKeys(data map[string][]byte) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedConfigMapKeys(item corev1.ConfigMap) []string {
	seen := map[string]struct{}{}
	for key := range item.Data {
		seen[key] = struct{}{}
	}
	for key := range item.BinaryData {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortSecretResources(resources []secretResource) {
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})
}

func maskPreviewBytes(value []byte) string {
	if !utf8.Valid(value) {
		return byteCountPreview(value)
	}
	return ai.MaskKey(string(value))
}

func byteCountPreview(value []byte) string {
	return fmt.Sprintf("%d bytes", len(value))
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

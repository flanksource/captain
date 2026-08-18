// The cluster resources the deploy form offers instead of asking an operator to
// recall a name.
//
// Every field here names something that must already exist in the cluster, and
// every one of them fails the same way when it is wrong: the deploy succeeds,
// the objects are created, and the mistake surfaces at the first push — an
// Ingress whose TLS Secret holds no certificate for the host, or an issuer
// annotation no controller answers to. A typed name is still accepted, because
// a list this cannot read is not proof the name is wrong.

package cli

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func registerSandboxPickerHandlers(mux *http.ServeMux) {
	mux.Handle("GET /api/captain/sandbox/git-agent/namespaces", handleGitAgentNamespaces())
	mux.Handle("GET /api/captain/sandbox/git-agent/secrets", handleGitAgentSecrets())
	mux.Handle("GET /api/captain/sandbox/git-agent/cluster-issuers", handleGitAgentClusterIssuers())
}

// handleGitAgentNamespaces lists the namespaces a kubernetes deploy could target.
//
// A bare JSON array, which is what clicky-ui's NamespacePicker loadNamespaces
// getter consumes. Failures are reported as failures rather than as an empty
// cluster — "no namespaces" and "no kubeconfig" are different answers, and the
// form's picker allows a typed value either way.
func handleGitAgentNamespaces() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), preflightTimeout)
		defer cancel()
		names, err := listKubernetesNamespaces(ctx, r.URL.Query().Get("kubeContext"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeServeJSON(w, http.StatusOK, names)
	})
}

func listKubernetesNamespaces(ctx context.Context, kubeContext string) ([]string, error) {
	client, _, err := kubernetesClient(kubeClientOptions{Context: strings.TrimSpace(kubeContext)})
	if err != nil {
		return nil, err
	}
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names, nil
}

// handleGitAgentSecrets lists the Secrets a deploy could name.
//
// `type` narrows to one Secret type, which is what makes the certificate field
// a picker rather than a text box: a Secret that is not kubernetes.io/tls
// cannot serve the agent's host, and the Ingress would be created pointing at
// it anyway. Empty lists them all, for the agent-login field.
func handleGitAgentSecrets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), preflightTimeout)
		defer cancel()
		query := r.URL.Query()
		names, err := listKubernetesSecrets(ctx, kubeClientOptions{
			Context: strings.TrimSpace(query.Get("kubeContext")),
			// Empty falls through to the kubeconfig context's own namespace,
			// which is the same default the form's namespace field shows.
			Namespace: strings.TrimSpace(query.Get("namespace")),
		}, strings.TrimSpace(query.Get("type")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeServeJSON(w, http.StatusOK, names)
	})
}

func listKubernetesSecrets(ctx context.Context, opts kubeClientOptions, secretType string) ([]string, error) {
	client, namespace, err := kubernetesClient(opts)
	if err != nil {
		return nil, err
	}
	list, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing secrets in %s: %w", namespace, err)
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if secretType != "" && string(item.Type) != secretType {
			continue
		}
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names, nil
}

// handleGitAgentClusterIssuers lists the cert-manager issuers that could mint
// the agent's certificate.
//
// A name no ClusterIssuer answers to leaves the Ingress with an annotation
// nothing acts on: the controller serves its own default certificate and the
// supervisor's first push fails verification, long after the deploy reported
// success. Empty means cert-manager is absent or unreadable, and the field
// still accepts a typed name.
func handleGitAgentClusterIssuers() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), preflightTimeout)
		defer cancel()
		names, err := listClusterIssuers(ctx, strings.TrimSpace(r.URL.Query().Get("kubeContext")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeServeJSON(w, http.StatusOK, names)
	})
}

// clusterIssuerResource is cert-manager's cluster-scoped issuer.
var clusterIssuerResource = schema.GroupVersionResource{
	Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers",
}

func listClusterIssuers(ctx context.Context, kubeContext string) ([]string, error) {
	client, err := kubernetesDynamicClient(kubeClientOptions{Context: kubeContext})
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(clusterIssuerResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		// A cluster without cert-manager has no such resource, which is a fact
		// about the cluster rather than a failure: certManagerInstalled already
		// tells the form to steer towards an existing Secret.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || apierrors.IsForbidden(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing cluster issuers: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	sort.Strings(names)
	return names, nil
}

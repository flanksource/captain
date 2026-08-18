package deploy

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var TraefikServersTransportResource = schema.GroupVersionResource{
	Group: "traefik.io", Version: "v1alpha1", Resource: "serverstransports",
}

func (p Plan) UsesTraefik() bool {
	return p.HasExternalRoute() && p.ExternalRoute.ClassName == "traefik"
}

func (p Plan) TraefikServersTransport(namespace string) *unstructured.Unstructured {
	labels := map[string]any{}
	for key, value := range p.Labels() {
		labels[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "ServersTransport",
		"metadata": map[string]any{
			"name":      p.WorkloadName(),
			"namespace": namespace,
			"labels":    labels,
		},
		"spec": map[string]any{
			"serverName": p.ExternalRoute.Host,
			"rootCAs": []any{
				map[string]any{"secret": p.IngressTLSSecretName()},
			},
		},
	}}
}

func ApplyTraefikServersTransport(
	ctx context.Context, client dynamic.Interface, plan Plan, namespace string,
) error {
	if client == nil {
		return fmt.Errorf("apply Traefik ServersTransport: dynamic Kubernetes client is nil")
	}
	resources := client.Resource(TraefikServersTransportResource).Namespace(namespace)
	desired := plan.TraefikServersTransport(namespace)
	current, err := resources.Get(ctx, plan.WorkloadName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resources.Create(ctx, desired, metav1.CreateOptions{FieldManager: FieldManager})
	} else if err == nil {
		desired.SetResourceVersion(current.GetResourceVersion())
		_, err = resources.Update(ctx, desired, metav1.UpdateOptions{FieldManager: FieldManager})
	}
	if err != nil {
		return fmt.Errorf("apply Traefik ServersTransport: %w", err)
	}
	return nil
}

func DeleteTraefikServersTransport(
	ctx context.Context, client dynamic.Interface, plan Plan, namespace string,
) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("delete Traefik ServersTransport: dynamic Kubernetes client is nil")
	}
	resources := client.Resource(TraefikServersTransportResource).Namespace(namespace)
	current, err := resources.Get(ctx, plan.WorkloadName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get Traefik ServersTransport: %w", err)
	}
	for key, want := range plan.Labels() {
		if got := current.GetLabels()[key]; got != want {
			return false, fmt.Errorf(
				"refusing to delete Traefik ServersTransport %s: label %s is %q, want %q",
				plan.WorkloadName(), key, got, want)
		}
	}
	if err := resources.Delete(ctx, plan.WorkloadName(), metav1.DeleteOptions{}); err != nil {
		return false, fmt.Errorf("delete Traefik ServersTransport: %w", err)
	}
	return true, nil
}

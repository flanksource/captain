package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// applyOptions force-owns the fields this package sets. Force resolves a
// conflict with a previous manager in our favour rather than erroring, which is
// what makes a re-deploy converge on the declared spec.
func applyOptions() metav1.ApplyOptions {
	return metav1.ApplyOptions{FieldManager: FieldManager, Force: true}
}

// requiredPermissions is what a deploy needs, checked before it starts.
//
// Applying four objects — five with an external route — is not transactional: failing on the third leaves a
// Secret and a PVC behind and an enrollment already recorded. A
// SelfSubjectAccessReview turns that into one refusal up front.
var requiredPermissions = []struct{ group, resource, verb string }{
	{"apps", "deployments", "create"},
	{"apps", "deployments", "patch"},
	{"", "services", "create"},
	{"", "services", "patch"},
	{"", "secrets", "create"},
	{"", "secrets", "delete"},
	// Server-side apply is a PATCH against a possibly-absent object, so the
	// credential publisher needs get and patch as well as create. Checking them
	// here means a namespace that cannot host the republish loop is refused at
	// deploy time rather than at the supervisor's first publish.
	{"", "secrets", "get"},
	{"", "secrets", "patch"},
	{"", "persistentvolumeclaims", "create"},
	{"", "pods", "list"},
}

// ingressPermissions are checked only when a route is rendered.
//
// A namespace that can host the in-cluster topology and not the externally
// routed one is a legitimate configuration, and demanding ingress rights there
// would refuse a deploy that would have succeeded. Delete is checked at DEPLOY
// time on purpose: teardown running with rights the deploy never verified is how
// a sidecar ends up unremovable and still routed.
var ingressPermissions = []struct{ group, resource, verb string }{
	{"networking.k8s.io", "ingresses", "create"},
	{"networking.k8s.io", "ingresses", "patch"},
	{"networking.k8s.io", "ingresses", "delete"},
}

var traefikPermissions = []struct{ group, resource, verb string }{
	{"traefik.io", "serverstransports", "get"},
	{"traefik.io", "serverstransports", "create"},
	{"traefik.io", "serverstransports", "update"},
	{"traefik.io", "serverstransports", "delete"},
}

// EnsureNamespace makes the target namespace exist, or refuses before anything
// is applied into it.
//
// Every object a deploy creates is namespaced, so a namespace that is not there
// fails on the first apply with an error naming neither the cause nor the fix —
// the same reason CheckPermissions runs up front. Creating one is opt-in: a
// typo'd namespace that silently appears is a cluster-scoped side effect nobody
// asked for, and nothing later would report it.
//
// It returns whether it created the namespace, so the caller can report it.
func EnsureNamespace(ctx context.Context, client kubernetes.Interface, namespace string, create bool) (bool, error) {
	_, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	switch {
	case err == nil:
		return false, nil
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("checking namespace %q: %w", namespace, err)
	case !create:
		return false, fmt.Errorf(
			"namespace %q does not exist in this cluster; create it, or pass --create-namespace", namespace)
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "captain"},
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Another deploy won the race, or it appeared between the two calls. The
		// postcondition — the namespace exists — is met either way.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("creating namespace %q: %w", namespace, err)
	}
	return true, nil
}

// CheckPermissions refuses a deploy the caller is not allowed to complete.
func CheckPermissions(ctx context.Context, client kubernetes.Interface, namespace, ingressClass string) error {
	var denied []string
	permissions := requiredPermissions
	if ingressClass != "" {
		permissions = append(append([]struct{ group, resource, verb string }{}, permissions...), ingressPermissions...)
	}
	if ingressClass == "traefik" {
		permissions = append(permissions, traefikPermissions...)
	}
	for _, permission := range permissions {
		review := &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authv1.ResourceAttributes{
					Namespace: namespace,
					Group:     permission.group,
					Resource:  permission.resource,
					Verb:      permission.verb,
				},
			},
		}
		result, err := client.AuthorizationV1().SelfSubjectAccessReviews().
			Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			// The cluster may not serve SSAR to this identity. That is not itself a
			// denial, and refusing here would block a deploy that would succeed.
			return nil //nolint:nilerr // absence of the check is not a failed check
		}
		if !result.Status.Allowed {
			denied = append(denied, fmt.Sprintf("%s %s", permission.verb, resourceLabel(permission.group, permission.resource)))
		}
	}
	if len(denied) > 0 {
		return fmt.Errorf("missing permission in namespace %q: %s", namespace, strings.Join(denied, ", "))
	}
	return nil
}

func resourceLabel(group, resource string) string {
	if group == "" {
		return resource
	}
	return group + "/" + resource
}

// KubernetesApply creates or converges every object for one sidecar, in
// dependency order: the token and its volume exist before the pod that mounts
// them.
func KubernetesApply(ctx context.Context, client kubernetes.Interface, plan Plan, opts KubernetesOptions) ([]string, error) {
	if err := plan.ValidateJoinPath(); err != nil {
		return nil, err
	}
	namespace := opts.Namespace
	applied := []string{}

	if opts.JoinToken != "" {
		if _, err := client.CoreV1().Secrets(namespace).
			Apply(ctx, plan.JoinSecret(namespace, opts.JoinToken), applyOptions()); err != nil {
			return applied, fmt.Errorf("apply join secret: %w", err)
		}
		applied = append(applied, "Secret/"+plan.JoinSecretName())
	}

	// A PVC's storage request is immutable once bound, so a re-deploy asking for
	// a different size is rejected by the API server. Report what to do rather
	// than leaving the operator with a field-immutable error.
	if _, err := client.CoreV1().PersistentVolumeClaims(namespace).
		Apply(ctx, plan.StateClaim(namespace, opts.StorageClass), applyOptions()); err != nil {
		if apierrors.IsInvalid(err) {
			return applied, fmt.Errorf(
				"the state volume %s already exists with different settings; resize it with kubectl, or run undeploy --purge first: %w",
				plan.VolumeName(), err)
		}
		return applied, fmt.Errorf("apply state volume: %w", err)
	}
	applied = append(applied, "PersistentVolumeClaim/"+plan.VolumeName())

	if _, err := client.CoreV1().Services(namespace).
		Apply(ctx, plan.Service(namespace), applyOptions()); err != nil {
		return applied, fmt.Errorf("apply service: %w", err)
	}
	applied = append(applied, "Service/"+plan.WorkloadName())

	if _, err := client.AppsV1().Deployments(namespace).
		Apply(ctx, plan.Deployment(namespace, opts.ImagePullPolicy, opts.ImagePullSecret), applyOptions()); err != nil {
		return applied, fmt.Errorf("apply deployment: %w", err)
	}
	applied = append(applied, "Deployment/"+plan.WorkloadName())

	if !plan.HasExternalRoute() {
		return applied, nil
	}
	// Last, after the Service it names. A controller reconciling an Ingress whose
	// backend Service does not exist logs an endpoint-not-found and, for some
	// controllers, caches that until the next resync — a route that starts
	// working only after a delay nobody can attribute.
	if _, err := client.NetworkingV1().Ingresses(namespace).
		Apply(ctx, plan.Ingress(namespace), applyOptions()); err != nil {
		return applied, fmt.Errorf("apply ingress: %w", err)
	}
	return append(applied, "Ingress/"+plan.IngressName()), nil
}

// KubernetesOptions are the cluster-side choices a Plan does not carry.
type KubernetesOptions struct {
	Namespace       string
	StorageClass    string
	ImagePullPolicy string
	ImagePullSecret string
	JoinToken       string
}

// KubernetesReady blocks until the Deployment reports a ready replica.
func KubernetesReady(ctx context.Context, client kubernetes.Interface, plan Plan, namespace string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		deployment, err := client.AppsV1().Deployments(namespace).
			Get(ctx, plan.WorkloadName(), metav1.GetOptions{})
		if err == nil && deployment.Status.ReadyReplicas >= 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("deployment %s did not become ready: %w", plan.WorkloadName(), ctx.Err())
		case <-ticker.C:
		}
	}
}

// KubernetesLogs returns the sidecar pod's recent output, so a timeout says why.
func KubernetesLogs(ctx context.Context, client kubernetes.Interface, plan Plan, namespace string, lines int64) string {
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: plan.Labels()}),
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	raw, err := client.CoreV1().Pods(namespace).
		GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: containerName, TailLines: &lines}).
		DoRaw(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// DeleteJoinSecret removes the token once enrollment has landed, so a spent
// credential does not sit in etcd for the life of the deployment.
func DeleteJoinSecret(ctx context.Context, client kubernetes.Interface, plan Plan, namespace string) error {
	err := client.CoreV1().Secrets(namespace).Delete(ctx, plan.JoinSecretName(), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// KubernetesRemove tears the sidecar down. The state volume goes only when
// asked: it holds the agent's private key, so deleting it is what makes the
// identity unrecoverable rather than merely stopped.
func KubernetesRemove(ctx context.Context, client kubernetes.Interface, plan Plan, namespace string, purgeVolume bool) ([]string, error) {
	removed := []string{}
	ignoreMissing := func(err error) error {
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// First, and unconditionally. First because while the route exists the
	// controller keeps sending pushes to a Service whose endpoints are draining,
	// and a 502 mid-teardown is harder to read than a name that has stopped
	// resolving. Unconditionally because undeploy reconstructs a Plan from a name
	// and a target and cannot know whether a route was rendered — deleting by the
	// derived name and ignoring NotFound is the only form that covers both
	// topologies.
	if err := ignoreMissing(client.NetworkingV1().Ingresses(namespace).
		Delete(ctx, plan.IngressName(), metav1.DeleteOptions{})); err != nil {
		return removed, fmt.Errorf("delete ingress: %w", err)
	}
	removed = append(removed, "Ingress/"+plan.IngressName())

	if err := ignoreMissing(client.AppsV1().Deployments(namespace).
		Delete(ctx, plan.WorkloadName(), metav1.DeleteOptions{})); err != nil {
		return removed, fmt.Errorf("delete deployment: %w", err)
	}
	removed = append(removed, "Deployment/"+plan.WorkloadName())

	if err := ignoreMissing(client.CoreV1().Services(namespace).
		Delete(ctx, plan.WorkloadName(), metav1.DeleteOptions{})); err != nil {
		return removed, fmt.Errorf("delete service: %w", err)
	}
	removed = append(removed, "Service/"+plan.WorkloadName())

	if err := DeleteJoinSecret(ctx, client, plan, namespace); err != nil {
		return removed, fmt.Errorf("delete join secret: %w", err)
	}
	removed = append(removed, "Secret/"+plan.JoinSecretName())

	if !purgeVolume {
		return removed, nil
	}
	if err := ignoreMissing(client.CoreV1().PersistentVolumeClaims(namespace).
		Delete(ctx, plan.VolumeName(), metav1.DeleteOptions{})); err != nil {
		return removed, fmt.Errorf("delete state volume: %w", err)
	}
	removed = append(removed, "PersistentVolumeClaim/"+plan.VolumeName())

	// The certificate goes with the state volume rather than with the route.
	// Let's Encrypt rate-limits duplicate certificates to five per week, so
	// deleting it on every teardown turns the sixth redeploy of the same agent
	// into a certificate that cannot be issued — and that surfaces as a TLS
	// failure on the supervisor's first push, not at deploy time. cert-manager
	// reuses the Secret when the Ingress comes back.
	//
	// Only the derived name: an operator-supplied Secret may be a wildcard shared
	// with every other agent on the domain.
	derived := plan.WorkloadName() + "-tls"
	if err := ignoreMissing(client.CoreV1().Secrets(namespace).
		Delete(ctx, derived, metav1.DeleteOptions{})); err != nil {
		return removed, fmt.Errorf("delete route certificate: %w", err)
	}
	return append(removed, "Secret/"+derived), nil
}

package credsync

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/agentcreds"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreapply "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
)

// FieldManager owns the fields this package sets under server-side apply.
//
// Deliberately distinct from the git-agent deploy manager
// (deploy.FieldManager, "captain-sandbox-git-agent"): the two write different
// Secrets in the same namespace, and sharing a manager name would make each
// one's apply look like it should prune the other's fields.
const FieldManager = "captain-credentials"

// DefaultSecretName is the Secret credentials are published to when a target
// does not name one.
const DefaultSecretName = "captain-agent-credentials"

// KubernetesTarget publishes credentials into a Secret that agent workloads
// mount. Unlike the git-agent join Secret it is mutable by design — being
// re-written before the access token expires is its entire purpose.
type KubernetesTarget struct {
	Client    kubernetes.Interface
	Namespace string
	Secret    string
}

func (t KubernetesTarget) Name() string {
	return fmt.Sprintf("secret %s/%s", t.Namespace, t.secretName())
}

func (t KubernetesTarget) secretName() string {
	if t.Secret == "" {
		return DefaultSecretName
	}
	return t.Secret
}

// Publish converges the Secret onto the current credentials.
//
// Server-side apply with a dedicated field manager means a republish updates
// the keys this package owns and leaves anything an operator added — extra
// keys, annotations — alone, rather than failing on AlreadyExists or silently
// replacing the whole object.
func (t KubernetesTarget) Publish(ctx context.Context, credentials []agentcreds.Credential) error {
	if t.Client == nil {
		return fmt.Errorf("kubernetes credential target has no client")
	}
	if t.Namespace == "" {
		return fmt.Errorf("kubernetes credential target has no namespace")
	}
	data := make(map[string][]byte, len(credentials))
	for _, credential := range credentials {
		data[credential.Filename] = credential.Payload
	}
	apply := coreapply.Secret(t.secretName(), t.Namespace).
		WithLabels(map[string]string{
			"app.kubernetes.io/managed-by": FieldManager,
		}).
		WithType(corev1.SecretTypeOpaque).
		WithData(data)

	if _, err := t.Client.CoreV1().Secrets(t.Namespace).Apply(ctx, apply, metav1.ApplyOptions{
		FieldManager: FieldManager,
		Force:        true,
	}); err != nil {
		return fmt.Errorf("apply credential secret %s/%s: %w", t.Namespace, t.secretName(), err)
	}
	return nil
}

package deploy_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// credentialSecretFixture stands in for the Secret the publisher maintains.
func credentialSecretFixture() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "captain-agent-credentials", Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"claude.credentials.json": []byte(`{"claudeAiOauth":{}}`)},
	}
}

// credentialsPlan is a kubernetes plan that mounts the shared login Secret.
func credentialsPlan() deploy.Plan {
	plan := kubernetesPlan()
	plan.CredentialsSecret = "captain-agent-credentials"
	return plan
}

var _ = Describe("credential mount", func() {
	Describe("kubernetes", func() {
		It("mounts the Secret as a directory, never through subPath", func() {
			// A subPath Secret mount is never refreshed by kubelet after the pod
			// starts, so it would pin the sidecar to the first credential it ever
			// saw and silently defeat the supervisor's republish loop.
			mounts := dig(asJSON(credentialsPlan().Deployment(testNamespace, "IfNotPresent", "")),
				"spec", "template", "spec", "containers").([]any)
			Expect(mounts).To(HaveLen(1))

			var found map[string]any
			for _, raw := range mounts[0].(map[string]any)["volumeMounts"].([]any) {
				mount := raw.(map[string]any)
				if mount["name"] == "credentials" {
					found = mount
				}
			}
			Expect(found).NotTo(BeNil(), "no credentials volume mount")
			Expect(found["mountPath"]).To(Equal(deploy.CredentialsMountPath))
			Expect(found["readOnly"]).To(BeTrue())
			Expect(found).NotTo(HaveKey("subPath"))
		})

		It("marks the volume optional so the pod starts before the first publish", func() {
			volumes := dig(asJSON(credentialsPlan().Deployment(testNamespace, "IfNotPresent", "")),
				"spec", "template", "spec", "volumes").([]any)

			var secret map[string]any
			for _, raw := range volumes {
				volume := raw.(map[string]any)
				if volume["name"] == "credentials" {
					secret = volume["secret"].(map[string]any)
				}
			}
			Expect(secret).NotTo(BeNil(), "no credentials volume")
			Expect(secret["secretName"]).To(Equal("captain-agent-credentials"))
			Expect(secret["optional"]).To(BeTrue())
			Expect(secret["defaultMode"]).To(BeEquivalentTo(0o400))
		})

		It("adds no volume at all when no credential Secret is named", func() {
			rendered := asJSON(kubernetesPlan().Deployment(testNamespace, "IfNotPresent", ""))
			volumes := dig(rendered, "spec", "template", "spec", "volumes").([]any)
			for _, raw := range volumes {
				Expect(raw.(map[string]any)["name"]).NotTo(Equal("credentials"))
			}
		})
	})

	Describe("docker", func() {
		It("bind-mounts the published directory read-only", func() {
			plan := dockerPlan()
			plan.CredentialsDir = "/host/captain/credentials"
			args := strings.Join(deploy.DockerArgs(plan, "/host/join"), " ")
			Expect(args).To(ContainSubstring(
				"--volume /host/captain/credentials:" + deploy.CredentialsMountPath + ":ro"))
		})

		It("mounts nothing when no directory is given", func() {
			args := strings.Join(deploy.DockerArgs(dockerPlan(), "/host/join"), " ")
			Expect(args).NotTo(ContainSubstring(deploy.CredentialsMountPath))
		})
	})

	Describe("undeploy", func() {
		It("leaves the shared credential Secret in place", func() {
			// The Secret is owned by the credential publisher and shared by every
			// agent in the namespace, so tearing one sidecar down must not take
			// every other sidecar's login with it.
			plan := credentialsPlan()
			client := fake.NewClientset()
			ctx := context.Background()

			_, err := client.CoreV1().Secrets(testNamespace).Create(ctx, credentialSecretFixture(), metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			removed, err := deploy.KubernetesRemove(ctx, client, plan, testNamespace, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(removed).NotTo(ContainElement(ContainSubstring(plan.CredentialsSecret)))

			_, err = client.CoreV1().Secrets(testNamespace).
				Get(ctx, plan.CredentialsSecret, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred(), "undeploy deleted the shared credential Secret")
		})
	})
})

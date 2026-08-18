package deploy_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

var _ = Describe("EnsureNamespace", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	It("accepts an existing namespace without creating anything", func() {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
		})

		created, err := deploy.EnsureNamespace(ctx, client, testNamespace, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
	})

	// Every object the deploy applies is namespaced, so a missing namespace fails
	// on the first apply with an error that names neither the cause nor the fix.
	// Refusing up front is the same reason CheckPermissions runs before applying.
	It("refuses a missing namespace and names the flag that would create it", func() {
		client := fake.NewSimpleClientset()

		_, err := deploy.EnsureNamespace(ctx, client, "absent", false)
		Expect(err).To(MatchError(ContainSubstring("--create-namespace")))
		Expect(err).To(MatchError(ContainSubstring("absent")))
	})

	It("creates a missing namespace when asked, labelled as captain's", func() {
		client := fake.NewSimpleClientset()

		created, err := deploy.EnsureNamespace(ctx, client, "agents-2", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		namespace, err := client.CoreV1().Namespaces().Get(ctx, "agents-2", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(namespace.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "captain"))
	})

	// Two deploys racing, or a namespace created between the check and the
	// create: the second must converge rather than fail on AlreadyExists.
	It("treats a namespace that appeared concurrently as created", func() {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "agents-3"},
		})

		created, err := deploy.EnsureNamespace(ctx, client, "agents-3", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
	})
})

package deploy_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// resourcesTouched lists the resource each recorded action addressed, in order.
func resourcesTouched(actions []k8stesting.Action, verb string) []string {
	var touched []string
	for _, action := range actions {
		if action.GetVerb() == verb {
			touched = append(touched, action.GetResource().Resource)
		}
	}
	return touched
}

var _ = Describe("KubernetesApply", func() {
	options := deploy.KubernetesOptions{
		Namespace: testNamespace, ImagePullPolicy: "IfNotPresent", JoinToken: "cptn_x.y",
	}

	// A controller reconciling an Ingress whose backend Service does not exist
	// logs endpoint-not-found and, for some controllers, caches that until the
	// next resync — a route that works only after a delay nobody can attribute.
	It("applies the route after the Service it names", func() {
		client := fake.NewClientset()

		applied, err := deploy.KubernetesApply(context.Background(), client, routedPlan(), options)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(HaveLen(5))
		Expect(applied[len(applied)-1]).To(Equal("Ingress/captain-git-agent-worker-01"))

		touched := resourcesTouched(client.Actions(), "patch")
		Expect(touched).To(Equal([]string{
			"secrets", "persistentvolumeclaims", "services", "deployments", "ingresses",
		}))
	})

	It("applies no route for the in-cluster topology", func() {
		client := fake.NewClientset()

		applied, err := deploy.KubernetesApply(context.Background(), client, kubernetesPlan(), options)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(HaveLen(4))
		Expect(resourcesTouched(client.Actions(), "patch")).NotTo(ContainElement("ingresses"))
	})

	It("recreates the workload without a join secret when its enrollment is persisted on the state volume", func() {
		client := fake.NewClientset()
		reuse := options
		reuse.JoinToken = ""
		plan := kubernetesPlan()
		plan.JoinPath = ""

		applied, err := deploy.KubernetesApply(context.Background(), client, plan, reuse)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(Equal([]string{
			"PersistentVolumeClaim/captain-git-agent-worker-01-state",
			"Service/captain-git-agent-worker-01",
			"Deployment/captain-git-agent-worker-01",
		}))
		Expect(resourcesTouched(client.Actions(), "patch")).NotTo(ContainElement("secrets"))
		deployment, err := client.AppsV1().Deployments(testNamespace).
			Get(context.Background(), plan.WorkloadName(), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		container := deployment.Spec.Template.Spec.Containers[0]
		Expect(container.Args).NotTo(ContainElement("--token-file"))
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			Expect(volume.Name).NotTo(Equal("join"))
		}
	})
})

var _ = Describe("Traefik ServersTransport lifecycle", func() {
	It("applies and removes the verified transport by its derived name", func() {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{
				deploy.TraefikServersTransportResource: "ServersTransportList",
			})
		plan := routedPlan()
		plan.ExternalRoute.ClassName = "traefik"

		Expect(deploy.ApplyTraefikServersTransport(
			context.Background(), client, plan, testNamespace)).To(Succeed())
		transport, err := client.Resource(deploy.TraefikServersTransportResource).
			Namespace(testNamespace).Get(context.Background(), plan.WorkloadName(), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(transport.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "captain"))

		removed, err := deploy.DeleteTraefikServersTransport(
			context.Background(), client, plan, testNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeTrue())
		_, err = client.Resource(deploy.TraefikServersTransportResource).
			Namespace(testNamespace).Get(context.Background(), plan.WorkloadName(), metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("KubernetesRemove", func() {
	// undeploy rebuilds a Plan from a name, a backend and a target, so it cannot
	// know a route was rendered. Deleting by the derived name and ignoring
	// NotFound is the only form that covers both topologies.
	It("deletes the route for a plan that does not know it had one", func() {
		client := fake.NewClientset()
		bare := deploy.Plan{Name: "worker-01", Backend: "git-agent", Target: deploy.TargetKubernetes}

		removed, err := deploy.KubernetesRemove(context.Background(), client, bare, testNamespace, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(ContainElement("Ingress/captain-git-agent-worker-01"))
		deleted := resourcesTouched(client.Actions(), "delete")
		Expect(deleted).NotTo(BeEmpty())
		Expect(deleted[0]).To(Equal("ingresses"),
			"the route must go first, or the controller keeps sending pushes to draining endpoints")
	})

	// Let's Encrypt rate-limits duplicate certificates to five per week, so
	// deleting it on every teardown turns the sixth redeploy of the same agent
	// into a certificate that cannot be issued.
	It("retains the route certificate unless purging", func() {
		client := fake.NewClientset()
		plan := routedPlan()

		removed, err := deploy.KubernetesRemove(context.Background(), client, plan, testNamespace, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).NotTo(ContainElement("Secret/captain-git-agent-worker-01-tls"))

		purged, err := deploy.KubernetesRemove(context.Background(), client, plan, testNamespace, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(purged).To(ContainElement("Secret/captain-git-agent-worker-01-tls"))
	})

	// Teardown deletes what it derives, never an operator's shared wildcard —
	// removing that would take every other agent on the domain offline.
	It("never deletes an operator-supplied certificate", func() {
		client := fake.NewClientset()
		plan := routedPlan()
		plan.ExternalRoute.ClusterIssuer = ""
		plan.ExternalRoute.TLSSecret = "wildcard-agents"

		purged, err := deploy.KubernetesRemove(context.Background(), client, plan, testNamespace, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(purged).NotTo(ContainElement("Secret/wildcard-agents"))

		_, err = client.CoreV1().Secrets(testNamespace).Get(
			context.Background(), "wildcard-agents", metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the fake never held it; the point is no delete was issued")
		for _, action := range client.Actions() {
			if action.GetVerb() == "delete" && action.GetResource().Resource == "secrets" {
				deleted := action.(k8stesting.DeleteAction).GetName()
				Expect(deleted).NotTo(Equal("wildcard-agents"))
			}
		}
	})
})

// reviewingClient answers every SelfSubjectAccessReview with allowed, and
// records what was asked about. The bare fake returns an error instead, which
// CheckPermissions treats as "the cluster does not serve SSAR" and skips.
func reviewingClient(allowed bool) (*fake.Clientset, *[]authv1.ResourceAttributes) {
	client := fake.NewClientset()
	asked := &[]authv1.ResourceAttributes{}
	client.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			review := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			*asked = append(*asked, *review.Spec.ResourceAttributes)
			review.Status.Allowed = allowed
			return true, review, nil
		})
	return client, asked
}

func askedAbout(attributes []authv1.ResourceAttributes, resource string) bool {
	for _, attribute := range attributes {
		if attribute.Resource == resource {
			return true
		}
	}
	return false
}

var _ = Describe("CheckPermissions", func() {
	// A namespace that can host the in-cluster topology and not the externally
	// routed one is legitimate, so ingress rights are demanded only when a route
	// is actually rendered — demanding them always would refuse a deploy that
	// would have succeeded.
	It("asks for ingress rights only when a route is rendered", func() {
		withRoute, routeAsked := reviewingClient(true)
		Expect(deploy.CheckPermissions(context.Background(), withRoute, testNamespace, "nginx")).To(Succeed())
		Expect(askedAbout(*routeAsked, "ingresses")).To(BeTrue())

		without, plainAsked := reviewingClient(true)
		Expect(deploy.CheckPermissions(context.Background(), without, testNamespace, "")).To(Succeed())
		Expect(askedAbout(*plainAsked, "ingresses")).To(BeFalse())
		Expect(askedAbout(*plainAsked, "deployments")).To(BeTrue())
	})

	// Checked at DEPLOY time on purpose: teardown running with rights the deploy
	// never verified is how a sidecar ends up unremovable and still routed.
	It("demands delete as well as create, so teardown cannot be the surprise", func() {
		client, asked := reviewingClient(true)
		Expect(deploy.CheckPermissions(context.Background(), client, testNamespace, "nginx")).To(Succeed())

		var verbs []string
		for _, attribute := range *asked {
			if attribute.Resource == "ingresses" {
				verbs = append(verbs, attribute.Verb)
			}
		}
		Expect(verbs).To(ConsistOf("create", "patch", "delete"))
	})

	It("checks the Traefik transport permissions only for a Traefik route", func() {
		traefik, asked := reviewingClient(true)
		Expect(deploy.CheckPermissions(context.Background(), traefik, testNamespace, "traefik")).To(Succeed())
		Expect(askedAbout(*asked, "serverstransports")).To(BeTrue())

		nginx, asked := reviewingClient(true)
		Expect(deploy.CheckPermissions(context.Background(), nginx, testNamespace, "nginx")).To(Succeed())
		Expect(askedAbout(*asked, "serverstransports")).To(BeFalse())
	})

	It("names the missing ingress permissions rather than failing at apply", func() {
		client, _ := reviewingClient(false)

		err := deploy.CheckPermissions(context.Background(), client, testNamespace, "nginx")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ingresses"))
	})
})

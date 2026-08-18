// The mutating half of `git-agent deploy`.
//
// New deployments mint after every cheap validation. Replacements retain the
// state volume and its enrollment, so they validate that identity before
// removing the old workload.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/text"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

func runDeploy(ctx context.Context, plan deploy.Plan, opts GitAgentDeployOptions,
	result GitAgentDeployResult, timeout time.Duration) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, namespace, err := deployPreflight(ctx, plan, opts)
	if err != nil {
		return nil, err
	}
	var customClient dynamic.Interface
	if plan.Target == deploy.TargetKubernetes {
		customClient, err = kubernetesDynamicClient(kubeClientOptions{Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
	}
	plan.Advertise, result.Advertise = reAdvertiseForNamespace(plan, opts, namespace, result.Advertise)
	result.Namespace = namespace
	result.EnrollmentReused = opts.reuseEnrollment
	if opts.reuseEnrollment {
		if err := validateReusableEnrollment(plan); err != nil {
			return nil, err
		}
	}

	if opts.Replace {
		if err := teardownWorkload(ctx, client, customClient, plan, namespace, false); err != nil {
			return nil, fmt.Errorf("replace existing deployment: %w", err)
		}
		result.Replaced = true
	}

	var enrollment GitAgentAddResult
	if !opts.reuseEnrollment {
		// The mint is the first irreversible step, and everything above has already
		// proven it will be usable.
		added, err := RunGitAgentAdd(ctx, GitAgentAddOptions{
			Name: plan.Name, Backend: plan.Backend, Endpoint: plan.Supervisor,
		})
		if err != nil {
			return nil, err
		}
		var ok bool
		enrollment, ok = added.(GitAgentAddResult)
		if !ok {
			return nil, fmt.Errorf("unexpected enrollment result %T", added)
		}
	}

	objects, err := provision(ctx, client, customClient, plan, opts, namespace, enrollment.Token)
	if err != nil {
		// A durable token nothing ever claims does not lapse on its own, so a
		// half-finished deploy would leave a live credential behind it.
		if rollbackErr := revokeUnclaimedToken(ctx, enrollment.TokenID, plan.Name); rollbackErr != nil {
			clicky.Printf("warning: could not revoke the unclaimed token for %q: %v\n", plan.Name, rollbackErr)
		}
		return nil, err
	}
	result.Objects = objects

	// Recorded now rather than after --wait: the workload exists from here on,
	// so a teardown must be able to find it even if the wait times out. Without
	// this an operator is left with a running sidecar and nothing that knows
	// which runtime it is on.
	if err := recordDeployment(plan, opts, namespace); err != nil {
		clicky.Printf("warning: deployed, but the deployment record could not be saved; "+
			"pass --target to undeploy: %v\n", err)
	}

	if !opts.Wait {
		clicky.Printf("deployed %s; not waiting for enrollment (--wait=false)\n", plan.WorkloadName())
		return result, nil
	}
	if opts.reuseEnrollment {
		if err := awaitReplacedWorkload(ctx, client, plan, namespace); err != nil {
			return nil, err
		}
	} else {
		if err := awaitEnrollment(ctx, client, plan, namespace); err != nil {
			return nil, err
		}
	}
	result.Ready, result.Enrolled = true, true

	// The Secret stays. The token is durable and the pod re-presents it on
	// every start — deleting it here would leave a Deployment that works until
	// its first reschedule and then cannot enroll, which is the failure mode
	// this whole change exists to remove. `undeploy` removes it along with the
	// workload, and revokes it.
	clicky.Printf("agent %q is enrolled and dispatchable at %s\n", plan.Name, plan.Advertise)
	return result, nil
}

// deployPreflight fails before the mint on anything the runtime can tell us.
func deployPreflight(ctx context.Context, plan deploy.Plan, opts GitAgentDeployOptions) (kubernetes.Interface, string, error) {
	if plan.Target == deploy.TargetDocker {
		if err := deploy.DockerAvailable(ctx); err != nil {
			return nil, "", err
		}
		// Pull before minting. This image carries a Go toolchain, Chromium and
		// several agent CLIs, and a cold pull can outlast the token's 15-minute
		// TTL — after which the token is burned on its first use, so the failure
		// is permanent and repeats identically on every restart.
		if !deploy.DockerImagePresent(ctx, plan.Image) {
			clicky.Printf("pulling %s before minting a token...\n", plan.Image)
			if err := deploy.DockerPull(ctx, plan.Image); err != nil {
				return nil, "", err
			}
		}
		return nil, "", nil
	}

	client, namespace, err := kubernetesClient(kubeClientOptions{Context: opts.KubeContext, Namespace: opts.Namespace})
	if err != nil {
		return nil, "", err
	}
	// Before the permission check, which is namespace-scoped and would report a
	// missing namespace as a missing permission.
	created, err := deploy.EnsureNamespace(ctx, client, namespace, opts.CreateNamespace)
	if err != nil {
		return nil, "", err
	}
	if created {
		clicky.Printf("created namespace %s\n", namespace)
	}
	// Applying four objects — five with an external route — is not transactional;
	// failing on the third leaves a Secret and a volume behind and an enrollment
	// already recorded.
	if err := deploy.CheckPermissions(ctx, client, namespace, plan.ExternalRoute.ClassName); err != nil {
		return nil, "", err
	}
	if plan.HasExternalRoute() {
		// Both of these produce an agent that enrolls, reports ready, and is never
		// dispatchable — the failure this whole command exists to prevent — so
		// they are checked before the mint rather than discovered hours later.
		if err := refuseUnroutableExternalRoute(ctx, client, plan.ExternalRoute); err != nil {
			return nil, "", err
		}
		if err := refuseDuplicateRouteHost(ctx, client, plan, namespace); err != nil {
			return nil, "", err
		}
	}
	return client, namespace, nil
}

// reAdvertiseForNamespace recomputes the cluster address once the namespace is
// resolved from the kubeconfig, unless the operator pinned one.
func reAdvertiseForNamespace(plan deploy.Plan, opts GitAgentDeployOptions, namespace, current string) (string, string) {
	if plan.Target != deploy.TargetKubernetes || opts.Advertise != "" || namespace == "" {
		return plan.Advertise, current
	}
	// An ingress advertise carries no namespace, so there is nothing for the
	// resolved one to change — and recomputing it would be a second derivation of
	// the string awaitEnrollment compares byte for byte.
	if plan.HasExternalRoute() {
		return plan.Advertise, current
	}
	advertise, _, err := resolveAdvertiseAddress(plan.Target, plan, namespace, "", runningInCluster())
	if err != nil {
		return plan.Advertise, current
	}
	return advertise, advertise
}

func provision(ctx context.Context, client kubernetes.Interface, customClient dynamic.Interface, plan deploy.Plan,
	opts GitAgentDeployOptions, namespace string, token text.SensitiveString) ([]string, error) {
	if plan.Target == deploy.TargetDocker {
		path := ""
		if !token.IsEmpty() {
			path = joinTokenPath(plan)
			if err := writeJoinTokenFile(path, token); err != nil {
				return nil, err
			}
		}
		id, err := deploy.DockerRun(ctx, plan, path)
		if err != nil {
			if path != "" {
				_ = os.Remove(path)
			}
			return nil, err
		}
		return []string{"container/" + id[:min(12, len(id))]}, nil
	}
	objects := []string{}
	if plan.UsesTraefik() {
		if err := deploy.ApplyTraefikServersTransport(ctx, customClient, plan, namespace); err != nil {
			return nil, err
		}
		objects = append(objects, "ServersTransport/"+plan.WorkloadName())
	}
	applied, err := deploy.KubernetesApply(ctx, client, plan, deploy.KubernetesOptions{
		Namespace:       namespace,
		StorageClass:    opts.StorageClass,
		ImagePullPolicy: opts.ImagePullPolicy,
		ImagePullSecret: opts.ImagePullSecret,
		JoinToken:       token.Value(),
	})
	return append(objects, applied...), err
}

func validateReusableEnrollment(plan deploy.Plan) error {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return err
	}
	entry, err := enrolledAgent(cfg, plan.Backend, plan.Name)
	if err != nil {
		return fmt.Errorf("reuse enrollment for agent %q: %w", plan.Name, err)
	}
	if ready, issue := gitAgentDispatchStatus(entry); !ready {
		return fmt.Errorf("reuse enrollment for agent %q: %s", plan.Name, issue)
	}
	endpoint, _ := entry["url"].(string)
	if endpoint != plan.Advertise {
		return fmt.Errorf(
			"reuse enrollment for agent %q: saved endpoint %s does not match requested endpoint %s",
			plan.Name, endpoint, plan.Advertise)
	}
	return nil
}

func awaitReplacedWorkload(ctx context.Context, client kubernetes.Interface, plan deploy.Plan, namespace string) error {
	if plan.Target == deploy.TargetKubernetes {
		return deploy.KubernetesReady(ctx, client, plan, namespace)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		container, found, err := deploy.DockerInspect(ctx, plan.WorkloadName())
		if err != nil {
			return err
		}
		if found && container.Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s did not become ready: %w", plan.WorkloadName(), ctx.Err())
		case <-ticker.C:
		}
	}
}

// writeJoinTokenFile puts the token where only the deploying user can read it.
// The workload bind-mounts it read-only and removes it once the join succeeds,
// so it never reaches argv, `docker inspect`, or /proc/<pid>/cmdline — all of
// which the coding agent inside that same container can read.
func writeJoinTokenFile(path string, token text.SensitiveString) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the join token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token.Value()), 0o600); err != nil {
		return fmt.Errorf("write the join token: %w", err)
	}
	return nil
}

// awaitEnrollment waits for the cycle to close, not merely for a process to
// start. Readiness alone would report success for a sidecar that came up and
// could not reach the mailbox.
func awaitEnrollment(ctx context.Context, client kubernetes.Interface, plan deploy.Plan, namespace string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if url := enrolledAgentURL(plan.Backend, plan.Name); url != "" {
			// The recorded address is what dispatch will actually use. If the
			// sidecar advertised something else, every dispatch goes to the wrong
			// place while the roster still looks healthy.
			if url != plan.Advertise {
				return fmt.Errorf(
					"agent %q enrolled advertising %s, but the deployment expects %s; dispatch would go to the wrong address",
					plan.Name, url, plan.Advertise)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent %q did not enroll within the timeout%s", plan.Name,
				workloadLogTail(ctx, client, plan, namespace))
		case <-ticker.C:
		}
	}
}

// workloadLogTail attaches the sidecar's own output to a timeout, so the
// failure reports why rather than only that it happened.
func workloadLogTail(ctx context.Context, client kubernetes.Interface, plan deploy.Plan, namespace string) string {
	// The caller's context is already expired; logs need a fresh deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	var logs string
	if plan.Target == deploy.TargetDocker {
		logs = deploy.DockerLogs(ctx, plan.WorkloadName(), 50)
	} else if client != nil {
		logs = deploy.KubernetesLogs(ctx, client, plan, namespace, 50)
	}
	if logs == "" {
		return ""
	}
	return "\n\nlast output from " + plan.WorkloadName() + ":\n" + logs
}

// enrolledAgentURL reports the address an enrolled agent advertised, empty when
// it has not enrolled.
func enrolledAgentURL(backendName, agentName string) string {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return ""
	}
	entry, err := enrolledAgent(cfg, backendName, agentName)
	if err != nil {
		return ""
	}
	url, _ := entry["url"].(string)
	return url
}

// revokeUnclaimedToken withdraws the credential a failed deploy minted.
//
// A durable token that nothing ever claims does not expire on its own, so a
// half-finished deploy would leave a live credential for a workload that never
// started. Revocation is the honest undo: the row stays, with a reason.
func revokeUnclaimedToken(ctx context.Context, tokenID, agentName string) error {
	if tokenID == "" {
		return nil
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return err
	}
	return db.RevokeAPIToken(ctx, tokenID, "deploy of agent "+agentName+" failed before the workload started")
}

// teardownWorkload removes the workload for a plan on either target.
func teardownWorkload(ctx context.Context, client kubernetes.Interface, customClient dynamic.Interface, plan deploy.Plan,
	namespace string, purgeVolume bool) error {
	if plan.Target == deploy.TargetDocker {
		if err := deploy.DockerRemove(ctx, plan, purgeVolume); err != nil {
			return err
		}
		_ = os.Remove(joinTokenPath(plan))
		return nil
	}
	if _, err := deploy.KubernetesRemove(ctx, client, plan, namespace, purgeVolume); err != nil {
		return err
	}
	if !plan.UsesTraefik() {
		return nil
	}
	_, err := deploy.DeleteTraefikServersTransport(ctx, customClient, plan, namespace)
	return err
}

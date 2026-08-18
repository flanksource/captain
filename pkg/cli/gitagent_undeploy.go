package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
	"github.com/flanksource/clicky"
)

// GitAgentUndeployOptions tears down a deployed sidecar.
//
// It is separate from `revoke` because the two do different halves of the same
// job and either is useful alone: revoke refuses an agent's key from now on but
// leaves the machine running, while undeploy removes the machine. Run alone,
// revoke leaves a host on the network still holding a valid key, a checkout of
// the source tree, and any model credentials it was given.
type GitAgentUndeployOptions struct {
	Name    string `args:"true" help:"Deployed agent to tear down"`
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Target  string `flag:"target" help:"Where the sidecar runs: docker or kubernetes (default: whatever deploy recorded)"`

	Namespace   string `flag:"namespace" help:"kubernetes: namespace holding the sidecar (default: the kubeconfig context's)"`
	KubeContext string `flag:"kube-context" help:"kubernetes: kubeconfig context (default: current-context)"`

	Purge bool `flag:"purge" help:"Also delete the state volume, which holds the agent's private key"`
	// Revoking is the default so the two halves cannot drift apart.
	KeepEnrollment bool `flag:"keep-enrollment" help:"Leave the agent enrolled; by default undeploy also revokes it"`
	DryRun         bool `flag:"dry-run" help:"Print every intended mutation without touching anything" short:"n"`
}

type GitAgentUndeployResult struct {
	Backend  string   `json:"backend" pretty:"label=Backend"`
	Agent    string   `json:"agent" pretty:"label=Agent"`
	Target   string   `json:"target" pretty:"label=Target"`
	Removed  []string `json:"removed" pretty:"label=Removed"`
	Revoked  bool     `json:"revoked" pretty:"label=Revoked"`
	Retained string   `json:"retained,omitempty" pretty:"label=Retained"`
	DryRun   bool     `json:"dryRun,omitempty" pretty:"label=Dry Run"`
}

func RunGitAgentUndeploy(ctx context.Context, opts GitAgentUndeployOptions) (any, error) {
	target, err := resolveUndeployTarget(opts.Backend, opts.Name, opts.Target)
	if err != nil {
		return nil, err
	}
	plan := deploy.Plan{Name: opts.Name, Backend: opts.Backend, Target: target}
	if recorded, ok := lookupDeployment(opts.Backend, opts.Name); ok && opts.Namespace == "" {
		opts.Namespace = recorded.Namespace
	}
	result := GitAgentUndeployResult{Backend: opts.Backend, Agent: opts.Name, Target: string(target)}

	if opts.DryRun {
		clicky.Printf("[dry-run] would remove the workload %s\n", plan.WorkloadName())
		if opts.Purge {
			clicky.Printf("[dry-run] would delete the state volume %s, destroying the agent's private key\n", plan.VolumeName())
		} else {
			clicky.Printf("[dry-run] would retain the state volume %s\n", plan.VolumeName())
		}
		if !opts.KeepEnrollment {
			clicky.Printf("[dry-run] would revoke agent %q from sandbox.backends.%s in %s\n",
				opts.Name, opts.Backend, configPathForDisplay())
		}
		result.DryRun = true
		return result, nil
	}

	var removed []string
	if target == deploy.TargetDocker {
		// teardownWorkload also removes the host-side join token file.
		if err := teardownWorkload(ctx, nil, nil, plan, "", opts.Purge); err != nil {
			return nil, err
		}
		removed = []string{"container/" + plan.WorkloadName()}
		if opts.Purge {
			removed = append(removed, "volume/"+plan.VolumeName())
		}
	} else {
		client, namespace, err := kubernetesClient(kubeClientOptions{Context: opts.KubeContext, Namespace: opts.Namespace})
		if err != nil {
			return nil, err
		}
		customClient, err := kubernetesDynamicClient(kubeClientOptions{Context: opts.KubeContext})
		if err != nil {
			return nil, err
		}
		if removed, err = deploy.KubernetesRemove(ctx, client, plan, namespace, opts.Purge); err != nil {
			return nil, err
		}
		transportRemoved, err := deploy.DeleteTraefikServersTransport(ctx, customClient, plan, namespace)
		if err != nil {
			return nil, err
		}
		if transportRemoved {
			removed = append(removed, "ServersTransport/"+plan.WorkloadName())
		}
	}
	result.Removed = removed
	// The workload is gone, so the record that pointed at it is stale; leaving
	// it would make the roster keep offering to tear down something that is no
	// longer there.
	if err := forgetDeployment(opts.Backend, opts.Name); err != nil {
		clicky.Printf("warning: the workload is gone but its deployment record could not be cleared: %v\n", err)
	}

	if !opts.Purge {
		result.Retained = fmt.Sprintf("%s (holds the agent's private key; delete with --purge)", plan.VolumeName())
	}
	if opts.KeepEnrollment {
		clicky.Printf("agent %q is still enrolled; its key and token remain valid until you revoke them\n", opts.Name)
		return result, nil
	}
	// The token outlives the workload unless it is revoked: it is durable, so
	// nothing else ever retires it, and a live credential for a torn-down agent
	// is exactly what R8.5 exists to prevent.
	if err := revokeAgentTokens(ctx, opts.Name); err != nil {
		clicky.Printf("warning: the workload is gone but its captain tokens could not be revoked: %v\n", err)
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: opts.Name, Backend: opts.Backend}); err != nil {
		// The workload is already gone, so a revoke failure is worth reporting
		// loudly but does not undo the teardown.
		if !strings.Contains(err.Error(), "is not enrolled") && !strings.Contains(err.Error(), "has no enrolled agents") {
			return result, fmt.Errorf("workload removed, but revoking the agent failed: %w", err)
		}
	} else {
		result.Revoked = true
	}
	return result, nil
}

// revokeAgentTokens retires every live token that speaks for an agent.
//
// A bound token is retired outright. A pool token is left alone: it serves
// siblings that are still running, and revoking it to tear down one member
// would take the whole deployment offline.
func revokeAgentTokens(ctx context.Context, agent string) error {
	db, err := captainServeDB(ctx)
	if err != nil {
		return err
	}
	tokens, err := db.ListAPITokens(ctx, database.ListAPITokensFilter{Agent: agent})
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token.Pool {
			clicky.Printf("token %s serves pool %q and other members; leaving it in place\n", token.TokenID, token.Name)
			continue
		}
		if err := db.RevokeAPIToken(ctx, token.TokenID, "agent "+agent+" was undeployed"); err != nil {
			return err
		}
	}
	return nil
}

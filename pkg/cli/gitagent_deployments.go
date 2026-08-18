// Where a deployed sidecar actually went.
//
// The agent roster records dispatch targeting — an endpoint and a host key —
// and says nothing about the runtime the workload runs on. Tearing one down
// needs that: `undeploy --target docker` against a Kubernetes agent removes
// nothing and reports success, because there is no container by that name. So
// deploy writes down where it put the workload, and undeploy reads it back.

package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// GitAgentDeployment is one recorded placement.
type GitAgentDeployment struct {
	Target     string                    `json:"target" pretty:"label=Target"`
	Namespace  string                    `json:"namespace,omitempty" pretty:"label=Namespace"`
	Workload   string                    `json:"workload" pretty:"label=Workload"`
	Image      string                    `json:"image,omitempty" pretty:"label=Image"`
	DeployedAt string                    `json:"deployedAt,omitempty" pretty:"label=Deployed"`
	Config     *GitAgentDeploymentConfig `json:"config,omitempty" pretty:"-"`
}

// GitAgentDeploymentConfig is the resolved, non-secret deployment input needed
// to edit a workload without resetting settings the form does not expose.
type GitAgentDeploymentConfig struct {
	Target            string   `json:"target"`
	Transport         string   `json:"transport,omitempty"`
	Namespace         string   `json:"namespace,omitempty"`
	KubeContext       string   `json:"kubeContext,omitempty"`
	Domain            string   `json:"domain,omitempty"`
	IngressClass      string   `json:"ingressClass,omitempty"`
	IngressIssuer     string   `json:"ingressIssuer,omitempty"`
	IngressTLSSecret  string   `json:"ingressTlsSecret,omitempty"`
	IngressAnnotation []string `json:"ingressAnnotation,omitempty"`
	Image             string   `json:"image,omitempty"`
	ImagePullPolicy   string   `json:"imagePullPolicy,omitempty"`
	ImagePullSecret   string   `json:"imagePullSecret,omitempty"`
	SupervisorAddress string   `json:"supervisorAddress,omitempty"`
	Advertise         string   `json:"advertise,omitempty"`
	ListenPort        *int     `json:"listenPort,omitempty"`
	HostPort          int      `json:"hostPort,omitempty"`
	CPURequest        string   `json:"cpuRequest,omitempty"`
	CPULimit          string   `json:"cpuLimit,omitempty"`
	MemoryRequest     string   `json:"memoryRequest,omitempty"`
	MemoryLimit       string   `json:"memoryLimit,omitempty"`
	Storage           string   `json:"storage,omitempty"`
	StorageClass      string   `json:"storageClass,omitempty"`
	TmpSize           string   `json:"tmpSize,omitempty"`
	PidsLimit         *int     `json:"pidsLimit,omitempty"`
	RunAsUser         *int     `json:"runAsUser,omitempty"`
	RunAsGroup        *int     `json:"runAsGroup,omitempty"`
	Home              string   `json:"home,omitempty"`
	ReadOnlyRoot      *bool    `json:"readOnlyRoot,omitempty"`
	Network           string   `json:"network,omitempty"`
	CapAdd            []string `json:"capAdd,omitempty"`
	Env               []string `json:"env,omitempty"`
	EnvFromSecret     []string `json:"envFromSecret,omitempty"`
	CredentialsSecret string   `json:"credentialsSecret,omitempty"`
	CredentialsDir    string   `json:"credentialsDir,omitempty"`
	Wait              *bool    `json:"wait,omitempty"`
	Timeout           string   `json:"timeout,omitempty"`
}

func deploymentConfig(plan deploy.Plan, opts GitAgentDeployOptions, namespace string) GitAgentDeploymentConfig {
	return GitAgentDeploymentConfig{
		Target: string(plan.Target), Transport: opts.Transport,
		Namespace: namespace, KubeContext: opts.KubeContext,
		Domain: opts.Domain, IngressClass: opts.IngressClass,
		IngressIssuer: opts.IngressIssuer, IngressTLSSecret: opts.IngressTLSSecret,
		IngressAnnotation: opts.IngressAnnotation,
		Image:             plan.Image, ImagePullPolicy: opts.ImagePullPolicy, ImagePullSecret: opts.ImagePullSecret,
		SupervisorAddress: plan.Supervisor, Advertise: plan.Advertise,
		ListenPort: intPointer(plan.ListenPort), HostPort: plan.HostPort,
		CPURequest: opts.CPURequest, CPULimit: opts.CPULimit,
		MemoryRequest: opts.MemoryRequest, MemoryLimit: opts.MemoryLimit,
		Storage: opts.Storage, StorageClass: opts.StorageClass, TmpSize: opts.TmpSize,
		PidsLimit: intPointer(opts.PidsLimit),
		RunAsUser: intPointer(opts.RunAsUser), RunAsGroup: intPointer(opts.RunAsGroup),
		Home: plan.Home, ReadOnlyRoot: boolPointer(opts.ReadOnlyRoot),
		Network: opts.Network, CapAdd: opts.CapAdd,
		Env: opts.Env, EnvFromSecret: opts.EnvFromSecret,
		CredentialsSecret: opts.CredentialsSecret, CredentialsDir: opts.CredentialsDir,
		Wait: boolPointer(opts.Wait), Timeout: opts.Timeout,
	}
}

func (config GitAgentDeploymentConfig) options(name, backend string) GitAgentDeployOptions {
	opts := defaultGitAgentDeployOptions()
	opts.Name, opts.Backend, opts.Target = name, backend, config.Target
	opts.Transport = strings.TrimSpace(config.Transport)
	opts.Namespace, opts.KubeContext = strings.TrimSpace(config.Namespace), strings.TrimSpace(config.KubeContext)
	opts.Domain, opts.IngressIssuer = strings.TrimSpace(config.Domain), strings.TrimSpace(config.IngressIssuer)
	opts.IngressTLSSecret, opts.IngressAnnotation = strings.TrimSpace(config.IngressTLSSecret), config.IngressAnnotation
	opts.SupervisorAddress = strings.TrimSpace(config.SupervisorAddress)
	opts.Advertise, opts.StorageClass = strings.TrimSpace(config.Advertise), strings.TrimSpace(config.StorageClass)
	opts.HostPort = config.HostPort
	opts.CapAdd, opts.Env, opts.EnvFromSecret = config.CapAdd, config.Env, config.EnvFromSecret
	opts.CredentialsSecret = strings.TrimSpace(config.CredentialsSecret)
	opts.CredentialsDir = strings.TrimSpace(config.CredentialsDir)
	for _, override := range []struct {
		value string
		into  *string
	}{
		{config.IngressClass, &opts.IngressClass},
		{config.Image, &opts.Image},
		{config.ImagePullPolicy, &opts.ImagePullPolicy},
		{config.ImagePullSecret, &opts.ImagePullSecret},
		{config.CPURequest, &opts.CPURequest}, {config.CPULimit, &opts.CPULimit},
		{config.MemoryRequest, &opts.MemoryRequest}, {config.MemoryLimit, &opts.MemoryLimit},
		{config.Storage, &opts.Storage}, {config.TmpSize, &opts.TmpSize},
		{config.Home, &opts.Home}, {config.Network, &opts.Network}, {config.Timeout, &opts.Timeout},
	} {
		if trimmed := strings.TrimSpace(override.value); trimmed != "" {
			*override.into = trimmed
		}
	}
	for _, field := range []struct{ value, into *int }{
		{config.ListenPort, &opts.ListenPort}, {config.PidsLimit, &opts.PidsLimit},
		{config.RunAsUser, &opts.RunAsUser}, {config.RunAsGroup, &opts.RunAsGroup},
	} {
		if field.value != nil {
			*field.into = *field.value
		}
	}
	if config.ReadOnlyRoot != nil {
		opts.ReadOnlyRoot = *config.ReadOnlyRoot
	}
	if config.Wait != nil {
		opts.Wait = *config.Wait
	}
	return opts
}

func intPointer(value int) *int    { return &value }
func boolPointer(value bool) *bool { return &value }

// recordDeployment stores where a workload was placed and the resolved inputs
// needed to edit it without resetting settings hidden by the web form.
func recordDeployment(plan deploy.Plan, opts GitAgentDeployOptions, namespace string) error {
	config, err := deploymentConfigRecord(deploymentConfig(plan, opts, namespace))
	if err != nil {
		return err
	}
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, plan.Backend)
		if err != nil {
			return err
		}
		deployments, _ := backend.Options["deployments"].(map[string]any)
		if deployments == nil {
			deployments = map[string]any{}
		}
		record := map[string]any{
			"target":     string(plan.Target),
			"workload":   plan.WorkloadName(),
			"image":      plan.Image,
			"deployedAt": time.Now().UTC().Format(time.RFC3339),
			"config":     config,
		}
		if strings.TrimSpace(namespace) != "" {
			record["namespace"] = namespace
		}
		deployments[plan.Name] = record
		backend.Options["deployments"] = deployments
		cfg.Sandbox.Backends[plan.Backend] = backend
		return nil
	})
}

// forgetDeployment drops the record once the workload is gone. A stale record
// would make the UI offer to tear down something that no longer exists.
func forgetDeployment(backendName, agentName string) error {
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, ok := cfg.Sandbox.Backends[backendName]
		if !ok {
			return nil
		}
		deployments, _ := backend.Options["deployments"].(map[string]any)
		delete(deployments, agentName)
		if len(deployments) == 0 {
			delete(backend.Options, "deployments")
		}
		cfg.Sandbox.Backends[backendName] = backend
		return nil
	})
}

// lookupDeployment reads back where an agent was deployed, if it was.
func lookupDeployment(backendName, agentName string) (GitAgentDeployment, bool) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return GitAgentDeployment{}, false
	}
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return GitAgentDeployment{}, false
	}
	deployments, _ := backend.Options["deployments"].(map[string]any)
	record, ok := deployments[agentName].(map[string]any)
	if !ok {
		return GitAgentDeployment{}, false
	}
	return deploymentFromRecord(record), true
}

func deploymentFromRecord(record map[string]any) GitAgentDeployment {
	deployment := GitAgentDeployment{}
	deployment.Target, _ = record["target"].(string)
	deployment.Namespace, _ = record["namespace"].(string)
	deployment.Workload, _ = record["workload"].(string)
	deployment.Image, _ = record["image"].(string)
	deployment.DeployedAt, _ = record["deployedAt"].(string)
	deployment.Config = deploymentConfigFromRecord(record["config"])
	return deployment
}

func deploymentConfigRecord(config GitAgentDeploymentConfig) (map[string]any, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode deployment edit config: %w", err)
	}
	record := map[string]any{}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("store deployment edit config: %w", err)
	}
	return record, nil
}

func deploymentConfigFromRecord(value any) *GitAgentDeploymentConfig {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil
	}
	var config GitAgentDeploymentConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil
	}
	return &config
}

// resolveUndeployTarget picks the runtime to tear down from.
//
// An explicit --target wins, but it must agree with the record: tearing down
// with the wrong one removes nothing and reports success, leaving a live sidecar
// on the network holding a valid key and a checkout of the source tree.
func resolveUndeployTarget(backendName, agentName, override string) (deploy.Target, error) {
	recorded, found := lookupDeployment(backendName, agentName)
	given := strings.TrimSpace(override)
	if given == "" {
		if !found {
			return "", fmt.Errorf(
				"captain has no record of deploying %q, so it cannot tell which runtime to tear down; pass --target docker or --target kubernetes",
				agentName)
		}
		return deploy.ParseTarget(recorded.Target)
	}
	target, err := deploy.ParseTarget(given)
	if err != nil {
		return "", err
	}
	if found && recorded.Target != "" && !strings.EqualFold(recorded.Target, string(target)) {
		return "", fmt.Errorf(
			"%q was deployed on %s, not %s; tearing down the wrong runtime removes nothing and would leave the sidecar running",
			agentName, recorded.Target, target)
	}
	return target, nil
}

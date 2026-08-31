package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/fixture"
	"github.com/flanksource/captain/pkg/ai/fixture/kubeproxy"
	"github.com/flanksource/captain/pkg/ai/fixture/mcpproxy"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

const (
	maxObservationExternalEvents          = 256
	kubernetesCaptureUnavailableErrorCode = "kubernetes_capture_unavailable"
)

type observationCaptureSession struct {
	mu sync.Mutex

	mcpCapture   api.ObservationExternalCapture
	kubeCapture  api.ObservationExternalCapture
	mcpEvents    []api.ObservationExternalEvent
	kubeEvents   []api.ObservationExternalEvent
	mcpOverflow  bool
	kubeOverflow bool
	runtime      observation.RuntimeCaptureConfig
	mcp          *fixture.MCPConfigCapture
	kube         *kubeproxy.Proxy
	kubeDir      string
	blockingCode string
}

// initialObservationExternalCapture is the pre-start contract shared by early
// setup returns and live capture startup. Later startup can only refine it with
// backend reachability or proxy results.
func initialObservationExternalCapture(req ai.Request, flags map[string]string) (api.ObservationExternalCapture, api.ObservationExternalCapture) {
	mcp := api.ObservationExternalCapture{
		Status: api.ObservationCaptureUnavailable, ReasonCode: "capture_not_started",
		Events: []api.ObservationExternalEvent{},
	}
	hasMCPConfig := len(flagSlice(flags["mcp-config"])) > 0
	switch {
	case req.Permissions.MCP.Disabled:
		mcp.Status = api.ObservationCaptureNotRequested
		mcp.ReasonCode = ""
	case !hasMCPConfig && (len(req.Permissions.MCP.Servers) > 0 || len(req.Permissions.MCP.Modes) > 0):
		mcp.ReasonCode = "configured_mcp_not_intercepted"
	case !hasMCPConfig:
		mcp.Status = api.ObservationCaptureNotRequested
		mcp.ReasonCode = ""
	}

	kubernetes := api.ObservationExternalCapture{
		Status: api.ObservationCaptureUnavailable, ReasonCode: "capture_not_started",
		Events: []api.ObservationExternalEvent{},
	}
	if !flagBool(flags["capture-kubernetes"]) {
		kubernetes.Status = api.ObservationCaptureNotRequested
		kubernetes.ReasonCode = ""
	}
	return mcp, kubernetes
}

func startObservationCapture(
	flags map[string]string,
	runtime api.Model,
	req ai.Request,
	cfg ai.Config,
) (*observationCaptureSession, error) {
	baseDir := req.Cwd()
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve observation capture directory: %w", err)
		}
	}
	mcpCapture, kubeCapture := initialObservationExternalCapture(req, flags)
	session := &observationCaptureSession{
		mcpCapture:  mcpCapture,
		kubeCapture: kubeCapture,
		runtime:     observation.RuntimeCaptureConfig{Environment: map[string]string{}},
	}
	if strings.TrimSpace(flags["kubeconfig"]) != "" && !flagBool(flags["capture-kubernetes"]) {
		return nil, fmt.Errorf("--kubeconfig requires --capture-kubernetes")
	}

	mcpConfigs := flagSlice(flags["mcp-config"])
	if req.Permissions.MCP.Disabled && len(mcpConfigs) > 0 {
		return nil, fmt.Errorf("--mcp-config cannot be combined with --no-mcp")
	}
	if len(mcpConfigs) > 0 && runtime.Backend != api.BackendClaudeCLI {
		session.mcpCapture.Status = api.ObservationCaptureUnsupported
		session.mcpCapture.ReasonCode = "runtime_mcp_config_unsupported"
		session.blockingCode = "mcp_config_unsupported"
	} else if len(mcpConfigs) > 0 && (!localProcessCapture(cfg) || relocatesRun(cfg)) {
		session.mcpCapture.ReasonCode = "runtime_mcp_proxy_unreachable"
		session.blockingCode = "mcp_capture_unavailable"
	} else if len(mcpConfigs) > 0 {
		capture, err := fixture.StartMCPConfigCapture(mcpConfigs, baseDir, session.recordMCP)
		if err != nil {
			return nil, err
		}
		session.mcp = capture
		session.runtime.MCPConfigs = append([]string(nil), capture.Configs...)
		switch {
		case capture.HasHTTP && capture.HasUncaptured:
			session.mcpCapture.Status = api.ObservationCapturePartial
			session.mcpCapture.ReasonCode = "non_http_mcp_not_interceptable"
		case capture.HasHTTP || !capture.HasUncaptured:
			session.mcpCapture.Status = api.ObservationCaptureComplete
		default:
			session.mcpCapture.Status = api.ObservationCaptureUnsupported
			session.mcpCapture.ReasonCode = "non_http_mcp_not_interceptable"
		}
	}

	if !flagBool(flags["capture-kubernetes"]) {
		return session, nil
	}
	session.kubeCapture.Status = api.ObservationCaptureUnsupported
	session.kubeCapture.ReasonCode = "runtime_kubernetes_capture_unsupported"
	if !kubernetesProcessBackend(runtime.Backend) {
		return session, nil
	}
	if !localProcessCapture(cfg) || relocatesRun(cfg) {
		session.kubeCapture.Status = api.ObservationCaptureUnavailable
		session.kubeCapture.ReasonCode = "runtime_kubernetes_proxy_unreachable"
		return session, nil
	}
	proxy, err := kubeproxy.Start(strings.TrimSpace(flags["kubeconfig"]))
	if err != nil {
		session.kubeCapture.Status = api.ObservationCaptureUnavailable
		session.kubeCapture.ReasonCode = "kubernetes_proxy_start_failed"
		session.blockingCode = kubernetesCaptureUnavailableErrorCode
		return session, nil
	}
	kubeDir, err := os.MkdirTemp("", "captain-observe-kube-")
	if err != nil {
		proxy.Close()
		session.kubeCapture.Status = api.ObservationCaptureUnavailable
		session.kubeCapture.ReasonCode = "kubernetes_proxy_setup_failed"
		session.blockingCode = kubernetesCaptureUnavailableErrorCode
		return session, nil
	}
	kubeconfig, err := proxy.WriteKubeconfig(kubeDir)
	if err != nil {
		proxy.Close()
		_ = os.RemoveAll(kubeDir)
		session.kubeCapture.Status = api.ObservationCaptureUnavailable
		session.kubeCapture.ReasonCode = "kubernetes_proxy_setup_failed"
		session.blockingCode = kubernetesCaptureUnavailableErrorCode
		return session, nil
	}
	proxy.SetObserver(session.recordKubernetes)
	session.kube = proxy
	session.kubeDir = kubeDir
	session.runtime.Environment["KUBECONFIG"] = kubeconfig
	session.kubeCapture.Status = api.ObservationCaptureComplete
	session.kubeCapture.ReasonCode = ""
	return session, nil
}

func (s *observationCaptureSession) Context(ctx context.Context) context.Context {
	return observation.ContextWithRuntimeCapture(ctx, s.runtime)
}

func (s *observationCaptureSession) Close() {
	if s == nil {
		return
	}
	if s.mcp != nil {
		s.mcp.Close()
	}
	if s.kube != nil {
		s.kube.Close()
	}
	if s.kubeDir != "" {
		_ = os.RemoveAll(s.kubeDir)
	}
}

func (s *observationCaptureSession) recordMCP(event mcpproxy.ObservationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mcpEvents) >= maxObservationExternalEvents {
		s.mcpOverflow = true
		return
	}
	duration := event.Duration.Milliseconds()
	method := captureIdentifier(event.RPCMethod)
	if method == "" {
		method = captureIdentifier(event.HTTPMethod)
	}
	s.mcpEvents = append(s.mcpEvents, api.ObservationExternalEvent{
		ID: fmt.Sprintf("mcp-%d", len(s.mcpEvents)+1), Kind: "request",
		Target: captureIdentifier(event.Server), Method: method,
		HTTPMethod: captureIdentifier(event.HTTPMethod), Tool: captureIdentifier(event.Tool),
		Status: strconv.Itoa(event.Status), DurationMS: &duration,
	})
}

func (s *observationCaptureSession) recordKubernetes(event kubeproxy.ObservationEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.kubeEvents) >= maxObservationExternalEvents {
		s.kubeOverflow = true
		return
	}
	duration := event.Duration.Milliseconds()
	s.kubeEvents = append(s.kubeEvents, api.ObservationExternalEvent{
		ID: fmt.Sprintf("kubernetes-%d", len(s.kubeEvents)+1), Kind: "request",
		Method: captureIdentifier(event.Method), Resource: captureIdentifier(event.Resource),
		Status: strconv.Itoa(event.Status), DurationMS: &duration,
	})
}

func (s *observationCaptureSession) Apply(result *api.RuntimeObservation, tools []api.ObservationToolEvent) {
	s.mu.Lock()
	mcpEvents := append([]api.ObservationExternalEvent(nil), s.mcpEvents...)
	kubeEvents := append([]api.ObservationExternalEvent(nil), s.kubeEvents...)
	mcpOverflow, kubeOverflow := s.mcpOverflow, s.kubeOverflow
	s.mu.Unlock()

	correlateMCPObservationEvents(mcpEvents, tools)
	s.mcpCapture.Events = mcpEvents
	s.kubeCapture.Events = kubeEvents
	if mcpOverflow {
		s.mcpCapture.Status = partialIfComplete(s.mcpCapture.Status)
		s.mcpCapture.ReasonCode = "capture_truncated"
	}
	if kubeOverflow {
		s.kubeCapture.Status = partialIfComplete(s.kubeCapture.Status)
		s.kubeCapture.ReasonCode = "capture_truncated"
	}
	result.Capture.MCP = s.mcpCapture
	result.Capture.Kubernetes = s.kubeCapture
}

func correlateMCPObservationEvents(events []api.ObservationExternalEvent, tools []api.ObservationToolEvent) {
	for i := range events {
		if events[i].Tool == "" {
			continue
		}
		match := ""
		for _, tool := range tools {
			if tool.ToolCallID == "" || !mcpToolMatches(tool.Name, events[i].Target, events[i].Tool) {
				continue
			}
			if match != "" && match != tool.ToolCallID {
				match = ""
				break
			}
			match = tool.ToolCallID
		}
		events[i].CorrelationID = match
	}
}

func mcpToolMatches(recorded, server, proxied string) bool {
	return recorded == proxied || server != "" && recorded == "mcp__"+server+"__"+proxied
}

func captureIdentifier(value string) string {
	value = strings.TrimSpace(value)
	var output strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/-", r) {
			output.WriteRune(r)
		} else {
			output.WriteByte('_')
		}
		if output.Len() >= 128 {
			break
		}
	}
	return output.String()
}

func localProcessCapture(cfg ai.Config) bool {
	sandbox := cfg.ResolvedSandbox()
	return sandbox == nil || sandbox.Kind == api.SandboxNone
}

func kubernetesProcessBackend(backend api.Backend) bool {
	switch backend {
	case api.BackendClaudeCLI, api.BackendCodexCLI, api.BackendGeminiCLI:
		return true
	default:
		return false
	}
}

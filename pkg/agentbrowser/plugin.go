package agentbrowser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// The agent-browser plugin protocol, as observed on the wire against
// agent-browser 0.31.1. A plugin is an external executable; agent-browser writes
// one JSON request object to its stdin — with no trailing newline — and reads
// one JSON response object from its stdout. The response must echo the protocol
// back (agent-browser rejects it with "used unsupported protocol" otherwise) and
// carry a success flag.
const (
	agentBrowserPluginProtocol = "agent-browser.plugin.v1"
	agentBrowserPluginName     = "captain"
	agentBrowserLaunchMutate   = "launch.mutate"
	agentBrowserPluginManifest = "plugin.manifest"
)

type browserPluginRequest struct {
	Protocol   string          `json:"protocol"`
	Type       string          `json:"type"`
	Capability string          `json:"capability"`
	Request    json.RawMessage `json:"request"`
}

type browserPluginResponse struct {
	Protocol     string                 `json:"protocol"`
	Success      bool                   `json:"success"`
	Error        string                 `json:"error,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Capabilities []string               `json:"capabilities,omitempty"`
	Data         *browserPluginManifest `json:"data,omitempty"`
}

type browserPluginManifest struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// RunBrowserPlugin serves the agent-browser plugin protocol on stdin/stdout.
//
// Captain registers itself as a launch.mutate plugin purely to observe: it
// appends nothing to the launch and records which agent session opened the
// browser. It deliberately does not add a Chrome argument carrying the session
// id — agent-browser hashes the launch configuration into <session>.config, and
// a hash that changed per agent session would force a daemon restart and discard
// the browser's state every time a different session attached to the same name.
//
// A json.Decoder is used rather than a line reader because the request arrives
// without a trailing newline; the loop also covers a stream of requests on one
// process, and ends at EOF.
func RunBrowserPlugin(stdin io.Reader, stdout, stderr io.Writer, stateDir string) error {
	decoder := json.NewDecoder(stdin)
	encoder := json.NewEncoder(stdout)
	for {
		var request browserPluginRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode agent-browser plugin request: %w", err)
		}
		response, err := handleBrowserPluginRequest(request, stateDir, stderr)
		if err != nil {
			return err
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
}

func handleBrowserPluginRequest(request browserPluginRequest, stateDir string, stderr io.Writer) (browserPluginResponse, error) {
	if request.Protocol != agentBrowserPluginProtocol {
		return browserPluginFailure(fmt.Sprintf("unsupported protocol %q", request.Protocol)), nil
	}
	switch request.Type {
	case agentBrowserPluginManifest:
		capabilities := []string{agentBrowserLaunchMutate}
		return browserPluginResponse{
			Protocol:     agentBrowserPluginProtocol,
			Success:      true,
			Name:         agentBrowserPluginName,
			Capabilities: capabilities,
			// Manifest discovery reads name and capabilities, but which of the two
			// shapes it reads is not documented; both are cheap to declare.
			Data: &browserPluginManifest{Name: agentBrowserPluginName, Capabilities: capabilities},
		}, nil
	case agentBrowserLaunchMutate:
		return handleBrowserLaunchMutate(request, stateDir, stderr)
	default:
		return browserPluginFailure(fmt.Sprintf("unsupported request type %q", request.Type)), nil
	}
}

// handleBrowserLaunchMutate records the agent session launching this browser.
//
// Failure policy: an unidentifiable agent session is a missing input, not a
// broken invariant — captain warns and lets the browser start, because refusing
// the launch would break the user's browsing to protect a listing. Failing to
// store a link captain *did* resolve is a real failure and is returned.
func handleBrowserLaunchMutate(request browserPluginRequest, stateDir string, stderr io.Writer) (browserPluginResponse, error) {
	var launch struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(request.Request, &launch); err != nil {
		return browserPluginResponse{}, fmt.Errorf("decode launch.mutate request: %w", err)
	}
	session := strings.TrimSpace(launch.Session)
	if session == "" {
		session = strings.TrimSpace(os.Getenv("AGENT_BROWSER_SESSION"))
	}
	if session == "" {
		fmt.Fprintln(stderr, "captain: agent-browser did not name the session being launched; no link recorded")
		return browserPluginSuccess(), nil
	}

	environment := currentEnvironment()
	agentSource, agentSessionID := agentIdentityFromEnv(environment)
	if agentSessionID == "" {
		fmt.Fprintf(stderr, "captain: no agent session identifies this browser launch (%s); no link recorded\n", session)
		return browserPluginSuccess(), nil
	}

	namespace := strings.TrimSpace(os.Getenv("AGENT_BROWSER_NAMESPACE"))
	record := browserLinkRecord{
		BrowserSession: session,
		Namespace:      namespace,
		SocketDir:      strings.TrimSpace(os.Getenv("AGENT_BROWSER_SOCKET_DIR")),
		// The plugin is spawned by the process that called it, which in this
		// protocol is agent-browser's per-session daemon.
		DaemonPID:      os.Getppid(),
		AgentSource:    agentSource,
		AgentSessionID: agentSessionID,
		ClaudePID:      envInt(environment, "CLAUDE_PID"),
		CWD:            workingDirectory(),
		LaunchedAt:     time.Now().UTC(),
		PluginRequest:  request.Request,
	}
	if err := writeBrowserLinkRecord(browserLinkPath(stateDir, namespace, session), record); err != nil {
		return browserPluginResponse{}, fmt.Errorf("record browser session link for %q: %w", session, err)
	}
	return browserPluginSuccess(), nil
}

func browserPluginSuccess() browserPluginResponse {
	return browserPluginResponse{Protocol: agentBrowserPluginProtocol, Success: true}
}

func browserPluginFailure(message string) browserPluginResponse {
	return browserPluginResponse{Protocol: agentBrowserPluginProtocol, Success: false, Error: message}
}

// currentEnvironment is this process's own environment. The plugin is a
// descendant of the agent that ran agent-browser, so it inherits the agent's
// session id.
func currentEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		environment[key] = value
	}
	return environment
}

func workingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

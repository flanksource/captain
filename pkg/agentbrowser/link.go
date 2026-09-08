package agentbrowser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	sessionprocess "github.com/flanksource/captain/pkg/session/process"
	clickyprocess "github.com/flanksource/clicky/process"
)

// BrowserLinkSource names how a browser session was attributed to an agent
// session. It is reported so an operator can tell a live inference from a
// record written when the browser was launched.
type BrowserLinkSource string

const (
	BrowserLinkNone     BrowserLinkSource = ""
	BrowserLinkEnv      BrowserLinkSource = "env"
	BrowserLinkSidecar  BrowserLinkSource = "sidecar"
	BrowserLinkAncestor BrowserLinkSource = "ancestor"
)

// unnamespacedDir stands in for the empty namespace so every record has a
// directory, and a session named "default" cannot collide with a namespace.
const unnamespacedDir = "_"

// BrowserLink is the agent session that owns a browser session.
type BrowserLink struct {
	Source         BrowserLinkSource       `json:"source,omitempty"`
	AgentSource    string                  `json:"agentSource,omitempty"`
	AgentSessionID string                  `json:"agentSessionId,omitempty"`
	AgentPID       int                     `json:"agentPid,omitempty"`
	CWD            string                  `json:"cwd,omitempty"`
	Surface        *sessionprocess.Surface `json:"surface,omitempty"`
}

// BrowserLinkInputs are the three independent identity signals, in the order
// resolveBrowserLink trusts them.
type BrowserLinkInputs struct {
	// Env is the daemon's own environment. A daemon inherits it from the CLI
	// invocation that started it, i.e. from the agent's shell, so this resolves
	// even for browsers started before captain's plugin was installed.
	Env map[string]string
	// Record is what captain's launch plugin wrote when the browser started.
	Record *browserLinkRecord
	// Tree is the host process snapshot, used to walk the daemon's ancestry.
	Tree *clickyprocess.Snapshot
}

// browserLinkRecord is the durable record captain's agent-browser launch plugin
// writes. agent-browser sessions carry no metadata of their own, so this file is
// the only place a launch-time fact can live.
type browserLinkRecord struct {
	BrowserSession  string          `json:"browserSession"`
	Namespace       string          `json:"namespace,omitempty"`
	SocketDir       string          `json:"socketDir,omitempty"`
	DaemonPID       int             `json:"daemonPid,omitempty"`
	DaemonStartedAt *time.Time      `json:"daemonStartedAt,omitempty"`
	AgentSource     string          `json:"agentSource,omitempty"`
	AgentSessionID  string          `json:"agentSessionId,omitempty"`
	ClaudePID       int             `json:"claudePid,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	LaunchedAt      time.Time       `json:"launchedAt"`
	PluginRequest   json.RawMessage `json:"pluginRequest,omitempty"`
}

// resolveBrowserLink attributes a browser session to the agent session that
// launched it. Each signal is tried in turn and the first that names a session
// wins; an unattributable session reports BrowserLinkNone rather than a guess.
func resolveBrowserLink(sidecar browserSidecar, inputs BrowserLinkInputs) BrowserLink {
	link := BrowserLink{Surface: sessionprocess.SurfaceFromEnvironment(inputs.Env)}
	if source, sessionID := agentIdentityFromEnv(inputs.Env); sessionID != "" {
		link.Source = BrowserLinkEnv
		link.AgentSource = source
		link.AgentSessionID = sessionID
		link.AgentPID = envInt(inputs.Env, "CLAUDE_PID")
		return link
	}
	if record := inputs.Record; record != nil && record.AgentSessionID != "" {
		link.Source = BrowserLinkSidecar
		link.AgentSource = record.AgentSource
		link.AgentSessionID = record.AgentSessionID
		link.AgentPID = record.ClaudePID
		link.CWD = record.CWD
		return link
	}
	return resolveBrowserLinkFromAncestry(link, sidecar, inputs.Tree)
}

// resolveBrowserLinkFromAncestry finds the nearest agent process above the
// daemon. agent-browser's daemon is spawned by the CLI invocation the agent ran,
// so the agent is normally a direct ancestor.
func resolveBrowserLinkFromAncestry(link BrowserLink, sidecar browserSidecar, tree *clickyprocess.Snapshot) BrowserLink {
	if tree == nil || sidecar.DaemonPID <= 0 {
		return link
	}
	for _, ancestor := range tree.Ancestors(sidecar.DaemonPID) {
		source := sessionprocess.Source(ancestor.Command)
		if source == "" {
			continue
		}
		link.Source = BrowserLinkAncestor
		link.AgentSource = source
		link.AgentSessionID = sessionprocess.SessionIDFromCommand(ancestor.Command)
		link.AgentPID = ancestor.PID
		return link
	}
	return link
}

// agentIdentityFromEnv reads the session id an agent exports into its own
// environment, and therefore into everything it launches.
func agentIdentityFromEnv(env map[string]string) (string, string) {
	return sessionprocess.IdentityFromEnvironment(env)
}

func envInt(env map[string]string, key string) int {
	value, err := strconv.Atoi(env[key])
	if err != nil {
		return 0
	}
	return value
}

// BrowserLinkStateDir is where captain keeps the records its launch plugin
// writes, alongside its other host state.
func BrowserLinkStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".captain", "browser"), nil
}

func browserLinkPath(stateDir, namespace, session string) string {
	if namespace == "" {
		namespace = unnamespacedDir
	}
	return filepath.Join(stateDir, namespace, session+".json")
}

func readBrowserLinkRecord(path string) (*browserLinkRecord, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record browserLinkRecord
	if err := json.Unmarshal(content, &record); err != nil {
		return nil, fmt.Errorf("read browser link record %s: %w", path, err)
	}
	return &record, nil
}

// writeBrowserLinkRecord replaces the record atomically, so a listing never
// reads a half-written link.
func writeBrowserLinkRecord(path string, record browserLinkRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".browser-link-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(append(content, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

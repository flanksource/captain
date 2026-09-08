package process

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
)

type Surface struct {
	SurfaceID   string `json:"surfaceId,omitempty"`
	SurfaceRef  string `json:"surfaceRef,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	TabID       string `json:"tabId,omitempty"`
	PanelID     string `json:"panelId,omitempty"`
	Port        int    `json:"port,omitempty"`
	AgentKind   string `json:"agentKind,omitempty"`
	SocketPath  string `json:"socketPath,omitempty"`
	ClaudePID   int    `json:"claudePid,omitempty"`
	Title       string `json:"title,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
}

type Agent struct {
	Source        string
	PID           int
	Status        string
	Active        bool
	CPUPercent    float64
	MemoryPercent float64
	RSSBytes      uint64
	StartedAt     *time.Time
	CWD           string
	Command       string
	Environment   map[string]string
	Surface       *Surface
}

func Discover(ctx context.Context) ([]Agent, error) {
	snapshot, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{
		Environment: &clickyprocess.EnvironmentOptions{
			Keys:     []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CODEX_THREAD_ID"},
			Prefixes: []string{"CMUX_", "AGENT_BROWSER_"},
		},
	})
	if err != nil {
		return nil, err
	}
	agents := FromSnapshot(snapshot)
	pids := make([]int, 0, len(agents))
	for _, agent := range agents {
		pids = append(pids, agent.PID)
	}
	snapshot.PopulateWorkingDirectories(ctx, pids)
	return FromSnapshot(snapshot), nil
}

func FromSnapshot(snapshot *clickyprocess.Snapshot) []Agent {
	if snapshot == nil {
		return nil
	}
	var agents []Agent
	for _, process := range snapshot.All() {
		source := Source(process.Command)
		if source == "" {
			continue
		}
		agents = append(agents, Agent{
			Source: source, PID: process.PID, Status: process.Status, Active: process.Active,
			CPUPercent: process.CPUPercent, MemoryPercent: process.MemoryPercent,
			RSSBytes:  process.RSSBytes,
			StartedAt: process.StartedAt, CWD: process.CWD, Command: process.Command,
			Environment: process.Environment, Surface: SurfaceFromEnvironment(process.Environment),
		})
	}
	return agents
}

func Source(command string) string {
	lower := strings.ToLower(command)
	switch executableName(lower) {
	case "captain", "ctop", "claude-manager":
		return ""
	}
	if strings.Contains(lower, "claude.app") || strings.Contains(lower, "codex.app") {
		return ""
	}
	if commandNameMatches(lower, "claude") {
		return "claude"
	}
	if strings.Contains(lower, "codex-darwin") || strings.Contains(lower, "codex-linux") ||
		strings.Contains(lower, "codex-win") || commandNameMatches(lower, "codex") {
		if commandNameMatches(lower, "mcp-server") || commandNameMatches(lower, "app-server") {
			return ""
		}
		return "codex"
	}
	return ""
}

func SessionIDFromCommand(command string) string {
	fields := strings.Fields(command)
	for index, field := range fields {
		for _, flag := range []string{"--session-id", "--resume"} {
			if field == flag && index+1 < len(fields) {
				return fields[index+1]
			}
			if value, found := strings.CutPrefix(field, flag+"="); found {
				return value
			}
		}
	}
	return ""
}

func IdentityFromEnvironment(environment map[string]string) (string, string) {
	if sessionID := environment["CLAUDE_CODE_SESSION_ID"]; sessionID != "" {
		return "claude", sessionID
	}
	if threadID := environment["CODEX_THREAD_ID"]; threadID != "" {
		return "codex", threadID
	}
	return "", ""
}

func SurfaceFromEnvironment(environment map[string]string) *Surface {
	surface := Surface{
		SurfaceID: environment["CMUX_SURFACE_ID"], WorkspaceID: environment["CMUX_WORKSPACE_ID"],
		TabID: environment["CMUX_TAB_ID"], PanelID: environment["CMUX_PANEL_ID"],
		AgentKind: environment["CMUX_AGENT_LAUNCH_KIND"], SocketPath: environment["CMUX_SOCKET_PATH"],
	}
	surface.Port, _ = strconv.Atoi(environment["CMUX_PORT"])
	surface.ClaudePID, _ = strconv.Atoi(environment["CMUX_CLAUDE_PID"])
	if surface == (Surface{}) {
		return nil
	}
	return &surface
}

func executableName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(filepath.Base(fields[0]), `"'`)
}

func commandNameMatches(command, name string) bool {
	for _, field := range strings.Fields(command) {
		base := strings.Trim(filepath.Base(field), `"'`)
		if base == name {
			return true
		}
	}
	return false
}

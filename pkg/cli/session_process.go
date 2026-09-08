package cli

import (
	"context"
	"time"

	sessionprocess "github.com/flanksource/captain/pkg/session/process"
)

type agentProcess struct {
	Source        string
	PID           int
	PPID          int
	Status        string
	Active        bool
	CPUPercent    float64
	MemoryPercent float64
	RSSBytes      uint64
	StartedAt     *time.Time
	CWD           string
	Command       string
	Environment   map[string]string
	SessionID     string
	AgentIDs      []string
	LastActivity  *time.Time
	SessionFile   string
	Surface       *CmuxSurface
}

func (p agentProcess) wire() *SessionLiveWire {
	return &SessionLiveWire{
		PID: p.PID, PPID: p.PPID, Status: p.Status, Active: p.Active,
		CPUPercent: p.CPUPercent, MemoryPercent: p.MemoryPercent, RSSBytes: p.RSSBytes,
		StartedAt: p.StartedAt, CWD: p.CWD, Command: p.Command, Environment: p.Environment,
		SessionID: p.SessionID, AgentIDs: p.AgentIDs,
		LastActivity: p.LastActivity, SessionFile: p.SessionFile, Surface: p.Surface,
	}
}

var (
	discoverSessionProcesses = discoverAgentProcesses
	inspectSessionProcesses  = inspectAgentProcesses
)

func discoverAgentProcesses() ([]agentProcess, error) {
	processes, err := sessionprocess.Discover(context.Background())
	if err != nil {
		return nil, err
	}
	return agentProcessesFrom(processes), nil
}

// inspectAgentProcesses reports the named PIDs whether or not they are agents,
// so `captain ps <pid>` can inspect an agent-browser daemon, an un-ingested
// agent, or any other process the user points at.
func inspectAgentProcesses(ctx context.Context, pids []int) ([]agentProcess, error) {
	processes, err := sessionprocess.Inspect(ctx, sessionprocess.InspectOptions{PIDs: pids})
	if err != nil {
		return nil, err
	}
	return agentProcessesFrom(processes), nil
}

func agentProcessesFrom(processes []sessionprocess.Agent) []agentProcess {
	result := make([]agentProcess, len(processes))
	for i, process := range processes {
		result[i] = agentProcess{
			Source: process.Source, PID: process.PID, PPID: process.PPID,
			Status: process.Status, Active: process.Active,
			CPUPercent: process.CPUPercent, MemoryPercent: process.MemoryPercent,
			RSSBytes:  process.RSSBytes,
			StartedAt: process.StartedAt, CWD: process.CWD, Command: process.Command,
			Environment: process.Environment, Surface: process.Surface,
		}
	}
	return result
}

func filterAgentProcessesByProject(processes []agentProcess, projectRoot string) []agentProcess {
	if projectRoot == "" {
		return processes
	}
	filtered := make([]agentProcess, 0, len(processes))
	for _, process := range processes {
		if sessionRecordMatchesProject(SessionRecord{CWD: process.CWD}, projectRoot) {
			filtered = append(filtered, process)
		}
	}
	return filtered
}

func processIDs(processes []agentProcess) []int {
	pids := make([]int, 0, len(processes))
	for _, process := range processes {
		if process.PID > 0 {
			pids = append(pids, process.PID)
		}
	}
	return pids
}

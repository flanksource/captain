package cli

import (
	"context"
	"time"

	sessionprocess "github.com/flanksource/captain/pkg/session/process"
)

type agentProcess struct {
	Source        string
	PID           int
	Status        string
	Active        bool
	CPUPercent    float64
	MemoryPercent float64
	StartedAt     *time.Time
	CWD           string
	Command       string
	SessionID     string
	AgentIDs      []string
	LastActivity  *time.Time
	SessionFile   string
	Surface       *CmuxSurface
}

func (p agentProcess) wire() *SessionLiveWire {
	return &SessionLiveWire{
		PID: p.PID, Status: p.Status, Active: p.Active,
		CPUPercent: p.CPUPercent, MemoryPercent: p.MemoryPercent,
		StartedAt: p.StartedAt, CWD: p.CWD, Command: p.Command,
		SessionID: p.SessionID, AgentIDs: p.AgentIDs,
		LastActivity: p.LastActivity, SessionFile: p.SessionFile, Surface: p.Surface,
	}
}

var discoverSessionProcesses = discoverAgentProcesses

func discoverAgentProcesses() ([]agentProcess, error) {
	processes, err := sessionprocess.Discover(context.Background())
	if err != nil {
		return nil, err
	}
	result := make([]agentProcess, len(processes))
	for i, process := range processes {
		result[i] = agentProcess{
			Source: process.Source, PID: process.PID, Status: process.Status, Active: process.Active,
			CPUPercent: process.CPUPercent, MemoryPercent: process.MemoryPercent,
			StartedAt: process.StartedAt, CWD: process.CWD, Command: process.Command, Surface: process.Surface,
		}
	}
	return result, nil
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

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
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
}

func (p agentProcess) wire() *SessionLiveWire {
	return &SessionLiveWire{
		PID:           p.PID,
		Status:        p.Status,
		Active:        p.Active,
		CPUPercent:    p.CPUPercent,
		MemoryPercent: p.MemoryPercent,
		StartedAt:     p.StartedAt,
		CWD:           p.CWD,
		Command:       p.Command,
	}
}

func discoverAgentProcesses() ([]agentProcess, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-eo", "pid=,pcpu=,pmem=,stat=,lstart=,command=").Output()
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(out, []byte{'\n'})
	processes := make([]agentProcess, 0)
	for _, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		proc, ok := parseAgentProcessLine(line)
		if !ok {
			continue
		}
		processes = append(processes, proc)
	}
	cwds := processCWDs(processIDs(processes))
	for i := range processes {
		processes[i].CWD = cwds[processes[i].PID]
	}
	return processes, nil
}

func parseAgentProcessLine(line string) (agentProcess, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return agentProcess{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return agentProcess{}, false
	}
	command := strings.Join(fields[9:], " ")
	source := processSource(command)
	if source == "" {
		return agentProcess{}, false
	}
	cpu, _ := strconv.ParseFloat(fields[1], 64)
	mem, _ := strconv.ParseFloat(fields[2], 64)
	stat := fields[3]
	start := parseProcessStart(strings.Join(fields[4:9], " "))
	status, active := processStatus(stat)
	return agentProcess{
		Source:        source,
		PID:           pid,
		Status:        status,
		Active:        active,
		CPUPercent:    cpu,
		MemoryPercent: mem,
		StartedAt:     start,
		Command:       command,
	}, true
}

func processSource(command string) string {
	lower := strings.ToLower(command)
	if strings.Contains(lower, "captain") || strings.Contains(lower, "ctop") || strings.Contains(lower, "claude-manager") {
		return ""
	}
	if strings.Contains(lower, "claude.app") {
		return ""
	}
	if commandNameMatches(lower, "claude") {
		return "claude"
	}
	if strings.Contains(lower, "codex-darwin") ||
		strings.Contains(lower, "codex-linux") ||
		strings.Contains(lower, "codex-win") ||
		commandNameMatches(lower, "codex") {
		return "codex"
	}
	return ""
}

func commandNameMatches(command, name string) bool {
	fields := strings.Fields(command)
	for _, field := range fields {
		base := field
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		base = strings.Trim(base, `"'`)
		if base == name {
			return true
		}
	}
	return false
}

func processStatus(stat string) (string, bool) {
	switch {
	case strings.Contains(stat, "Z"):
		return "zombie", false
	case strings.Contains(stat, "T"):
		return "stopped", false
	case strings.Contains(stat, "S"):
		return "sleeping", true
	default:
		return "active", true
	}
}

func parseProcessStart(value string) *time.Time {
	if value == "" {
		return nil
	}
	t, err := time.Parse("Mon Jan 2 15:04:05 2006", value)
	if err != nil {
		return nil
	}
	return &t
}

func processIDs(processes []agentProcess) []int {
	pids := make([]int, 0, len(processes))
	for _, proc := range processes {
		if proc.PID > 0 {
			pids = append(pids, proc.PID)
		}
	}
	return pids
}

func processCWDs(pids []int) map[int]string {
	cwds := make(map[int]string, len(pids))
	if runtime.GOOS == "linux" {
		for _, pid := range pids {
			if pid <= 0 {
				continue
			}
			cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
			if err == nil {
				cwds[pid] = cwd
			}
		}
		return cwds
	}
	var pidList []string
	for _, pid := range pids {
		if pid > 0 {
			pidList = append(pidList, strconv.Itoa(pid))
		}
	}
	if len(pidList) == 0 {
		return cwds
	}
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "pn", "-p", strings.Join(pidList, ",")).Output()
	if err != nil {
		return cwds
	}
	return parseLsofCWDs(out)
}

func parseLsofCWDs(out []byte) map[int]string {
	cwds := make(map[int]string)
	currentPID := 0
	for _, raw := range bytes.Split(out, []byte{'\n'}) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err == nil {
				currentPID = pid
			}
		case 'n':
			if currentPID > 0 {
				cwds[currentPID] = strings.TrimPrefix(line, "n")
			}
		}
	}
	return cwds
}

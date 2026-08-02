package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/cmux"
)

type CmuxInfoOptions struct {
	Input []string `args:"true" stdin:"true" help:"cmux Copy IDs lines or a process ID"`
	Stack bool     `flag:"stack" help:"Capture goroutine stacks from Go processes with gops"`
}

type cmuxInfoSelector struct {
	PID      int
	Selector cmux.Selector
}

type CmuxInfoResult struct {
	Target    CmuxInfoTarget `json:"target"`
	Processes []CmuxProcess  `json:"processes"`
}

type CmuxInfoTarget struct {
	Kind     string         `json:"kind"`
	PID      int            `json:"pid,omitempty"`
	Selector *cmux.Selector `json:"selector,omitempty"`
}

type CmuxProcess struct {
	PID             int                    `json:"pid"`
	PPID            int                    `json:"ppid,omitempty"`
	Name            string                 `json:"name,omitempty"`
	Executable      string                 `json:"executable,omitempty"`
	Command         string                 `json:"command,omitempty"`
	Runtime         string                 `json:"runtime,omitempty"`
	CPUPercent      float64                `json:"cpu_percent"`
	RSSBytes        uint64                 `json:"rss_bytes"`
	Listeners       []CmuxListener         `json:"listeners"`
	Locations       []cmux.ProcessLocation `json:"locations"`
	Stack           string                 `json:"stack,omitempty"`
	StackError      string                 `json:"stack_error,omitempty"`
	InspectionError string                 `json:"inspection_error,omitempty"`
}

type CmuxListener struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     uint32 `json:"port"`
}

var (
	loadCmuxTop    = cmux.Top
	inspectCmuxPID = inspectProcess
	loadGoStack    = captureGoStack
)

func parseCmuxInfoSelector(input []string) (cmuxInfoSelector, error) {
	lines := splitCmuxInfoInput(input)
	if len(lines) == 0 {
		return cmuxInfoSelector{}, fmt.Errorf("cmux info requires cmux Copy IDs lines or a PID")
	}

	parsed := cmuxInfoSelector{}
	values := map[string]*string{
		"workspace_id": &parsed.Selector.WorkspaceID, "workspace_ref": &parsed.Selector.WorkspaceRef,
		"pane_id": &parsed.Selector.PaneID, "pane_ref": &parsed.Selector.PaneRef,
		"surface_id": &parsed.Selector.SurfaceID, "surface_ref": &parsed.Selector.SurfaceRef,
	}
	for _, line := range lines {
		if !strings.Contains(line, "=") {
			if err := parseCmuxInfoPID(line, &parsed); err != nil {
				return cmuxInfoSelector{}, err
			}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		destination, ok := values[strings.TrimSpace(parts[0])]
		if !ok {
			return cmuxInfoSelector{}, fmt.Errorf("unknown cmux selector key %q", strings.TrimSpace(parts[0]))
		}
		value := strings.TrimSpace(parts[1])
		if value == "" {
			return cmuxInfoSelector{}, fmt.Errorf("cmux selector %s is empty", strings.TrimSpace(parts[0]))
		}
		if *destination != "" && *destination != value {
			return cmuxInfoSelector{}, fmt.Errorf("conflicting %s values %q and %q", strings.TrimSpace(parts[0]), *destination, value)
		}
		*destination = value
	}
	if parsed.PID > 0 && parsed.Selector.Kind() != "" {
		return cmuxInfoSelector{}, fmt.Errorf("cannot mix a PID with cmux selector lines")
	}
	if parsed.PID == 0 && parsed.Selector.Kind() == "" {
		return cmuxInfoSelector{}, fmt.Errorf("cmux info requires cmux Copy IDs lines or a PID")
	}
	return parsed, nil
}

func splitCmuxInfoInput(input []string) []string {
	var lines []string
	for _, value := range input {
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func parseCmuxInfoPID(value string, parsed *cmuxInfoSelector) error {
	pid, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("invalid cmux selector line %q", value)
	}
	if pid <= 0 {
		return fmt.Errorf("PID must be positive, got %d", pid)
	}
	if parsed.PID != 0 && parsed.PID != pid {
		return fmt.Errorf("multiple PIDs are not supported: %d and %d", parsed.PID, pid)
	}
	parsed.PID = pid
	return nil
}

func RunCmuxInfo(ctx context.Context, opts CmuxInfoOptions) (CmuxInfoResult, error) {
	selector, err := parseCmuxInfoSelector(opts.Input)
	if err != nil {
		return CmuxInfoResult{}, err
	}
	target, pids, locations, err := resolveCmuxInfoTarget(selector)
	if err != nil {
		return CmuxInfoResult{}, err
	}

	result := CmuxInfoResult{Target: target, Processes: make([]CmuxProcess, 0, len(pids))}
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return CmuxInfoResult{}, err
		}
		row, inspectErr := inspectCmuxPID(ctx, pid)
		row.PID = pid
		row.Locations = locations[pid]
		if row.Locations == nil {
			row.Locations = []cmux.ProcessLocation{}
		}
		if inspectErr != nil {
			row.InspectionError = inspectErr.Error()
		}
		if opts.Stack && row.Runtime == "go" {
			stackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			row.Stack, err = loadGoStack(stackCtx, pid)
			cancel()
			if err != nil {
				row.StackError = err.Error()
			}
		}
		result.Processes = append(result.Processes, row)
	}
	return result, nil
}

func resolveCmuxInfoTarget(selector cmuxInfoSelector) (CmuxInfoTarget, []int, map[int][]cmux.ProcessLocation, error) {
	if selector.PID > 0 {
		return CmuxInfoTarget{Kind: "pid", PID: selector.PID}, []int{selector.PID}, nil, nil
	}
	snapshot, err := loadCmuxTop()
	if err != nil {
		return CmuxInfoTarget{}, nil, nil, err
	}
	resolved, err := snapshot.Resolve(selector.Selector)
	if err != nil {
		return CmuxInfoTarget{}, nil, nil, err
	}
	if len(resolved.PIDs) == 0 {
		return CmuxInfoTarget{}, nil, nil, fmt.Errorf("cmux %s target has no running processes", resolved.Kind)
	}
	for _, pid := range resolved.PIDs {
		if pid <= 0 {
			return CmuxInfoTarget{}, nil, nil, fmt.Errorf("cmux %s target returned invalid PID %d", resolved.Kind, pid)
		}
	}
	sort.Ints(resolved.PIDs)
	return CmuxInfoTarget{Kind: resolved.Kind, Selector: &selector.Selector}, resolved.PIDs, resolved.Locations, nil
}

package cli

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	gopsnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

func inspectProcess(ctx context.Context, pid int) (CmuxProcess, error) {
	row := CmuxProcess{PID: pid, Listeners: []CmuxListener{}}
	proc, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return row, fmt.Errorf("open process %d: %w", pid, err)
	}

	var issues []error
	if value, err := proc.PpidWithContext(ctx); err != nil {
		issues = append(issues, fmt.Errorf("read parent PID: %w", err))
	} else {
		row.PPID = int(value)
	}
	if row.Name, err = proc.NameWithContext(ctx); err != nil {
		issues = append(issues, fmt.Errorf("read process name: %w", err))
	}
	if row.Executable, err = proc.ExeWithContext(ctx); err != nil {
		issues = append(issues, fmt.Errorf("read executable: %w", err))
	}
	if row.Command, err = proc.CmdlineWithContext(ctx); err != nil {
		issues = append(issues, fmt.Errorf("read command: %w", err))
	}
	if row.CPUPercent, err = proc.CPUPercentWithContext(ctx); err != nil {
		issues = append(issues, fmt.Errorf("read CPU usage: %w", err))
	}
	if memory, memoryErr := proc.MemoryInfoWithContext(ctx); memoryErr != nil {
		issues = append(issues, fmt.Errorf("read memory usage: %w", memoryErr))
	} else {
		row.RSSBytes = memory.RSS
	}
	if connections, connectionsErr := proc.ConnectionsWithContext(ctx); connectionsErr != nil {
		issues = append(issues, fmt.Errorf("read network listeners: %w", connectionsErr))
	} else {
		row.Listeners = listeningTCPEndpoints(connections)
	}
	row.Runtime = detectProcessRuntime(row.Executable, row.Name)
	return row, errors.Join(issues...)
}

func detectProcessRuntime(executable, name string) string {
	if executable != "" {
		if _, err := buildinfo.ReadFile(executable); err == nil {
			return "go"
		}
	}
	if executable == "" && name == "" {
		return "unknown"
	}
	binary := strings.ToLower(filepath.Base(executable))
	if binary == "" {
		binary = strings.ToLower(name)
	}
	binary = strings.TrimSuffix(binary, ".exe")
	switch {
	case binary == "node" || strings.HasPrefix(binary, "nodejs"):
		return "node"
	case binary == "bun":
		return "bun"
	case binary == "deno":
		return "deno"
	case strings.HasPrefix(binary, "python"):
		return "python"
	case binary == "java":
		return "java"
	case strings.HasPrefix(binary, "ruby"):
		return "ruby"
	case isShell(binary):
		return "shell"
	default:
		return "native"
	}
}

func captureGoStack(ctx context.Context, pid int) (string, error) {
	gops, err := gopsBinary()
	if err != nil {
		return "", err
	}
	output, err := exec.CommandContext(ctx, gops, "stack", strconv.Itoa(pid)).CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("gops stack %d: %w", pid, ctx.Err())
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return "", fmt.Errorf("gops stack %d: %w: %s", pid, err, detail)
	}
	return "", fmt.Errorf("gops stack %d: %w", pid, err)
}

func isShell(binary string) bool {
	switch binary {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh":
		return true
	default:
		return false
	}
}

func listeningTCPEndpoints(connections []gopsnet.ConnectionStat) []CmuxListener {
	listeners := make([]CmuxListener, 0)
	for _, connection := range connections {
		if connection.Type != syscall.SOCK_STREAM || !strings.EqualFold(connection.Status, "LISTEN") {
			continue
		}
		protocol := "tcp4"
		if strings.Contains(connection.Laddr.IP, ":") {
			protocol = "tcp6"
		}
		listeners = append(listeners, CmuxListener{Protocol: protocol, Address: connection.Laddr.IP, Port: connection.Laddr.Port})
	}
	sort.Slice(listeners, func(i, j int) bool {
		return listeners[i].String() < listeners[j].String()
	})
	return listeners
}

func gopsBinary() (string, error) {
	if configured := os.Getenv("GOPS_BIN"); configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("find GOPS_BIN %q: %w", configured, err)
		}
		return path, nil
	}
	if path, err := exec.LookPath("gops"); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find gops: %w", err)
	}
	path, err := exec.LookPath(filepath.Join(home, "go", "bin", "gops"))
	if err != nil {
		return "", fmt.Errorf("find gops: %w", err)
	}
	return path, nil
}

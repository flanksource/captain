package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type PortKillOptions struct {
	Port int `args:"true" help:"TCP port number to kill" required:"true"`
}

type PortKillResult struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
	Killed  bool   `json:"killed"`
}

func RunPortKill(opts PortKillOptions) (any, error) {
	pid, err := findPIDOnPort(opts.Port)
	if err != nil {
		return nil, err
	}

	process := processInfo(pid)
	fmt.Fprintf(os.Stderr, "Port %d: PID %d (%s)\n", opts.Port, pid, process)

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return nil, fmt.Errorf("failed to kill PID %d: %w", pid, err)
	}

	time.Sleep(300 * time.Millisecond)

	if _, err := findPIDOnPort(opts.Port); err == nil {
		return nil, fmt.Errorf("kill sent but port %d is still bound", opts.Port)
	}

	fmt.Fprintf(os.Stderr, "Killed successfully — port %d is free\n", opts.Port)
	return PortKillResult{Port: opts.Port, PID: pid, Process: process, Killed: true}, nil
}

func findPIDOnPort(port int) (int, error) {
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0, fmt.Errorf("no process found on port %d", port)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return 0, fmt.Errorf("no process found on port %d", port)
	}
	return strconv.Atoi(lines[0])
}

func processInfo(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

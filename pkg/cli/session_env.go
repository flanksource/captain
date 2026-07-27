package cli

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// cmuxEnvPattern matches a CMUX_* environment assignment in the flattened
// `ps -Eww` output on macOS. CMUX values are single whitespace-free tokens
// (ids, ports, paths), so this reliably skips decoy references inside the
// argv (e.g. `${CMUX_CLAUDE_HOOK_CMUX_BIN:-cmux}`, which uses ':' not '=').
var cmuxEnvPattern = regexp.MustCompile(`\bCMUX_[A-Z0-9_]+=\S+`)

// processSurface returns the cmux surface hosting the process, or nil when the
// process has no CMUX_* environment (not launched under cmux) or its environment
// cannot be read. Enrichment is best-effort and never fails the caller.
func processSurface(pid int) *CmuxSurface {
	if pid <= 0 {
		return nil
	}
	env, err := processEnviron(pid)
	if err != nil {
		return nil
	}
	return parseCmuxSurface(env)
}

// processEnviron reads another process's environment. On Linux it reads the
// NUL-separated /proc/<pid>/environ (full environment). On macOS/other it shells
// out to `ps -Eww` and extracts only the CMUX_* assignments (the full argv+env
// blob cannot be split reliably, but the CMUX subset is unambiguous).
func processEnviron(pid int) (map[string]string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			return nil, err
		}
		return parseProcEnviron(data), nil
	}
	out, err := exec.Command("ps", "-Eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil, err
	}
	return extractCmuxEnv(string(out)), nil
}

// parseProcEnviron parses NUL-separated KEY=VALUE pairs from /proc/<pid>/environ.
func parseProcEnviron(data []byte) map[string]string {
	env := make(map[string]string)
	for _, pair := range bytes.Split(data, []byte{0}) {
		if len(pair) == 0 {
			continue
		}
		key, value, ok := strings.Cut(string(pair), "=")
		if !ok || key == "" {
			continue
		}
		env[key] = value
	}
	return env
}

// extractCmuxEnv pulls CMUX_* assignments out of a flattened `ps -Eww` line.
func extractCmuxEnv(psOutput string) map[string]string {
	env := make(map[string]string)
	for _, match := range cmuxEnvPattern.FindAllString(psOutput, -1) {
		key, value, ok := strings.Cut(match, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}

// parseCmuxSurface builds a CmuxSurface from an environment map, returning nil
// when no CMUX surface markers are present.
func parseCmuxSurface(env map[string]string) *CmuxSurface {
	surface := CmuxSurface{
		SurfaceID:   env["CMUX_SURFACE_ID"],
		WorkspaceID: env["CMUX_WORKSPACE_ID"],
		TabID:       env["CMUX_TAB_ID"],
		PanelID:     env["CMUX_PANEL_ID"],
		AgentKind:   env["CMUX_AGENT_LAUNCH_KIND"],
		SocketPath:  env["CMUX_SOCKET_PATH"],
	}
	if port, err := strconv.Atoi(env["CMUX_PORT"]); err == nil {
		surface.Port = port
	}
	if pid, err := strconv.Atoi(env["CMUX_CLAUDE_PID"]); err == nil {
		surface.ClaudePID = pid
	}
	if surface == (CmuxSurface{}) {
		return nil
	}
	return &surface
}

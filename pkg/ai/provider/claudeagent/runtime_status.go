package claudeagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flanksource/captain/pkg/ai"
)

type runtimeProbeOptions struct {
	cacheDir string
	lookPath func(string) (string, error)
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}

// ProbeRuntime mirrors newAgentProcess without materialising files or
// installing dependencies during a readiness check.
func ProbeRuntime() ai.RuntimeStatus {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ai.RuntimeStatus{Error: err.Error()}
	}
	return probeRuntimeStatus(runtimeProbeOptions{
		cacheDir: cacheDir,
		lookPath: exec.LookPath,
		readFile: os.ReadFile,
		stat:     os.Stat,
	})
}

func probeRuntimeStatus(options runtimeProbeOptions) ai.RuntimeStatus {
	agentDir := filepath.Join(options.cacheDir, "captain", "claude-agent")
	current, err := dependenciesCurrent(options.readFile, agentDir)
	if err != nil {
		return ai.RuntimeStatus{Error: err.Error()}
	}
	if !current {
		if npm, pathErr := options.lookPath("npm"); pathErr == nil {
			return ai.RuntimeStatus{Provisioner: npm}
		}
		return ai.RuntimeStatus{DependencyMissing: "npm"}
	}
	localTsx := filepath.Join(agentDir, "node_modules", ".bin", "tsx")
	if _, statErr := options.stat(localTsx); statErr == nil {
		return ai.RuntimeStatus{Binary: localTsx}
	}
	if tsx, pathErr := options.lookPath("tsx"); pathErr == nil {
		return ai.RuntimeStatus{Binary: tsx}
	}
	return ai.RuntimeStatus{DependencyMissing: "tsx"}
}

func dependenciesCurrent(readFile func(string) ([]byte, error), agentDir string) (bool, error) {
	required, err := requiredSDKVersion()
	if err != nil {
		return false, err
	}
	data, err := readFile(filepath.Join(agentDir, "node_modules", "@anthropic-ai", "claude-agent-sdk", "package.json"))
	if err != nil {
		return false, nil
	}
	var installed struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &installed) != nil {
		return false, nil
	}
	return installed.Version == required, nil
}

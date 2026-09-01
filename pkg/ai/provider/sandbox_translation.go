package provider

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// permissionMode is the run's posture. It comes from permissions, not from the
// sandbox: the isolation boundary and whether the agent asks before acting vary
// independently, so a run with no sandbox can still ask for plan.
func permissionMode(req ai.Request) api.PermissionMode {
	return req.Permissions.Mode
}

func validatePermissionMode(runtime api.Runtime, mode api.PermissionMode) error {
	if mode == "" || api.PermissionCapabilitiesFor(runtime).ModeSupport(mode).Honoured() {
		return nil
	}
	return fmt.Errorf("permissions.mode %q is not supported by %s", mode, runtime)
}

func claudeSettingsDocument(req ai.Request, monitorBinary string) ([]byte, error) {
	settings := map[string]any{}
	if req.Sandbox != nil {
		sandbox, err := api.TranslateClaudeSandbox(api.RuntimeOf(api.Anthropic, api.ModeCLI), *req.Sandbox)
		if err != nil {
			return nil, err
		}
		settings["sandbox"] = sandbox
	}
	if monitorBinary != "" {
		monitor, err := claudeMonitorSettings(monitorBinary)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(monitor, &settings); err != nil {
			return nil, fmt.Errorf("merge Claude monitor settings: %w", err)
		}
	}
	if len(settings) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode Claude settings: %w", err)
	}
	return data, nil
}

// translateCodexSandbox takes the whole request rather than the sandbox alone so
// the isolation boundary and the posture cannot be passed as a mismatched pair.
func translateCodexSandbox(runtime api.Runtime, req ai.Request) (api.CodexSandboxTranslation, error) {
	return api.TranslateCodexSandbox(runtime, req.Sandbox, req.Permissions.Mode)
}

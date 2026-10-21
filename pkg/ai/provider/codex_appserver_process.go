package provider

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/exec"
)

func newCodexAppServerProcess(cfg ai.Config) *exec.Process {
	args := []string{"app-server", "-c", fmt.Sprintf("tools.experimental_request_user_input.enabled=%t", cfg.OnApproval != nil)}
	args = append(args, "-c", "features.default_mode_request_user_input=false")
	if cfg.APIURL != "" {
		args = append(args, codexProviderOverride(cfg.APIURL)...)
	}
	process := exec.NewExec("codex", args...)
	if cfg.APIKey != "" {
		process.WithEnv(map[string]string{"OPENAI_API_KEY": cfg.APIKey})
	}
	return process
}

func appServerProcessError(err error, stderr string) error {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

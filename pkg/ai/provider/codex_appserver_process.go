package provider

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/codexhome"
	"github.com/flanksource/clicky/exec"
)

// newCodexAppServerProcess builds the app-server command. codexHome, when set,
// is the isolated home a read-only run starts in (see codexRunHome).
func newCodexAppServerProcess(cfg ai.Config, codexHome string) *exec.Process {
	args := []string{"app-server", "-c", fmt.Sprintf("tools.experimental_request_user_input.enabled=%t", cfg.OnApproval != nil)}
	args = append(args, "-c", "features.default_mode_request_user_input=false")
	if cfg.APIURL != "" {
		args = append(args, codexProviderOverride(cfg.APIURL)...)
	}
	process := exec.NewExec("codex", args...)
	if cfg.APIKey != "" {
		process.WithEnv(map[string]string{"OPENAI_API_KEY": cfg.APIKey})
	}
	if codexHome != "" {
		process.WithEnv(map[string]string{"CODEX_HOME": codexHome})
	}
	return process
}

// codexRunHome is the CODEX_HOME a codex process that cannot pass
// --ignore-rules runs in. Execpolicy allow rules run a matching command
// outside the sandbox even under read-only with approval never, so a
// read-only run starts in captain's isolated home, which has none.
func codexRunHome(req ai.Request) (string, error) {
	return codexhome.ForRun(req.Permissions.EffectiveAccess().ReadOnly(), req.Cwd())
}

func appServerProcessError(err error, stderr string) error {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

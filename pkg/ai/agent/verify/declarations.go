package verify

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

type DeclarationOptions struct {
	// Provider is authoritative when supplied; Model names a provider not built yet.
	Provider ai.Provider
	Model    string
}

// ValidateDeclarations inspects verifier wiring and judge files without invoking
// registered factories, executing commands, or constructing a provider.
func ValidateDeclarations(wf *api.Workflow, opts DeclarationOptions) error {
	if wf == nil || wf.Verify == nil {
		return nil
	}
	if strings.TrimSpace(wf.Verify.Fixture) != "" && !Registered(KindFixture) {
		return fmt.Errorf("workflow.verify.fixture declared but no fixture verifier is registered " +
			"(link a fixture runner in-process or set verify.fixtureRunner in ~/.captain.yaml)")
	}
	model := opts.Model
	if opts.Provider != nil {
		model = opts.Provider.GetModel()
	}
	for i, path := range wf.Verify.Prompts {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("workflow.verify.prompts[%d] is empty", i)
		}
		tmpl, err := prompt.LoadFile(path)
		if err != nil {
			return fmt.Errorf("verify prompt %q: %w", path, err)
		}
		if err := rejectJudgeOverrides(path, tmpl, model); err != nil {
			return err
		}
	}
	return nil
}

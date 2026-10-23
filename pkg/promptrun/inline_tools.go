package promptrun

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

// bindInlineTools turns the spec's frontmatter-declared tools into caller tools
// on Config.Tools, so admission, approval binding and every provider see them
// exactly like a host's own. The handlers run in the run's workspace, which the
// returned hook refreshes once setup has relocated it; they are confined by the
// same wrapper as the workflow's verify commands.
func bindInlineTools(in *Input) error {
	declared := in.Resolved.Spec.Tools
	if len(declared) == 0 {
		return nil
	}
	if in.Provider != nil {
		return fmt.Errorf("promptrun: the spec declares inline tools, but a supplied Provider owns its own tool wiring and would never expose them")
	}
	workspace := exectool.NewWorkspace(in.Resolved.Spec.Cwd(), in.Resolved.Spec.Setup)
	definitions, err := exectool.Definitions(declared, workspace, shell.WrapFunc(in.Verify.Wrap))
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	for _, existing := range in.Config.Tools {
		if _, clash := declared[existing.Name]; clash {
			return fmt.Errorf("promptrun: inline tool %q collides with a caller tool the host supplies", existing.Name)
		}
	}
	in.Config.Tools = append(append([]api.ToolDefinition(nil), in.Config.Tools...), definitions...)
	in.inlineTools = workspace
	return nil
}

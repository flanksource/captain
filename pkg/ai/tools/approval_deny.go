package tools

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// ApprovalDenial is the deny pre-check every provider runs before an approval
// reaches the callback or the broker. A request the run's policy resolves to
// deny is refused here, so no approval, grant or scope can override it and the
// host is never asked. An elicitation asks for input rather than running a
// tool, so no tool policy applies to it.
func ApprovalDenial(opts ResolveOptions, req api.ApprovalRequest) (reason string, denied bool, err error) {
	if req.Kind == api.ApprovalKindElicitation {
		return "", false, nil
	}
	resolver, err := opts.Resolver()
	if err != nil {
		return "", false, err
	}
	info := ToolInfo{Name: req.Tool}
	if req.Info != nil {
		info = *req.Info
	}
	policy, err := resolver.Resolve(info)
	if err != nil {
		return "", false, err
	}
	if policy != ToolPolicyDeny {
		return "", false, nil
	}
	return fmt.Sprintf("tool %q is denied by the run's permission policy", info.Name), true, nil
}

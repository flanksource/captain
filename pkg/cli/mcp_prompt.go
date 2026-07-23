package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	clickymcp "github.com/flanksource/clicky/mcp"
)

// ResolveMCPPromptRestrictions converts Captain's prompt permission model into
// the portable policy used by clicky's offline MCP shortcuts.
func ResolveMCPPromptRestrictions(path string) (clickymcp.PromptRestrictions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return clickymcp.PromptRestrictions{}, fmt.Errorf("read prompt %s: %w", path, err)
	}
	document, err := prompt.Parse(string(data))
	if err != nil {
		return clickymcp.PromptRestrictions{}, fmt.Errorf("parse prompt %s: %w", path, err)
	}
	if err := document.Spec.Permissions.Validate(); err != nil {
		return clickymcp.PromptRestrictions{}, fmt.Errorf("validate prompt %s permissions: %w", path, err)
	}
	permissions := document.Spec.Permissions
	restrictions := clickymcp.PromptRestrictions{
		Name:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Disabled: permissions.MCP.Disabled,
	}

	servers := append([]string(nil), permissions.MCP.Servers...)
	var explicitlyEnabled []string
	for name, mode := range permissions.MCP.Modes {
		switch mode {
		case api.ResourceEnabled:
			explicitlyEnabled = append(explicitlyEnabled, name)
		case api.ResourceDisabled:
			restrictions.DisabledServers = append(restrictions.DisabledServers, name)
		}
	}
	if len(servers) > 0 {
		servers = append(servers, explicitlyEnabled...)
	} else if len(explicitlyEnabled) > 0 {
		servers = explicitlyEnabled
	}
	restrictions.Servers = uniqueSorted(servers)
	restrictions.DisabledServers = uniqueSorted(restrictions.DisabledServers)

	for name, policy := range permissions.Tools.Policies() {
		if !strings.HasPrefix(name, "mcp__") {
			continue
		}
		switch policy {
		case api.ToolPolicyDeny:
			restrictions.DenyTools = append(restrictions.DenyTools, name)
		case api.ToolPolicyAllow, api.ToolPolicyAuto, api.ToolPolicyAsk:
			restrictions.AllowTools = append(restrictions.AllowTools, name)
		}
	}
	sort.Strings(restrictions.AllowTools)
	sort.Strings(restrictions.DenyTools)
	return restrictions, nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
)

// codexPermissionProfile is the RequestPermissionProfile of
// item/permissions/requestApproval.
type codexPermissionProfile struct {
	FileSystem *struct {
		Entries []codexFileSystemEntry `json:"entries"`
		// Read and Write are the legacy spelling of Entries, still sent.
		Read  []string `json:"read"`
		Write []string `json:"write"`
	} `json:"fileSystem"`
	Network *struct {
		Enabled *bool `json:"enabled"`
	} `json:"network"`
}

type codexFileSystemEntry struct {
	Access string `json:"access"`
	Path   struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Value   *struct {
			Kind    string  `json:"kind"`
			Subpath *string `json:"subpath"`
		} `json:"value"`
	} `json:"path"`
}

func codexNoGrant() map[string]any {
	return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
}

// buildPermissionsApproval asks for the grant Codex requested, expressed in the
// spec's sandbox vocabulary with every path resolved to a concrete root.
func (p codexPosture) buildPermissionsApproval(req jsonrpc.ServerRequest, _ *turnState) (codexApproval, error) {
	var params struct {
		ItemID      string                 `json:"itemId"`
		Cwd         string                 `json:"cwd"`
		Reason      *string                `json:"reason"`
		Permissions codexPermissionProfile `json:"permissions"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return codexApproval{}, fmt.Errorf("codex permissions approval: %w", err)
	}
	if params.ItemID == "" || params.Cwd == "" {
		return codexApproval{}, fmt.Errorf("codex permissions approval needs an itemId and a cwd")
	}
	requested, err := p.requestedPermissions(params.Permissions, params.Cwd)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex permissions approval %s: %w", params.ItemID, err)
	}
	input, err := codexInput(req.Params)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex permissions approval: %w", err)
	}
	return codexApproval{request: api.ApprovalRequest{
		Tool: "request_permissions", Input: input, ToolUseID: params.ItemID, Kind: api.ApprovalKindPermissions,
		Reason: stringValue(params.Reason), Escalates: true,
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeTurn, api.ApprovalScopeSession},
		Info:            &api.ToolInfo{Name: "request_permissions"}, Permissions: &requested,
	}, answer: func(decision api.ApprovalDecision) (any, error) {
		return p.permissionsAnswer(decision, requested)
	}}, nil
}

func (p codexPosture) requestedPermissions(profile codexPermissionProfile, cwd string) (api.NativeSandboxPolicy, error) {
	var requested api.NativeSandboxPolicy
	filesystem := &api.SandboxFilesystemPolicy{}
	if fs := profile.FileSystem; fs != nil {
		for _, entry := range fs.Entries {
			roots, err := p.entryRoots(entry, cwd)
			if err != nil {
				return requested, err
			}
			switch entry.Access {
			case "write":
				filesystem.WritableRoots = append(filesystem.WritableRoots, roots...)
			case "read":
				filesystem.ReadableRoots = append(filesystem.ReadableRoots, roots...)
			default:
				return requested, fmt.Errorf("entry for %v asks for access %q; only read and write can be granted", roots, entry.Access)
			}
		}
		for _, path := range fs.Write {
			filesystem.WritableRoots = append(filesystem.WritableRoots, resolveAgainst(cwd, path))
		}
		for _, path := range fs.Read {
			filesystem.ReadableRoots = append(filesystem.ReadableRoots, resolveAgainst(cwd, path))
		}
	}
	if len(filesystem.WritableRoots) > 0 || len(filesystem.ReadableRoots) > 0 {
		requested.Filesystem = filesystem
	}
	if network := profile.Network; network != nil && network.Enabled != nil && *network.Enabled {
		requested.Network = &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted}
	}
	return requested, nil
}

// entryRoots resolves one requested path to the concrete roots it names. A glob
// or a special path with no fixed location is refused: granting it would grant
// something the host never saw.
func (p codexPosture) entryRoots(entry codexFileSystemEntry, cwd string) ([]string, error) {
	switch entry.Path.Type {
	case "path":
		return []string{resolveAgainst(cwd, entry.Path.Path)}, nil
	case "glob_pattern":
		if strings.ContainsAny(entry.Path.Pattern, `*?[\`) {
			return nil, fmt.Errorf("entry names glob %q, which does not resolve to concrete roots", entry.Path.Pattern)
		}
		return []string{resolveAgainst(cwd, entry.Path.Pattern)}, nil
	case "special":
		return p.specialRoots(entry, cwd)
	default:
		return nil, fmt.Errorf("entry has unknown path type %q", entry.Path.Type)
	}
}

func (p codexPosture) specialRoots(entry codexFileSystemEntry, cwd string) ([]string, error) {
	special := entry.Path.Value
	if special == nil {
		return nil, fmt.Errorf("special path entry carries no value")
	}
	switch special.Kind {
	case "root":
		return []string{string(filepath.Separator)}, nil
	case "slash_tmp":
		return []string{"/tmp"}, nil
	case "tmpdir":
		tmpdir := os.Getenv("TMPDIR")
		if tmpdir == "" {
			return nil, fmt.Errorf("special path %q needs TMPDIR, which is unset", special.Kind)
		}
		return []string{filepath.Clean(tmpdir)}, nil
	case "project_roots":
		roots, err := codexRuntimeWorkspaceRoots(p.run)
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			roots = []string{cwd}
		}
		resolved := make([]string, len(roots))
		for i, root := range roots {
			resolved[i] = resolveAgainst(root, stringValue(special.Subpath))
		}
		return resolved, nil
	default:
		return nil, fmt.Errorf("entry names special path %q, which captain cannot resolve to a concrete root", special.Kind)
	}
}

// permissionsAnswer grants the decision's subset, or everything requested when
// an approval names none. The run's own deny rules are a floor no grant crosses.
func (p codexPosture) permissionsAnswer(decision api.ApprovalDecision, requested api.NativeSandboxPolicy) (any, error) {
	if !decision.Allow {
		return codexNoGrant(), nil
	}
	grant := requested
	if decision.Grants != nil {
		grant = *decision.Grants
	}
	if sandbox := p.run.Sandbox; sandbox != nil && sandbox.Policy != nil {
		if err := sandbox.Policy.GrantConflict(grant); err != nil {
			return nil, err
		}
	}
	scope := "turn"
	if decision.Scope == api.ApprovalScopeSession {
		scope = "session"
	}
	profile := map[string]any{}
	if fs := grant.Filesystem; fs != nil && len(fs.WritableRoots)+len(fs.ReadableRoots) > 0 {
		entries := make([]map[string]any, 0, len(fs.WritableRoots)+len(fs.ReadableRoots))
		for _, granted := range []struct {
			access string
			roots  []string
		}{{"write", fs.WritableRoots}, {"read", fs.ReadableRoots}} {
			for _, root := range granted.roots {
				entries = append(entries, map[string]any{"path": map[string]any{"type": "path", "path": root}, "access": granted.access})
			}
		}
		profile["fileSystem"] = map[string]any{"entries": entries}
	}
	if grant.Network != nil && grant.Network.Access == api.SandboxNetworkUnrestricted {
		profile["network"] = map[string]any{"enabled": true}
	}
	return map[string]any{"permissions": profile, "scope": scope}, nil
}

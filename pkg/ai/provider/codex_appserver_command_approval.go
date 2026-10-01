package provider

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
)

// codexCommandApprovalParams is item/commandExecution/requestApproval. The
// execpolicy and network amendment proposals are left in Input for display:
// they are persistent rules, which Captain never offers as a decision.
type codexCommandApprovalParams struct {
	ItemID string `json:"itemId"`
	// ApprovalID is null for a regular shell approval, the only approval on its
	// item; it is set wherever one item raises several, and for stdin writes.
	ApprovalID             *string `json:"approvalId"`
	Kind                   *string `json:"kind"`
	Command                *string `json:"command"`
	Cwd                    *string `json:"cwd"`
	Reason                 *string `json:"reason"`
	NetworkApprovalContext *struct {
		Host     string `json:"host"`
		Protocol string `json:"protocol"`
	} `json:"networkApprovalContext"`
	ProposedExecpolicyAmendment []string          `json:"proposedExecpolicyAmendment"`
	AvailableDecisions          []json.RawMessage `json:"availableDecisions"`
}

func buildCodexCommandApproval(req jsonrpc.ServerRequest, _ *turnState) (codexApproval, error) {
	var params codexCommandApprovalParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return codexApproval{}, fmt.Errorf("codex command approval: %w", err)
	}
	input, err := codexInput(req.Params)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex command approval: %w", err)
	}
	if params.ItemID == "" {
		return codexApproval{}, fmt.Errorf("codex command approval needs an itemId")
	}
	kind := "command"
	if params.Kind != nil {
		kind = *params.Kind
	}
	if kind != "command" && kind != "writeStdin" {
		return codexApproval{}, fmt.Errorf("codex command approval %s has unknown kind %q", params.ItemID, kind)
	}
	toolUseID := firstNonEmpty(stringValue(params.ApprovalID), params.ItemID)
	if kind == "writeStdin" && stringValue(params.ApprovalID) == "" {
		return codexApproval{}, fmt.Errorf("codex writeStdin approval on %s needs an approvalId", params.ItemID)
	}
	available, err := codexAvailableDecisions(params.AvailableDecisions)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex command approval %s: %w", toolUseID, err)
	}
	request := api.ApprovalRequest{
		Tool: "exec_command", Input: input, ToolUseID: toolUseID, Kind: api.ApprovalKindCommand,
		Reason: stringValue(params.Reason), Escalates: true, Interruptible: true,
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession},
	}
	if available != nil {
		request.Interruptible, request.SupportedScopes = available["cancel"], nil
		if available["accept"] {
			request.SupportedScopes = append(request.SupportedScopes, api.ApprovalScopeRequest)
		}
		if available["acceptForSession"] {
			request.SupportedScopes = append(request.SupportedScopes, api.ApprovalScopeSession)
		}
	}
	if err := setCodexCommandPayload(&request, params, kind); err != nil {
		return codexApproval{}, err
	}
	return codexApproval{request: request, answer: func(decision api.ApprovalDecision) (any, error) {
		return codexCommandDecision(decision, available)
	}}, nil
}

// setCodexCommandPayload fills the one payload a command approval carries: a
// terminal write, network access for a command, or the command itself.
func setCodexCommandPayload(request *api.ApprovalRequest, params codexCommandApprovalParams, kind string) error {
	command := stringValue(params.Command)
	switch {
	case kind == "writeStdin" && params.NetworkApprovalContext != nil:
		return fmt.Errorf("codex writeStdin approval %s unexpectedly carries a network context", request.ToolUseID)
	case kind == "writeStdin":
		request.Tool = "write_stdin"
		request.Command = &api.CommandApproval{Command: command, Stdin: &api.StdinWrite{Terminal: params.ItemID}}
	case params.NetworkApprovalContext != nil:
		request.Kind = api.ApprovalKindNetwork
		request.Network = &api.NetworkApproval{
			Host: params.NetworkApprovalContext.Host, Protocol: params.NetworkApprovalContext.Protocol, Command: command,
		}
	default:
		request.Command = &api.CommandApproval{Command: command, Cwd: stringValue(params.Cwd), ProposedPolicy: params.ProposedExecpolicyAmendment}
	}
	request.Info = &api.ToolInfo{Name: request.Tool}
	return nil
}

// codexAvailableDecisions is the set of plain decisions Codex offered, or nil
// when it did not say. Amendment offers are objects and are never taken.
func codexAvailableDecisions(offered []json.RawMessage) (map[string]bool, error) {
	if offered == nil {
		return nil, nil
	}
	available := make(map[string]bool, len(offered))
	for _, raw := range offered {
		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, "{") {
			continue
		}
		var decision string
		if err := json.Unmarshal(raw, &decision); err != nil {
			return nil, fmt.Errorf("availableDecisions entry %s is neither a decision nor an amendment offer: %w", trimmed, err)
		}
		available[decision] = true
	}
	return available, nil
}

// codexCommandDecision translates a decision into the command and file-change
// vocabulary, refusing one Codex did not offer rather than sending it anyway.
func codexCommandDecision(decision api.ApprovalDecision, available map[string]bool) (map[string]string, error) {
	if decision.UpdatedInput != nil {
		return nil, fmt.Errorf("codex cannot run an edited request: the decision carries updatedInput, which this approval does not take")
	}
	native := "accept"
	switch {
	case decision.Interrupt:
		native = "cancel"
	case !decision.Allow:
		native = "decline"
	case decision.Scope == api.ApprovalScopeSession:
		native = "acceptForSession"
	}
	if available != nil && !available[native] {
		return nil, fmt.Errorf("decision %q is not among the availableDecisions Codex offered", native)
	}
	return map[string]string{"decision": native}, nil
}

// codexFileUpdateChange is one file of a fileChange item.
type codexFileUpdateChange struct {
	Path string `json:"path"`
	Kind struct {
		Type     string  `json:"type"`
		MovePath *string `json:"move_path"`
	} `json:"kind"`
}

// recordFileChange keeps a fileChange item's latest changes from item/started
// and item/fileChange/patchUpdated, for the approval that names the item. It
// reports whether it consumed the notification, which patchUpdated is: it maps
// to no event.
func (ts *turnState) recordFileChange(method string, params json.RawMessage) bool {
	switch method {
	case "item/started":
		if it := parseAppServerNotif(params).Item; it != nil && it.Type == "fileChange" {
			ts.rememberFileChange(it.ID, it.Changes)
		}
		return false
	case "item/fileChange/patchUpdated":
		var patch struct {
			ItemID  string          `json:"itemId"`
			Changes json.RawMessage `json:"changes"`
		}
		if err := json.Unmarshal(params, &patch); err != nil {
			ts.send(ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("codex app-server item/fileChange/patchUpdated: %v", err), Model: ts.model})
			return true
		}
		ts.rememberFileChange(patch.ItemID, patch.Changes)
		return true
	}
	return false
}

func (ts *turnState) rememberFileChange(itemID string, changes json.RawMessage) {
	if itemID == "" {
		return
	}
	ts.fileChangesMu.Lock()
	defer ts.fileChangesMu.Unlock()
	if ts.fileChanges == nil {
		ts.fileChanges = map[string]json.RawMessage{}
	}
	ts.fileChanges[itemID] = changes
}

func (ts *turnState) fileChange(itemID string) (json.RawMessage, bool) {
	ts.fileChangesMu.Lock()
	defer ts.fileChangesMu.Unlock()
	changes, ok := ts.fileChanges[itemID]
	return changes, ok
}

// buildFileChangeApproval joins item/fileChange/requestApproval, which names
// only its item, to the changes that item started with.
func (p codexPosture) buildFileChangeApproval(req jsonrpc.ServerRequest, ts *turnState) (codexApproval, error) {
	var params struct {
		ItemID    string  `json:"itemId"`
		Reason    *string `json:"reason"`
		GrantRoot *string `json:"grantRoot"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return codexApproval{}, fmt.Errorf("codex fileChange approval: %w", err)
	}
	raw, ok := ts.fileChange(params.ItemID)
	if !ok {
		return codexApproval{}, fmt.Errorf("codex fileChange approval for unknown fileChange item %q: no item/started carried its changes", params.ItemID)
	}
	filesystem, err := codexPatchApproval(params.ItemID, raw)
	if err != nil {
		return codexApproval{}, err
	}
	written := filesystem.Paths
	if root := stringValue(params.GrantRoot); root != "" {
		written = append(append([]string{}, written...), root)
	}
	escalates, err := p.writesEscalate(written)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex fileChange approval %s: %w", params.ItemID, err)
	}
	input, err := codexInput(req.Params)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex fileChange approval: %w", err)
	}
	var joined any
	if err := json.Unmarshal(raw, &joined); err != nil {
		return codexApproval{}, fmt.Errorf("codex fileChange item %s: %w", params.ItemID, err)
	}
	input["changes"] = joined
	return codexApproval{request: api.ApprovalRequest{
		Tool: "apply_patch", Input: input, ToolUseID: params.ItemID, Kind: api.ApprovalKindFilesystem,
		Reason: stringValue(params.Reason), Escalates: escalates, Interruptible: true,
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession},
		Info:            &api.ToolInfo{Name: "apply_patch"}, Filesystem: filesystem,
	}, answer: func(decision api.ApprovalDecision) (any, error) {
		return codexCommandDecision(decision, nil)
	}}, nil
}

// codexPatchApproval lists the files a fileChange item's changes touch, a
// moved file under both its old and new path.
func codexPatchApproval(itemID string, raw json.RawMessage) (*api.FilesystemApproval, error) {
	var changes []codexFileUpdateChange
	if err := json.Unmarshal(raw, &changes); err != nil {
		return nil, fmt.Errorf("codex fileChange item %s: %w", itemID, err)
	}
	filesystem := &api.FilesystemApproval{Operation: api.FilesystemPatch}
	for _, change := range changes {
		if change.Path == "" {
			return nil, fmt.Errorf("codex fileChange item %s has a change without a path", itemID)
		}
		filesystem.Paths = append(filesystem.Paths, change.Path)
		if move := stringValue(change.Kind.MovePath); move != "" {
			filesystem.Paths = append(filesystem.Paths, move)
		}
		filesystem.Changes = append(filesystem.Changes, api.FileChange{Path: change.Path, Kind: change.Kind.Type})
	}
	return filesystem, nil
}

// writesEscalate reports whether writing any of paths exceeds what the run's
// sandbox already lets Codex write: its workspace roots and writable roots.
func (p codexPosture) writesEscalate(paths []string) (bool, error) {
	translation, err := translateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), p.run)
	if err != nil {
		return false, err
	}
	switch translation.Sandbox {
	case api.CodexSandboxDangerFull:
		return false, nil
	case api.CodexSandboxReadOnly:
		return len(paths) > 0, nil
	}
	cwd, err := codexRunCwd(p.run)
	if err != nil {
		return false, err
	}
	roots, err := codexRuntimeWorkspaceRoots(p.run)
	if err != nil {
		return false, err
	}
	roots = append(roots, cwd)
	if sandbox := p.run.Sandbox; sandbox != nil && sandbox.Policy != nil && sandbox.Policy.Filesystem != nil {
		roots = append(roots, sandbox.Policy.Filesystem.WritableRoots...)
	}
	for _, path := range paths {
		if !pathUnderAny(roots, resolveAgainst(cwd, path)) {
			return true, nil
		}
	}
	return false, nil
}

func resolveAgainst(cwd, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func pathUnderAny(roots []string, path string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

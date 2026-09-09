package api

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api/registry"
)

// Permissions governs what an agent may do: the base posture, named presets,
// per-tool policy, MCP servers, and plugin directories.
//
// The permission mode is orthogonal to the sandbox. A sandbox constrains what
// the process can reach — filesystem, network — while the mode decides whether
// the agent asks before acting. The two vary independently: a run can ask for
// `plan` with no sandbox at all, and a fully sandboxed run can still want
// `acceptEdits`. Folding the mode into the sandbox made `sandbox: off` imply
// bypassPermissions, which silently escalated a run that had asked for plan.
type Permissions struct {
	// Mode is the base permission posture, independent of any sandbox.
	Mode PermissionMode `json:"mode,omitempty" yaml:"mode,omitempty" pretty:"label=Mode"`
	// Presets are named safety bundles applied before per-tool rules. (--edit/--bare)
	Presets []Preset `json:"presets,omitempty" yaml:"presets,omitempty" pretty:"label=Presets"`
	// Tools is the per-tool allow/deny/mode policy.
	Tools Tools `json:"tools,omitempty" yaml:"tools,omitempty"`
	// MCP controls Model-Context-Protocol servers.
	MCP MCP `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	// Plugins are extra plugin directories (claude --plugin-dir).
	Plugins ResourcePolicies `json:"plugins,omitempty" yaml:"plugins,omitempty" pretty:"label=Plugins"`
	// Skills are skill directories enabled for this request.
	Skills ResourcePolicies `json:"skills,omitempty" yaml:"skills,omitempty" pretty:"label=Skills"`
	// Directories are paths outside the working directory the run's own tools may
	// read and write (claude/codex --add-dir, the Claude Agent SDK's
	// additionalDirectories).
	//
	// The working directory alone is not the run's world. A run in a git worktree
	// reaches its parent checkout for anything .gitignore hides, a linked module
	// or `file:` dependency resolves outside the tree, and a monorepo task spans
	// siblings. Without this the runtime raises a permission prompt for each such
	// path, which an unattended run has nobody to answer — so the grant belongs
	// with the rest of the posture, resolved through the same layering, rather
	// than in per-host CLI arguments only one runtime reads.
	Directories []string `json:"directories,omitempty" yaml:"directories,omitempty" pretty:"label=Directories"`
	// ApprovalTimeout is how long a tool the mode makes the agent ask about may
	// stay unanswered before the run gives up on it, as a Go duration ("30m").
	// Empty means the brokering host's own default.
	//
	// It sits beside Mode because it answers the same question — how much
	// authority a run holds without a person in the loop — and so that it
	// resolves through one layering (.gavel.yaml → prompt frontmatter → request)
	// rather than a compile-time constant that cannot tell an unattended CI run
	// from a dashboard somebody is watching.
	ApprovalTimeout string `json:"approvalTimeout,omitempty" yaml:"approvalTimeout,omitempty" pretty:"label=Approval Timeout"`
}

// CleanDirectories is Directories as a transport should emit it: trimmed, with
// blanks dropped and duplicates collapsed, in the order first named.
//
// Every runtime needs the same shape and none of them should each invent it —
// a blank entry becomes a flag with an empty value, and the same path arriving
// from two layers (a profile and the worktree that contributed it) would
// otherwise be granted twice.
func (p Permissions) CleanDirectories() []string {
	var dirs []string
	seen := make(map[string]struct{}, len(p.Directories))
	for _, dir := range p.Directories {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
}

// ParseApprovalTimeout resolves ApprovalTimeout to a duration. Zero means "no
// window declared" — the host's own default applies. An unparseable or
// non-positive value is an error rather than a silent fallback: a declared
// window that quietly does nothing is worse than none, because it reads as
// enforced.
func (p Permissions) ParseApprovalTimeout() (time.Duration, error) {
	if p.ApprovalTimeout == "" {
		return 0, nil
	}
	window, err := time.ParseDuration(p.ApprovalTimeout)
	if err != nil {
		return 0, fmt.Errorf("invalid permissions approvalTimeout %q: %w", p.ApprovalTimeout, err)
	}
	if window <= 0 {
		return 0, fmt.Errorf("invalid permissions approvalTimeout %q (must be > 0)", p.ApprovalTimeout)
	}
	return window, nil
}

// Tools is the per-tool policy map, and its own wire shape:
// map[tool]auto|ask|allow|deny.
//
// The legacy {allow: [], deny: [], modes: {}} object is still accepted on
// decode and folded into this map — see UnmarshalJSON. It is never emitted.
type Tools map[string]ToolPolicy

// MCP controls Model-Context-Protocol servers.
type MCP struct {
	// Disabled turns off all MCP servers. (ai.Request.NoMCP)
	Disabled bool `json:"-" yaml:"-" pretty:"label=Disabled"`
	// Servers is an optional allowlist subset of configured servers.
	Servers []string `json:"-" yaml:"-" pretty:"label=Servers"`
	// Modes optionally enables/disables named configured servers.
	Modes ResourcePolicies `json:"-" yaml:"-" pretty:"label=Modes"`
}

// ResourcePolicies maps MCP/plugin/skill IDs to enabled|disabled. It accepts a
// legacy string array on decode, treating every listed item as enabled.
type ResourcePolicies map[string]ResourceMode

// HasPreset reports whether the named preset is enabled.
func (p Permissions) HasPreset(x Preset) bool {
	return slices.Contains(p.Presets, x)
}

// AllowList and DenyList project the policy map onto the two lists every claude
// transport speaks (--allowedTools / --disallowedTools). They are the only
// correct source for those flags: a legacy `tools: {Bash: off}` decodes to deny,
// so anything that filters on its own notion of "denied" lets it past and the
// tool runs.
func (t Tools) AllowList() []string { return t.toolsWithPolicy(ToolPolicyAllow) }

// DenyList is AllowList's counterpart; see its documentation.
func (t Tools) DenyList() []string { return t.toolsWithPolicy(ToolPolicyDeny) }

func (t Tools) toolsWithPolicy(want ToolPolicy) []string {
	var out []string
	for tool, policy := range t {
		if policy == want {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// RequireToolPolicySupport refuses a run whose per-tool policy the backend
// cannot carry.
//
// A deny-list exists solely to forbid a tool, so dropping it silently inverts
// the caller's intent: the agent runs with strictly more authority than the spec
// granted, and nothing in the output says so. Only the claude transports reach a
// --disallowedTools equivalent today, so the rest fail loud here rather than
// proceeding as if the policy had been applied.
//
// Allow-lists are checked too: on a runtime with no tool filter, an allowlist is
// equally unenforced. The one entry that may be dropped is an allow naming
// another agent's built-in (a Claude `Read: allow` on a codex run): an allow only
// pre-approves a tool the agent already has, so on an agent without that tool it
// constrains nothing, and a portable spec can carry both vocabularies. A deny or
// ask is never dropped on that basis — `Bash` is codex's `shell` under another
// name, and dropping the deny would hand the agent the very tool the spec
// forbade. `ask` is refused everywhere: no transport has a per-tool prompt, so it
// would resolve to "allowed" on the runtimes that advertise tool policy support.
// `auto` constrains nothing, so it needs no runtime support.
func RequireToolPolicySupport(p *ModelProvider, mode RuntimeMode, permissions Permissions) error {
	if asked := permissions.Tools.toolsWithPolicy(ToolPolicyAsk); len(asked) > 0 {
		return fmt.Errorf(
			"per-tool policy \"ask\" (%s) is not enforceable on any runtime: transports carry allow/deny tool lists only, so the tool would run unprompted; use allow or deny",
			strings.Join(asked, ", "))
	}
	vocabulary := PermissionCapabilitiesFor(RuntimeOf(p, mode)).Tools
	allowed := slices.DeleteFunc(permissions.Tools.AllowList(), func(tool string) bool {
		return isForeignBuiltin(vocabulary, tool)
	})
	enforced := append(allowed, permissions.Tools.DenyList()...)
	if len(enforced) == 0 || registry.SupportsToolPolicy(p, mode) {
		return nil
	}
	sort.Strings(enforced)
	return fmt.Errorf(
		"%s cannot enforce a per-tool policy (%s), and running without it would grant more than the spec allows; remove permissions.tools or use one of: %s",
		registry.RuntimeOf(p, mode), strings.Join(enforced, ", "), registry.RuntimesList(registry.ToolPolicyRuntimes()))
}

// isForeignBuiltin reports whether tool is positively identified as another
// agent's built-in and absent from the selected runtime's vocabulary. It is the
// only basis on which an allow entry may be skipped; see RequireToolPolicySupport.
//
// A name no agent declares stays enforceable: the vocabularies are hand-kept,
// and a stale table must fail loud on a newly added built-in rather than wave it
// through. A runtime with no vocabulary at all (the API modes) owns every name.
func isForeignBuiltin(vocabulary []string, tool string) bool {
	if len(vocabulary) == 0 || slices.Contains(vocabulary, tool) {
		return false
	}
	for _, tools := range agentTools {
		if slices.ContainsFunc(tools, func(candidate AgentTool) bool {
			return candidate.Name == tool
		}) {
			return true
		}
	}
	return false
}

// Validate checks the mode, presets, tool policies, and resource modes are
// recognised.
func (p Permissions) Validate() error {
	if !p.Mode.Valid() {
		return fmt.Errorf("invalid permission mode %q", p.Mode)
	}
	for _, preset := range p.Presets {
		if !preset.Valid() {
			return fmt.Errorf("invalid preset %q (valid: edit, bare)", preset)
		}
	}
	for _, tool := range sortedKeys(p.Tools) {
		if policy := p.Tools[tool]; !policy.Valid() {
			return fmt.Errorf("invalid tool policy %q for tool %q (valid: auto, ask, allow, deny)", policy, tool)
		}
	}
	for name, mode := range p.MCP.Modes {
		if !mode.Valid() {
			return fmt.Errorf("invalid mcp mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	for name, mode := range p.Plugins {
		if !mode.Valid() {
			return fmt.Errorf("invalid plugin mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	for name, mode := range p.Skills {
		if !mode.Valid() {
			return fmt.Errorf("invalid skill mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	if _, err := p.ParseApprovalTimeout(); err != nil {
		return err
	}
	return nil
}

// Policies returns the tool policy map. Tools is that map, so this is an
// identity projection kept for callers that read the policy view by name.
func (t Tools) Policies() map[string]ToolPolicy { return t }

// ToolsFromLists builds a policy map from the two flag-shaped lists the CLI
// carries (--allowed-tools / --disallowed-tools). A tool named in both is
// denied: the deny list exists solely to forbid, so honouring allow instead
// would grant more than the caller asked for.
func ToolsFromLists(allow, deny []string) Tools {
	var tools Tools
	for _, tool := range compactStrings(allow) {
		tools.put(tool, ToolPolicyAllow)
	}
	for _, tool := range compactStrings(deny) {
		tools.put(tool, ToolPolicyDeny)
	}
	return tools
}

// SetList replaces every tool currently carrying policy with the named tools.
// It is how a CLI flag overrides an inherited allow/deny list wholesale rather
// than merging into it.
func (t *Tools) SetList(policy ToolPolicy, tools []string) {
	for tool, current := range *t {
		if current == policy {
			delete(*t, tool)
		}
	}
	for _, tool := range compactStrings(tools) {
		t.put(tool, policy)
	}
}

package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
)

// A permissions.tools key is authored once and read by every runtime a spec can
// land on, but each agent names its tools differently: claude's Bash is codex's
// shell and gemini's run_shell_command. ForRuntime is the one place a key is
// turned into the names a particular runtime speaks. The resolved spec, its
// trace and its preview keep the authored keys; only a runtime's own transport
// and its capability checks see the translation.
//
// A key resolves, in order, through:
//   - a `<provider>:<rule>` prefix (claude:Bash, gemini:read_file), which scopes
//     the key to one provider and is dropped everywhere else; the prefix only
//     scopes, so the rule after it resolves exactly like an unprefixed key;
//   - an mcp__ name, which MCP serves rather than the built-in table;
//   - the runtime's own tool name, matched case-sensitively and kept as written;
//   - a shared alias (shell, edit, …), matched case-insensitively, which expands
//     to every tool carrying it;
//   - another agent's built-in, matched case-sensitively, which expands through
//     that tool's aliases;
//   - the runtime's own tool name in another case, emitted in its canonical
//     spelling;
//   - another agent's built-in in another case.
//
// Anything else is ignored, with a warning when the ignored policy is a deny.
// Alias and foreign expansions emit bare tool names: a pattern such as
// `Bash(git log:*)` has no meaning on another agent, so a patterned deny fails
// closed to the whole tool and a patterned allow grants nothing.
//
// On a runtime without a per-tool filter, an allow is dropped when it reached a
// tool only through an alias or a foreign built-in, and also whenever the key
// (after any prefix) is spelled as an alias, even where the alias is also the
// runtime's own tool name (codex shell, gemini web_fetch). Denies are never
// dropped that way.

// ForRuntime translates the authored keys into the runtime's tool names and
// returns a warning for every deny it had to ignore. Collisions only tighten:
// see collapseToolContributions.
func (t Tools) ForRuntime(p *ModelProvider, mode RuntimeMode) (Tools, []string) {
	if len(t) == 0 {
		return nil, nil
	}
	resolved, warnings := t.resolveForRuntime(p, mode)
	out := make(Tools, len(resolved))
	for rule, tool := range resolved {
		out[rule] = tool.policy
	}
	return out, warnings
}

// ForRuntime returns a copy of the permissions with Tools translated for the
// runtime, and a warning for every deny it had to ignore. A transport logs
// them, so a provider driven directly, without UnsupportedPermissions, still
// reports a dropped deny.
func (p Permissions) ForRuntime(provider *ModelProvider, mode RuntimeMode) (Permissions, []string) {
	var warnings []string
	p.Tools, warnings = p.Tools.ForRuntime(provider, mode)
	return p, warnings
}

// toolContribution is one authored key's opinion about one emitted rule.
type toolContribution struct {
	rule   string
	key    string
	policy ToolPolicy
	// derived marks a rule reached through an alias or another agent's built-in
	// rather than named by the key itself.
	derived bool
}

// resolvedTool is the collapsed policy for one emitted rule, with the authored
// keys that decided it so an error can name both.
type resolvedTool struct {
	policy  ToolPolicy
	sources []string
}

// describe renders a rule for error text: `shell (from Bash)` when the rule
// came from differently named keys, the bare rule otherwise.
func (r resolvedTool) describe(rule string) string {
	from := slices.DeleteFunc(slices.Clone(r.sources), func(key string) bool { return key == rule })
	if len(from) == 0 {
		return rule
	}
	return fmt.Sprintf("%s (from %s)", rule, strings.Join(from, ", "))
}

func (t Tools) resolveForRuntime(p *ModelProvider, mode RuntimeMode) (map[string]resolvedTool, []string) {
	vocabulary := AgentToolsFor(p, mode)
	filtered := registry.SupportsToolPolicy(p, mode)
	var contributions []toolContribution
	var warnings []string
	for _, key := range sortedKeys(t) {
		policy := t[key]
		rule, inScope := providerScopedRule(p, key)
		if !inScope {
			continue
		}
		expanded := expandToolRule(vocabulary, rule)
		found := toolContributions(expanded, key, policy)
		if len(found) == 0 && policy == ToolPolicyDeny {
			warnings = append(warnings, fmt.Sprintf(
				"permissions.tools %q deny is ignored: %s has no tool it names", key, RuntimeOf(p, mode)))
		}
		base, _, _ := strings.Cut(rule, "(")
		portable := isToolAlias(base)
		for _, c := range found {
			// Portable allowlists (#110): on a runtime with no tool filter an
			// allow that only an alias or a foreign name reached pre-approves a
			// tool the spec never named, so it constrains nothing and is dropped.
			// A key spelled as an alias is portable in every spelling, even where
			// the alias is also one of this runtime's tool names (codex shell),
			// so the outcome never turns on case.
			if !filtered && (c.derived || portable) && c.policy == ToolPolicyAllow {
				continue
			}
			contributions = append(contributions, c)
		}
	}
	return collapseToolContributions(contributions), warnings
}

// providerScopedRule strips a `<provider>:` prefix. inScope is false when the
// prefix names a different provider. Text before the colon that is not a
// provider (`Bash(npm run test:*)`) is part of the rule, not a prefix.
func providerScopedRule(p *ModelProvider, key string) (rule string, inScope bool) {
	prefix, rest, found := strings.Cut(key, ":")
	if !found {
		return key, true
	}
	named, ok := registry.ProviderByName(prefix)
	if !ok {
		return key, true
	}
	return rest, named == p
}

// toolExpansion is what one rule names on a runtime, before policy is applied.
type toolExpansion struct {
	// exact is the rule as the runtime spells it, empty when the rule does not
	// name one of the runtime's own tools.
	exact string
	// derived are the bare tools reached through an alias or a foreign built-in.
	derived   []string
	patterned bool
}

func expandToolRule(vocabulary []AgentTool, rule string) toolExpansion {
	base, _, patterned := strings.Cut(rule, "(")
	out := toolExpansion{patterned: patterned}
	if strings.HasPrefix(strings.ToLower(rule), "mcp__") {
		out.exact = rule
		return out
	}
	if len(vocabulary) == 0 {
		// The API mode ships no built-ins, so a name any agent owns constrains
		// nothing here; every other name is one of the caller's own tools.
		if !isAgentVocabulary(base) {
			out.exact = rule
		}
		return out
	}
	if tool, ok := vocabularyTool(vocabulary, base, false); ok {
		out.exact = rule
		if slices.Contains(tool.Aliases, base) {
			out.derived = slices.DeleteFunc(aliasCarriers(vocabulary, []string{base}), func(name string) bool { return name == tool.Name })
		}
		return out
	}
	if carriers := aliasCarriers(vocabulary, []string{base}); len(carriers) > 0 {
		out.derived = carriers
		return out
	}
	if aliases, ok := foreignAliases(base, false); ok {
		out.derived = aliasCarriers(vocabulary, aliases)
		return out
	}
	if tool, ok := vocabularyTool(vocabulary, base, true); ok {
		out.exact = tool.Name + rule[len(base):]
		return out
	}
	if aliases, ok := foreignAliases(base, true); ok {
		out.derived = aliasCarriers(vocabulary, aliases)
	}
	return out
}

// toolContributions applies one key's policy to its expansion. A patterned rule
// reached through an alias or a foreign name loses its pattern, so it only
// survives as a restriction on the bare tool.
func toolContributions(expanded toolExpansion, key string, policy ToolPolicy) []toolContribution {
	var out []toolContribution
	if expanded.exact != "" {
		out = append(out, toolContribution{rule: expanded.exact, key: key, policy: policy})
	}
	if expanded.patterned && policy != ToolPolicyDeny && policy != ToolPolicyAsk {
		return out
	}
	for _, name := range expanded.derived {
		out = append(out, toolContribution{rule: name, key: key, policy: policy, derived: true})
	}
	return out
}

// collapseToolContributions groups contributions by emitted rule. The strictest
// policy wins — deny, then ask, then allow — and auto is no opinion. Collisions
// can therefore only tighten: the spec fold forgets which layer wrote a key, so
// a rank-based rule would let a lower layer's `Bash: allow` erase a preset's
// `shell: deny`. Same-key overrides already happened, in layer order, in
// Spec.Merge.
func collapseToolContributions(contributions []toolContribution) map[string]resolvedTool {
	out := make(map[string]resolvedTool, len(contributions))
	for _, c := range contributions {
		current, seen := out[c.rule]
		switch {
		case !seen || toolPolicyStrictness(c.policy) > toolPolicyStrictness(current.policy):
			out[c.rule] = resolvedTool{policy: c.policy, sources: []string{c.key}}
		case c.policy == current.policy && !slices.Contains(current.sources, c.key):
			current.sources = append(current.sources, c.key)
			out[c.rule] = current
		}
	}
	return out
}

func toolPolicyStrictness(policy ToolPolicy) int {
	switch policy {
	case ToolPolicyDeny:
		return 3
	case ToolPolicyAsk:
		return 2
	case ToolPolicyAllow:
		return 1
	default:
		return 0
	}
}

func vocabularyTool(vocabulary []AgentTool, name string, foldCase bool) (AgentTool, bool) {
	for _, tool := range vocabulary {
		if tool.Name == name || (foldCase && strings.EqualFold(tool.Name, name)) {
			return tool, true
		}
	}
	return AgentTool{}, false
}

// aliasCarriers returns the bare names of every vocabulary tool carrying one of
// the aliases, matched case-insensitively.
func aliasCarriers(vocabulary []AgentTool, aliases []string) []string {
	var out []string
	for _, tool := range vocabulary {
		if slices.ContainsFunc(tool.Aliases, func(alias string) bool {
			return slices.ContainsFunc(aliases, func(want string) bool { return strings.EqualFold(alias, want) })
		}) {
			out = append(out, tool.Name)
		}
	}
	return out
}

// foreignAliases reports whether name is some agent's built-in and returns the
// aliases it carries. It is only consulted once the runtime's own vocabulary
// has not claimed the name.
func foreignAliases(name string, foldCase bool) ([]string, bool) {
	var aliases []string
	found := false
	for _, tools := range agentTools {
		if tool, ok := vocabularyTool(tools, name, foldCase); ok {
			found = true
			aliases = append(aliases, tool.Aliases...)
		}
	}
	return aliases, found
}

// isAgentVocabulary reports whether any agent declares name as a built-in or an
// alias, case-insensitively.
func isAgentVocabulary(name string) bool {
	_, builtin := foreignAliases(name, true)
	return builtin || isToolAlias(name)
}

// isToolAlias reports whether any agent's tool carries name as a shared alias,
// case-insensitively.
func isToolAlias(name string) bool {
	for _, tools := range agentTools {
		if len(aliasCarriers(tools, []string{name})) > 0 {
			return true
		}
	}
	return false
}

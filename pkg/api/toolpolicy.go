package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons/collections"
	"gopkg.in/yaml.v3"
)

// This file is the one place a tool's authority is decided.
//
// It replaces four mechanisms that each owned part of the answer and disagreed:
// a static group→permission table, clicky's verb-default annotation stamping, an
// app-specific chat permission callback, and MCP metadata overlay. All four wrote
// into the same DefaultPermission slot, so the last writer won by accident of
// call order rather than by intent — which is how a group baseline like
// `provider.xero.read: off` could never reach the execution path, and how the
// OpenAPI catalog and the chat executor came to disagree about the same tool.
//
// The replacement is an ordered rule list evaluated last-match-wins. Order is the
// whole contract: weakest first, strongest last — group baselines, verb defaults,
// hint/method defaults, app rules, surface (.prompt) rules, then user rules. A
// later rule overrides an earlier one, so "stronger" is expressed by position
// rather than by a precedence number that every producer would have to keep
// consistent with every other.
//
// Matching is glob-based rather than exact-string. Exact matching is what forced
// callers to enumerate every tool name, and an enumeration goes stale silently
// the moment a tool is added — the new tool simply matches nothing and falls back
// to a default nobody chose.

// MatchPatterns is a glob pattern list that decodes from either a scalar or a
// sequence, so `.prompt` frontmatter can write `group: provider.xero.*` without
// list ceremony.
//
// Patterns are matched with commons/collections.MatchItems: `!` negates and takes
// precedence over any positive match, `*` wildcards a prefix, suffix, or the
// whole item, matching is case-insensitive, and one string may carry
// comma-separated alternatives. An empty list matches everything, which is why
// ToolMatch.Empty is a validation error rather than a wildcard rule.
type MatchPatterns []string

// Matches reports whether item satisfies these patterns. An undeclared list
// imposes no constraint.
func (p MatchPatterns) Matches(item string) bool {
	if len(p) == 0 {
		return true
	}
	return collections.MatchItems(item, p...)
}

func (p *MatchPatterns) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*p = compactPatterns([]string{single})
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("match patterns must be a string or a list of strings: %w", err)
	}
	*p = compactPatterns(list)
	return nil
}

func (p *MatchPatterns) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		*p = compactPatterns([]string{single})
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return fmt.Errorf("match patterns must be a string or a list of strings: %w", err)
	}
	*p = compactPatterns(list)
	return nil
}

func compactPatterns(in []string) MatchPatterns {
	out := make(MatchPatterns, 0, len(in))
	for _, pattern := range in {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			out = append(out, pattern)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolMatch selects the tools a rule applies to.
//
// Every declared facet must match (AND across facets); within one facet the
// patterns are alternatives (OR). An undeclared facet does not constrain, so a
// rule says only as much as it means to.
//
// Entity, Action, Verb, Method and Scope read the clicky operation a tool
// projects. A tool that projects none (an MCP server tool, an app-owned caller
// tool) reports them empty, so a rule declaring any of them simply does not
// select it.
type ToolMatch struct {
	Name   MatchPatterns `json:"name,omitempty" yaml:"name,omitempty"`
	Group  MatchPatterns `json:"group,omitempty" yaml:"group,omitempty"`
	Parent MatchPatterns `json:"parent,omitempty" yaml:"parent,omitempty"`
	Entity MatchPatterns `json:"entity,omitempty" yaml:"entity,omitempty"`
	Action MatchPatterns `json:"action,omitempty" yaml:"action,omitempty"`
	Verb   MatchPatterns `json:"verb,omitempty" yaml:"verb,omitempty"`
	Method MatchPatterns `json:"method,omitempty" yaml:"method,omitempty"`
	Scope  MatchPatterns `json:"scope,omitempty" yaml:"scope,omitempty"`

	// ReadOnly, Destructive and Idempotent match the tool's safety hints. A
	// declared hint requires the tool to declare the same hint with the same
	// value: an undeclared hint on the tool does NOT match, because "this tool
	// never said whether it is read-only" and "this tool said it is not
	// read-only" are different claims, and treating the first as the second is
	// how an unannotated tool would quietly inherit a permissive rule.
	ReadOnly    *bool `json:"readOnly,omitempty" yaml:"readOnly,omitempty"`
	Destructive *bool `json:"destructive,omitempty" yaml:"destructive,omitempty"`
	Idempotent  *bool `json:"idempotent,omitempty" yaml:"idempotent,omitempty"`
}

// Empty reports whether the match declares no facet at all. Such a rule would
// match every tool and, being last-match-wins, silently become the final word on
// every one of them.
func (m ToolMatch) Empty() bool {
	return len(m.Name) == 0 && len(m.Group) == 0 && len(m.Parent) == 0 &&
		len(m.Entity) == 0 && len(m.Action) == 0 &&
		len(m.Verb) == 0 && len(m.Method) == 0 && len(m.Scope) == 0 &&
		m.ReadOnly == nil && m.Destructive == nil && m.Idempotent == nil
}

// Matches reports whether this match selects the given tool.
func (m ToolMatch) Matches(info ToolInfo) bool {
	return m.Name.Matches(info.Name) &&
		m.Group.Matches(info.Group) &&
		m.Parent.Matches(info.Parent) &&
		m.Entity.Matches(info.Entity()) &&
		m.Action.Matches(info.Action()) &&
		m.Verb.Matches(info.Verb()) &&
		m.Method.Matches(info.Method()) &&
		m.Scope.Matches(info.Scope()) &&
		matchesHint(m.ReadOnly, info.ReadOnlyHint) &&
		matchesHint(m.Destructive, info.DestructiveHint) &&
		matchesHint(m.Idempotent, info.IdempotentHint)
}

func matchesHint(want, have *bool) bool {
	if want == nil {
		return true
	}
	return have != nil && *have == *want
}

// PermissionRule is one ordered rule: the tools it selects and the authority they
// get. The match facets are inlined so a rule reads as one flat mapping in
// `.prompt` frontmatter.
type PermissionRule struct {
	ToolMatch `json:",inline" yaml:",inline"`

	// Policy is the authority granted to matching tools. `auto` is meaningful
	// here: it hands the decision back to the tool's own safety hints rather
	// than asserting an answer.
	Policy ToolPolicy `json:"policy" yaml:"policy"`
}

// Validate rejects a rule that would match everything or grant an unrecognised
// authority. Both are fail-loud rather than fail-quiet: an empty match becomes
// the last word on every tool, and an unrecognised policy would otherwise fall
// back to a default the author did not choose.
func (r PermissionRule) Validate() error {
	if r.Empty() {
		return fmt.Errorf("permission rule must declare at least one match facet (name, group, parent, verb, method, scope, or a hint)")
	}
	if !r.Policy.Valid() {
		return fmt.Errorf("invalid policy %q in permission rule (valid: %s)", r.Policy, toolPolicyList())
	}
	return nil
}

// PermissionPolicy is the ordered rule list, evaluated last-match-wins.
type PermissionPolicy []PermissionRule

// Validate checks every rule, reporting the offending index so an author can find
// it in a long list.
func (p PermissionPolicy) Validate() error {
	for i, rule := range p {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("permission rule %d: %w", i, err)
		}
	}
	return nil
}

// Resolve returns the authority for one tool: the policy of the LAST rule that
// matches it, and whether any rule matched at all.
//
// Last, not first, because the layer order this list encodes runs weakest to
// strongest — a user rule appended after a group baseline is meant to win. A
// first-match-wins reading would invert the entire contract.
func (p PermissionPolicy) Resolve(info ToolInfo) (ToolPolicy, bool) {
	policy, matched := ToolPolicyAuto, false
	for _, rule := range p {
		if rule.Matches(info) {
			policy, matched = rule.Policy, true
		}
	}
	return policy, matched
}

// Append returns the concatenation of two policies, the later winning. It exists
// so call sites express layering as composition rather than by mutating a shared
// slice, which would let one caller's user rules leak into another's.
func (p PermissionPolicy) Append(later PermissionPolicy) PermissionPolicy {
	if len(p) == 0 {
		return append(PermissionPolicy(nil), later...)
	}
	if len(later) == 0 {
		return append(PermissionPolicy(nil), p...)
	}
	out := append(PermissionPolicy(nil), p...)
	return append(out, later...)
}

// FromPreferences lowers the flat tool→policy preference map into ordered rules,
// so the legacy shape and the rule list share one evaluation path instead of two
// that can disagree.
//
// A preference key is ambiguous — PreferenceKey yields a tool's group when it has
// one and its name otherwise, so the map alone cannot say which a given key is.
// Rather than guess, each key emits both a group rule and a name rule, with every
// group rule placed before every name rule. A key that names a group matches no
// tool name and vice versa, so the ambiguity costs nothing; and where a key is
// both, the name rule comes later and wins. That is exactly the precedence the
// UI needs, where a per-tool toggle must beat the group toggle above it.
//
// Keys are sorted so the result is deterministic: this list is compared in tests
// and serialized into specs, and map iteration order would make both flap.
func FromPreferences(prefs ToolPreferences) PermissionPolicy {
	if len(prefs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(prefs))
	for key := range prefs {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	out := make(PermissionPolicy, 0, len(keys))
	for _, key := range keys {
		out = append(out, PermissionRule{
			ToolMatch: ToolMatch{Group: MatchPatterns{key}},
			Policy:    prefs[key],
		})
	}
	for _, key := range keys {
		out = append(out, PermissionRule{
			ToolMatch: ToolMatch{Name: MatchPatterns{key}},
			Policy:    prefs[key],
		})
	}
	return out
}

func toolPolicyList() string {
	policies := AllToolPolicies()
	out := make([]string, len(policies))
	for i, policy := range policies {
		out[i] = string(policy)
	}
	return strings.Join(out, ", ")
}

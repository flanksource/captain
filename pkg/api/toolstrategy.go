package api

import "net/http"

// A strategy derives a tool's authority from what the tool already says about
// itself, for the tools no rule mentions. Rules are how an operator states an
// intent; strategies are how the system answers when nobody stated one — and
// before this file that answer was written out longhand in three places (a
// method switch in clicky's tool bridge, a hint check inlined in
// ResolveDefinitions, and a read-only heuristic in serve_chat), each with its own
// idea of what a GET or a destructive tool deserves.

// PermissionStrategy derives a tool's authority from the facts the tool carries.
//
// The signature is PermissionPolicy.Resolve's, so a rule list is itself a
// strategy and the two compose in one ordered chain instead of being two
// mechanisms a caller has to sequence by hand. A strategy that has no opinion
// reports false rather than guessing, leaving the next layer to answer.
type PermissionStrategy interface {
	Resolve(ToolInfo) (ToolPolicy, bool)
}

// HTTPVerbStrategy derives authority from the HTTP method the operation is
// served with: a read is auto-run, a write is asked for.
//
// This is the weakest useful signal — it knows only the shape of the request,
// not what the handler does with it — so it is meant to sit first in the chain
// and be overridden by anything that knows more.
type HTTPVerbStrategy struct{}

func (HTTPVerbStrategy) Resolve(info ToolInfo) (ToolPolicy, bool) {
	switch info.Method() {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return ToolPolicyAllow, true
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return ToolPolicyAsk, true
	default:
		return ToolPolicyAuto, false
	}
}

// MCPHintStrategy derives authority from the tool's declared safety hints.
//
// A hint the tool never declared is not a claim: "this tool did not say whether
// it is read-only" and "this tool said it is not read-only" are different, and
// reading the first as the second is how an unannotated tool would inherit a
// permissive answer. So auto-run requires both halves said out loud — read-only
// AND non-destructive — matching the discipline ToolMatch applies to the same
// hints.
type MCPHintStrategy struct{}

func (MCPHintStrategy) Resolve(info ToolInfo) (ToolPolicy, bool) {
	if info.ReadOnlyHint != nil && *info.ReadOnlyHint &&
		info.DestructiveHint != nil && !*info.DestructiveHint {
		return ToolPolicyAllow, true
	}
	if info.DestructiveHint != nil && *info.DestructiveHint {
		return ToolPolicyAsk, true
	}
	return ToolPolicyAuto, false
}

// DefaultStrategies is the derivation chain applied when a caller names none:
// the HTTP method first, then the safety hints, which override it because a tool
// that states its own semantics knows better than the verb it happens to be
// served under.
func DefaultStrategies() []PermissionStrategy {
	return []PermissionStrategy{HTTPVerbStrategy{}, MCPHintStrategy{}}
}

// ResolveStrategies runs an ordered chain and returns the last answer given, so
// a later strategy overrides an earlier one exactly as a later rule does.
func ResolveStrategies(strategies []PermissionStrategy, info ToolInfo) (ToolPolicy, bool) {
	policy, matched := ToolPolicyAuto, false
	for _, strategy := range strategies {
		if resolved, ok := strategy.Resolve(info); ok {
			policy, matched = resolved, true
		}
	}
	return policy, matched
}

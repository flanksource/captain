package api

import (
	"strings"
	"testing"
)

// TestRequireToolPolicySupport enumerates which backends may carry a per-tool
// policy. A deny-list exists only to forbid a tool, so a backend that drops it
// runs with strictly more authority than the spec granted — the run must fail
// instead. The table is the contract: adding a backend forces a decision here.
func TestRequireToolPolicySupport(t *testing.T) {
	supported := map[Backend]bool{
		BackendClaudeCLI:   true,
		BackendClaudeAgent: true,
		BackendClaudeCmux:  true,
	}

	policy := Permissions{Tools: Tools{Deny: []string{"Bash"}}}
	for _, backend := range AllBackends() {
		t.Run(string(backend), func(t *testing.T) {
			err := RequireToolPolicySupport(backend, policy)
			if supported[backend] {
				if err != nil {
					t.Fatalf("%s must carry a tool policy, got %v", backend, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s silently drops the deny-list; want a loud refusal", backend)
			}
			// The message has to name the offending tools and a way forward, or the
			// operator cannot tell which knob to remove.
			for _, want := range []string{string(backend), "Bash", string(BackendClaudeCLI)} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestRequireToolPolicySupport_EmptyPolicyAlwaysPasses keeps the guard scoped to
// runs that actually declared a policy: every backend must stay usable without one.
func TestRequireToolPolicySupport_EmptyPolicyAlwaysPasses(t *testing.T) {
	for _, backend := range AllBackends() {
		if err := RequireToolPolicySupport(backend, Permissions{}); err != nil {
			t.Errorf("%s rejected an empty policy: %v", backend, err)
		}
		// A mode alone is not a per-tool policy.
		if err := RequireToolPolicySupport(backend, Permissions{Mode: PermissionPlan}); err != nil {
			t.Errorf("%s rejected a bare permission mode: %v", backend, err)
		}
	}
}

// TestRequireToolPolicySupport_AllowListToo pins that an allow-list is refused on
// the same backends: where there is no tool filter, an allowlist is equally
// unenforced, and silently ignoring it grants more than the spec allowed.
func TestRequireToolPolicySupport_AllowListToo(t *testing.T) {
	policy := Permissions{Tools: Tools{Allow: []string{"Read"}}}
	if err := RequireToolPolicySupport(BackendCodexCLI, policy); err == nil {
		t.Fatal("codex-cli silently drops an allow-list; want a loud refusal")
	}
	if err := RequireToolPolicySupport(BackendClaudeCLI, policy); err != nil {
		t.Fatalf("claude-cli must carry an allow-list, got %v", err)
	}
}

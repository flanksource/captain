package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRequireToolPolicySupport enumerates which runtimes may carry a per-tool
// policy. A deny-list exists only to forbid a tool, so a runtime that drops it
// runs with strictly more authority than the spec granted — the run must fail
// instead. The table is the contract: adding a provider×mode cell forces a
// decision here.
func TestRequireToolPolicySupport(t *testing.T) {
	supported := map[Runtime]bool{
		{Provider: "anthropic", Mode: ModeCLI}:   true,
		{Provider: "anthropic", Mode: ModeAgent}: true,
		{Provider: "anthropic", Mode: ModeCmux}:  true,
	}

	policy := Permissions{Tools: Tools{"Bash": ToolPolicyDeny}}
	for _, runtime := range AllRuntimes() {
		t.Run(runtime.String(), func(t *testing.T) {
			p, ok := runtime.ModelProvider()
			if !ok {
				t.Fatalf("AllRuntimes returned %s, which resolves to no provider", runtime)
			}
			err := RequireToolPolicySupport(p, runtime.Mode, policy)
			if supported[runtime] {
				if err != nil {
					t.Fatalf("%s must carry a tool policy, got %v", runtime, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s silently drops the deny-list; want a loud refusal", runtime)
			}
			// The message has to name the offending tools and a way forward, or the
			// operator cannot tell which knob to remove.
			for _, want := range []string{runtime.String(), "Bash", RuntimeOf(Anthropic, ModeCLI).String()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestRequireToolPolicySupport_EmptyPolicyAlwaysPasses keeps the guard scoped to
// runs that actually declared a policy: every runtime must stay usable without one.
func TestRequireToolPolicySupport_EmptyPolicyAlwaysPasses(t *testing.T) {
	for _, runtime := range AllRuntimes() {
		p, ok := runtime.ModelProvider()
		if !ok {
			t.Fatalf("AllRuntimes returned %s, which resolves to no provider", runtime)
		}
		if err := RequireToolPolicySupport(p, runtime.Mode, Permissions{}); err != nil {
			t.Errorf("%s rejected an empty policy: %v", runtime, err)
		}
	}
}

// legacyTools decodes the deprecated {allow, deny, modes} object, which is the
// only way "on"/"off" enter the policy map now that Tools is the map itself.
func legacyTools(t *testing.T, body string) Tools {
	t.Helper()
	var tools Tools
	if err := json.Unmarshal([]byte(body), &tools); err != nil {
		t.Fatalf("decode legacy tools %s: %v", body, err)
	}
	return tools
}

// TestRequireToolPolicySupport_NormalizesToolModes pins that the guard reads the
// canonical policy map: legacy `modes: {Bash: off}` decodes to a deny, so a
// backend with no tool filter must refuse it exactly like an explicit deny.
func TestRequireToolPolicySupport_NormalizesToolModes(t *testing.T) {
	policy := Permissions{Tools: legacyTools(t, `{"modes":{"Bash":"off"}}`)}
	err := RequireToolPolicySupport(OpenAI, ModeCLI, policy)
	if err == nil {
		t.Fatal("codex-cli silently drops an off tool mode; want a loud refusal")
	}
	if !strings.Contains(err.Error(), "Bash") {
		t.Errorf("error %q does not name the offending tool", err)
	}
	if err := RequireToolPolicySupport(Anthropic, ModeCLI, policy); err != nil {
		t.Fatalf("claude-cli must carry an off tool mode, got %v", err)
	}
	// Legacy `on` resolves to auto in this encoding — the allow list already
	// carries allow — and auto constrains nothing, so it needs no backend support.
	auto := Permissions{Tools: legacyTools(t, `{"modes":{"Read":"on"}}`)}
	if auto.Tools["Read"] != ToolPolicyAuto {
		t.Fatalf(`legacy modes "on" = %q, want auto`, auto.Tools["Read"])
	}
	if err := RequireToolPolicySupport(OpenAI, ModeCLI, auto); err != nil {
		t.Errorf("an auto policy constrains nothing but was refused: %v", err)
	}
}

// TestRequireToolPolicySupport_AskIsRefusedEverywhere pins the gap the tool
// policy cannot express: no transport has a per-tool prompt, so an `ask` would
// resolve to "allowed" even on the runtimes that advertise support.
func TestRequireToolPolicySupport_AskIsRefusedEverywhere(t *testing.T) {
	policy := Permissions{Tools: Tools{"Bash": ToolPolicyAsk}}
	for _, runtime := range AllRuntimes() {
		p, ok := runtime.ModelProvider()
		if !ok {
			t.Fatalf("AllRuntimes returned %s, which resolves to no provider", runtime)
		}
		err := RequireToolPolicySupport(p, runtime.Mode, policy)
		if err == nil {
			t.Errorf("%s accepted an unenforceable ask policy", runtime)
			continue
		}
		if !strings.Contains(err.Error(), "Bash") {
			t.Errorf("%s: error %q does not name the offending tool", runtime, err)
		}
	}
}

// TestToolsAllowDenyLists pins the projection every claude transport reads,
// exercised through the legacy shape because that is where the two spellings of
// a deny meet: an explicit `deny` list and a `modes: off` entry must both land
// in DenyList, and a `modes: on` entry must land in neither — it means auto here.
func TestToolsAllowDenyLists(t *testing.T) {
	tools := legacyTools(t, `{"allow":["Read"],"deny":["WebFetch"],"modes":{"Bash":"off","Glob":"on"}}`)
	if got := tools.DenyList(); len(got) != 2 || got[0] != "Bash" || got[1] != "WebFetch" {
		t.Errorf("DenyList() = %v, want [Bash WebFetch]", got)
	}
	if got := tools.AllowList(); len(got) != 1 || got[0] != "Read" {
		t.Errorf("AllowList() = %v, want [Read]", got)
	}
	if tools["Glob"] != ToolPolicyAuto {
		t.Errorf(`Glob = %q, want auto — legacy "on" is not allow in this encoding`, tools["Glob"])
	}
}

// TestRequireToolPolicySupport_AllowListToo pins that an allow-list is refused on
// the same backends: where there is no tool filter, an allowlist is equally
// unenforced, and silently ignoring it grants more than the spec allowed.
func TestRequireToolPolicySupport_AllowListToo(t *testing.T) {
	policy := Permissions{Tools: Tools{"Read": ToolPolicyAllow}}
	if err := RequireToolPolicySupport(OpenAI, ModeCLI, policy); err == nil {
		t.Fatal("codex-cli silently drops an allow-list; want a loud refusal")
	}
	if err := RequireToolPolicySupport(Anthropic, ModeCLI, policy); err != nil {
		t.Fatalf("claude-cli must carry an allow-list, got %v", err)
	}
}

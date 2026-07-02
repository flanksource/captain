package api

import (
	"reflect"
	"testing"
)

// enumStrings extracts the "enum" values from a SchemaDescriber map as strings.
func enumStrings(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["enum"].([]any)
	if !ok {
		t.Fatalf("schema has no []any enum: %#v", m["enum"])
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("enum value %d is not a string: %#v", i, v)
		}
		out[i] = s
	}
	return out
}

func TestCodexSandboxJSONSchema(t *testing.T) {
	m := CodexSandbox("").JSONSchema()
	if got := m["type"]; got != "string" {
		t.Errorf("type = %v, want string", got)
	}
	want := []string{"read-only", "workspace-write", "danger-full-access"}
	if got := enumStrings(t, m); !reflect.DeepEqual(got, want) {
		t.Errorf("enum = %v, want %v", got, want)
	}
	if got := m["default"]; got != string(CodexSandboxReadOnly) {
		t.Errorf("default = %v, want %q", got, CodexSandboxReadOnly)
	}
	if got := m["x-enum-display"]; got != "radio" {
		t.Errorf("x-enum-display = %v, want radio", got)
	}
	labels, ok := m["x-enum-labels"].(map[string]string)
	if !ok || len(labels) != len(want) {
		t.Errorf("x-enum-labels = %#v, want %d entries", m["x-enum-labels"], len(want))
	}
}

func TestCodexApprovalJSONSchema(t *testing.T) {
	m := CodexApprovalPolicy("").JSONSchema()
	want := []string{"untrusted", "on-failure", "on-request", "never"}
	if got := enumStrings(t, m); !reflect.DeepEqual(got, want) {
		t.Errorf("enum = %v, want %v", got, want)
	}
	if got := m["default"]; got != string(CodexApprovalOnRequest) {
		t.Errorf("default = %v, want %q", got, CodexApprovalOnRequest)
	}
}

func TestCodexSafety(t *testing.T) {
	cases := []struct {
		name         string
		perms        Permissions
		wantSandbox  CodexSandbox
		wantApproval CodexApprovalPolicy
	}{
		{"bypass is full access", Permissions{Mode: PermissionBypass}, CodexSandboxDangerFull, CodexApprovalNever},
		{"edit preset is workspace write", Permissions{Presets: []Preset{PresetEdit}}, CodexSandboxWorkspaceWrite, CodexApprovalOnRequest},
		{"default is read-only", Permissions{}, CodexSandboxReadOnly, CodexApprovalOnRequest},
		{"edit preset with explicit mode stays read-only", Permissions{Mode: PermissionDefault, Presets: []Preset{PresetEdit}}, CodexSandboxReadOnly, CodexApprovalOnRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox, approval := CodexSafety(tc.perms)
			if sandbox != tc.wantSandbox || approval != tc.wantApproval {
				t.Errorf("CodexSafety() = (%q, %q), want (%q, %q)", sandbox, approval, tc.wantSandbox, tc.wantApproval)
			}
		})
	}
}

func TestCLIOptionsFor(t *testing.T) {
	if v, err := CLIOptionsFor(BackendClaudeCmux); err != nil {
		t.Errorf("claude-cmux: unexpected error %v", err)
	} else if _, ok := v.(ClaudeCmuxOptions); !ok {
		t.Errorf("claude-cmux: got %T, want ClaudeCmuxOptions", v)
	}
	if v, err := CLIOptionsFor(BackendCodexCmux); err != nil {
		t.Errorf("codex-cmux: unexpected error %v", err)
	} else if _, ok := v.(CodexCmuxOptions); !ok {
		t.Errorf("codex-cmux: got %T, want CodexCmuxOptions", v)
	}
	if _, err := CLIOptionsFor(BackendClaudeCLI); err == nil {
		t.Errorf("claude-cli: want error for non-cmux backend, got nil")
	}
}

// TestClaudeCmuxOptionsHasNoSpecFlags guards the Group-A/Group-B split: the extra
// option struct must not redeclare a flag that already has an api.Spec home (model,
// effort, permission mode, allow/deny tools, or the memory-derived flags), which
// would create a second source of truth for that flag.
func TestClaudeCmuxOptionsHasNoSpecFlags(t *testing.T) {
	specOwned := map[string]bool{
		"model": true, "effort": true, "permission-mode": true,
		"allowedTools": true, "disallowedTools": true,
		"bare": true, "disable-slash-commands": true, "setting-sources": true,
		"session-id": true, "resume": true,
	}
	for _, flag := range structFlags(ClaudeCmuxOptions{}) {
		if specOwned[flag] {
			t.Errorf("ClaudeCmuxOptions declares spec-owned flag %q", flag)
		}
	}
}

func structFlags(v any) []string {
	rt := reflect.TypeOf(v)
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i).Tag.Get("flag"); f != "" {
			out = append(out, f)
		}
	}
	return out
}

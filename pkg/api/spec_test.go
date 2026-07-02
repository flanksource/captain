package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/shell"
	"gopkg.in/yaml.v3"
)

func floatPtr(f float64) *float64 { return &f }

// sampleSpec is a fully-populated spec used across round-trip and render tests.
func sampleSpec() Spec {
	return Spec{
		Model:  Model{Name: "claude-sonnet-4-6", Backend: BackendAnthropic, Temperature: floatPtr(0.7), Effort: EffortXHigh, NoCache: true},
		Prompt: Prompt{User: "refactor the parser", System: "be precise", Source: "cli"},
		Budget: Budget{Cost: 2.5, MaxTokens: 8000, MaxTurns: 5, Timeout: "120s"},
		Memory: Memory{Skills: []string{"/skills/a"}, SkipUser: true},
		Permissions: Permissions{
			Mode:    PermissionAcceptEdits,
			Presets: []Preset{PresetEdit},
			Tools:   Tools{Allow: []string{"Edit", "Read"}, Modes: map[string]ToolMode{"Bash": ToolModeAsk}},
			MCP:     MCP{Disabled: true},
			Plugins: ResourcePolicies{"/plugins": ResourceEnabled},
			Skills:  ResourcePolicies{"/skills/b": ResourceDisabled},
		},
		Setup: &shell.Setup{
			Cwd:    "/repo",
			DotEnv: []string{".env"},
			Checkout: &shell.Checkout{
				Mode: shell.CheckoutLocal,
				Path: "/repo",
				Worktree: &shell.Worktree{
					Mode:   shell.WorktreeNew,
					Prefix: "captain",
				},
			},
		},
		SessionID: "sess-1",
	}
}

func TestSpec_JSONRoundTrip(t *testing.T) {
	in := sampleSpec()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Spec
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("JSON round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestSpec_YAMLRoundTrip(t *testing.T) {
	in := sampleSpec()
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Spec
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("YAML round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

// TestSpec_JSONFieldNames pins the nested camelCase wire shape callers depend on.
func TestSpec_JSONFieldNames(t *testing.T) {
	data, _ := json.Marshal(sampleSpec())
	s := string(data)
	for _, want := range []string{`"model"`, `"prompt"`, `"budget"`, `"permissions"`, `"setup"`, `"maxTokens"`, `"maxTurns"`, `"timeout"`, `"noCache"`, `"skipUser"`, `"sessionId"`, `"effort":"xhigh"`} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled spec missing %s\ngot: %s", want, s)
		}
	}
}

// TestSpec_ModelInlined pins that the embedded Model is flattened to the spec's
// top level (json:",inline"/yaml:",inline") for both encoders, rather than nested
// under a "model" object — so "model"/"effort" are top-level keys.
func TestSpec_ModelInlined(t *testing.T) {
	for _, tc := range []struct {
		name      string
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{
		{"json", json.Marshal, json.Unmarshal},
		{"yaml", yaml.Marshal, yaml.Unmarshal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.marshal(sampleSpec())
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var top map[string]any
			if err := tc.unmarshal(data, &top); err != nil {
				t.Fatalf("unmarshal to map: %v", err)
			}
			for _, key := range []string{"model", "effort", "prompt", "budget"} {
				if _, ok := top[key]; !ok {
					t.Errorf("top-level key %q missing (Model not inlined?); keys=%v", key, keysOf(top))
				}
			}
			if _, nested := top["name"]; nested {
				t.Errorf("found top-level %q; model name should serialize as \"model\"", "name")
			}
		})
	}
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestSpec_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(*Spec) {}, ""},
		{"missing model", func(s *Spec) { s.Model.Name = "" }, "model name is required"},
		{"bad effort", func(s *Spec) { s.Model.Effort = "extreme" }, "invalid reasoning effort"},
		{"temp too high", func(s *Spec) { s.Model.Temperature = floatPtr(3) }, "0.0-2.0"},
		{"bad backend", func(s *Spec) { s.Model.Backend = "nope" }, "invalid backend"},
		{"empty prompt", func(s *Spec) { s.Prompt.User = "" }, "prompt text is required"},
		{"negative budget", func(s *Spec) { s.Budget.Cost = -1 }, "budget cost"},
		{"too many turns", func(s *Spec) { s.Budget.MaxTurns = 200 }, "0-100"},
		{"bad permission mode", func(s *Spec) { s.Permissions.Mode = "yolo" }, "invalid permission mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sampleSpec()
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

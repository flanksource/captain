package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPermissions_JSONPolicyShape(t *testing.T) {
	in := Permissions{
		Tools: Tools{
			Allow: []string{"Read"},
			Deny:  []string{"Bash"},
			Modes: map[string]ToolMode{
				"WebSearch": ToolModeAsk,
				"Write":     ToolModeOn,
			},
		},
		MCP: MCP{
			Servers: []string{"filesystem", "gavel"},
			Modes: ResourcePolicies{
				"gavel": ResourceDisabled,
				"ado":   ResourceDisabled,
			},
		},
		Plugins: ResourcePolicies{"/Users/moshe/.codex/plugins/captain": ResourceDisabled},
		Skills:  ResourcePolicies{"$CWD/.skills": ResourceEnabled},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"Read":"allow"`,
		`"Bash":"deny"`,
		`"WebSearch":"ask"`,
		`"Write":"auto"`,
		`"servers":["filesystem","gavel"]`,
		`"gavel":"disabled"`,
		`"/Users/moshe/.codex/plugins/captain":"disabled"`,
		`"$CWD/.skills":"enabled"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshalled permissions missing %s\ngot: %s", want, got)
		}
	}
}

func TestPermissions_JSONLegacyInput(t *testing.T) {
	data := []byte(`{
	  "tools": {
	    "allow": ["Read"],
	    "deny": ["Bash"],
	    "modes": {"Edit": "ask", "Write": "on"}
	  },
	  "mcp": {"servers": ["filesystem", "gavel"], "gavel": "disabled"},
	  "plugins": ["/plugins"],
	  "skills": ["/skills"]
	}`)

	var out Permissions
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out.Tools.Policies(), map[string]ToolPolicy{
		"Read":  ToolPolicyAllow,
		"Bash":  ToolPolicyDeny,
		"Edit":  ToolPolicyAsk,
		"Write": ToolPolicyAuto,
	}) {
		t.Fatalf("tool policies = %#v", out.Tools.Policies())
	}
	if got := out.MCP.EnabledServers(); !reflect.DeepEqual(got, []string{"filesystem"}) {
		t.Fatalf("enabled MCP servers = %v, want filesystem", got)
	}
	if got := out.Plugins.Enabled(); !reflect.DeepEqual(got, []string{"/plugins"}) {
		t.Fatalf("enabled plugins = %v", got)
	}
	if got := out.Skills.Enabled(); !reflect.DeepEqual(got, []string{"/skills"}) {
		t.Fatalf("enabled skills = %v", got)
	}
}

func TestPermissions_YAMLLegacyInput(t *testing.T) {
	data := []byte(`
tools:
  allow:
    - Read
  deny:
    - Bash
mcp:
  servers:
    - filesystem
  gavel: disabled
plugins:
  - /plugins
skills:
  $CWD/.skills: enabled
`)

	var out Permissions
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Tools.Policies()["Read"] != ToolPolicyAllow {
		t.Fatalf("tools = %#v", out.Tools.Policies())
	}
	if out.MCP.Modes["gavel"] != ResourceDisabled {
		t.Fatalf("mcp modes = %#v", out.MCP.Modes)
	}
	if out.Plugins["/plugins"] != ResourceEnabled {
		t.Fatalf("plugins = %#v", out.Plugins)
	}
	if out.Skills["$CWD/.skills"] != ResourceEnabled {
		t.Fatalf("skills = %#v", out.Skills)
	}
}

// TestTools_UnrecognisedPolicyFailsAtDecode pins the decode boundary as the place
// a mistyped tool policy surfaces. The policy map is the only representation the
// value ever has: applyPolicy translates it into allow/deny/modes, so a policy it
// does not recognise leaves no trace for Permissions.Validate to inspect
// afterwards, and the tool runs under whatever posture it inherited instead.
func TestTools_UnrecognisedPolicyFailsAtDecode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		decode func(any) error
	}{
		{"yaml", func(v any) error { return yaml.Unmarshal([]byte("tools:\n  Bash: sometimes\n"), v) }},
		{"json", func(v any) error { return json.Unmarshal([]byte(`{"tools":{"Bash":"sometimes"}}`), v) }},
		{"json legacy shape", func(v any) error {
			return json.Unmarshal([]byte(`{"tools":{"allow":["Read"],"Bash":"sometimes"}}`), v)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out Permissions
			err := tc.decode(&out)
			if err == nil {
				t.Fatalf("decode accepted an unrecognised policy, tools = %#v", out.Tools)
			}
			if !strings.Contains(err.Error(), `invalid tool policy "sometimes" for tool "Bash"`) {
				t.Fatalf("error = %v, want it to name the policy and the tool", err)
			}
		})
	}
}

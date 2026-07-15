package api

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrompt_UnmarshalJSON_Shorthand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Prompt
	}{
		{"bare string", `"review strictly"`, Prompt{User: "review strictly"}},
		{"object form", `{"user":"u","system":"s"}`, Prompt{User: "u", System: "s"}},
		{"empty string", `""`, Prompt{User: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Prompt
			if err := json.Unmarshal([]byte(tc.in), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.User != tc.want.User || p.System != tc.want.System {
				t.Errorf("got %+v, want %+v", p, tc.want)
			}
		})
	}
}

func TestPrompt_UnmarshalYAML_Shorthand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Prompt
	}{
		{"scalar", `review strictly`, Prompt{User: "review strictly"}},
		{"object", "user: u\nsystem: s", Prompt{User: "u", System: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Prompt
			if err := yaml.Unmarshal([]byte(tc.in), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.User != tc.want.User || p.System != tc.want.System {
				t.Errorf("got %+v, want %+v", p, tc.want)
			}
		})
	}
}

// TestSpec_PromptShorthand pins that the shorthand works through the enclosing
// Spec — `prompt: "text"` alongside an inlined model — for both encoders.
func TestSpec_PromptShorthand(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var s Spec
		if err := json.Unmarshal([]byte(`{"model":"m","prompt":"hi there"}`), &s); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if s.Model.Name != "m" || s.Prompt.User != "hi there" {
			t.Errorf("got model=%q prompt.user=%q", s.Model.Name, s.Prompt.User)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		var s Spec
		if err := yaml.Unmarshal([]byte("model: m\nprompt: hi there\n"), &s); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if s.Model.Name != "m" || s.Prompt.User != "hi there" {
			t.Errorf("got model=%q prompt.user=%q", s.Model.Name, s.Prompt.User)
		}
	})
}

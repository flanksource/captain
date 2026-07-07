package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func modelNames(models []Model) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.Name
	}
	return out
}

func TestModel_ExpandCSV(t *testing.T) {
	cases := []struct {
		name          string
		in            Model
		wantName      string
		wantFallbacks []string
	}{
		{"single", Model{Name: "claude-sonnet-5"}, "claude-sonnet-5", nil},
		{"csv three", Model{Name: "a,b,c"}, "a", []string{"b", "c"}},
		{"spaces trimmed", Model{Name: " a , b "}, "a", []string{"b"}},
		{
			"csv prepended before explicit fallbacks",
			Model{Name: "a,b", Fallbacks: []Model{{Name: "z"}}},
			"a", []string{"b", "z"},
		},
		{"empty name unchanged", Model{Name: ""}, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.ExpandCSV()
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if names := modelNames(got.Fallbacks); !reflect.DeepEqual(names, tc.wantFallbacks) {
				t.Errorf("Fallbacks = %v, want %v", names, tc.wantFallbacks)
			}
			// Idempotent: a second expansion changes nothing.
			if again := got.ExpandCSV(); !reflect.DeepEqual(again, got) {
				t.Errorf("ExpandCSV not idempotent:\n first=%+v\nsecond=%+v", got, again)
			}
		})
	}
}

func TestModel_Candidates(t *testing.T) {
	primary := Model{
		Name:        "claude-sonnet-5",
		Effort:      EffortHigh,
		Temperature: floatPtr(0.3),
		NoCache:     true,
		Fallbacks: []Model{
			{Name: "gpt-4o"}, // inherits primary effort/temp/noCache
			{Name: "gemini-2.0-flash", Effort: EffortLow, ID: "drop-me", Fallbacks: []Model{{Name: "nested"}}},
		},
	}
	got := primary.Candidates()

	if names := modelNames(got); !reflect.DeepEqual(names, []string{"claude-sonnet-5", "gpt-4o", "gemini-2.0-flash"}) {
		t.Fatalf("candidate order = %v", names)
	}
	if got[0].Fallbacks != nil {
		t.Errorf("primary candidate should not carry Fallbacks, got %v", got[0].Fallbacks)
	}
	// gpt-4o inherits the primary's effort/temperature/noCache.
	if got[1].Effort != EffortHigh {
		t.Errorf("fallback effort = %q, want inherited %q", got[1].Effort, EffortHigh)
	}
	if got[1].Temperature == nil || *got[1].Temperature != 0.3 {
		t.Errorf("fallback temperature = %v, want inherited 0.3", got[1].Temperature)
	}
	if !got[1].NoCache {
		t.Errorf("fallback NoCache = false, want inherited true")
	}
	// gemini keeps its own effort, drops ID and nested fallbacks.
	if got[2].Effort != EffortLow {
		t.Errorf("fallback own effort = %q, want %q", got[2].Effort, EffortLow)
	}
	if got[2].ID != "" {
		t.Errorf("fallback ID = %q, want cleared", got[2].ID)
	}
	if got[2].Fallbacks != nil {
		t.Errorf("nested fallbacks should be dropped, got %v", got[2].Fallbacks)
	}
}

func TestModel_Candidates_SingleWhenNoFallbacks(t *testing.T) {
	if got := (Model{Name: "claude-sonnet-5"}).Candidates(); len(got) != 1 {
		t.Fatalf("Candidates len = %d, want 1", len(got))
	}
}

func TestModel_Validate_Fallbacks(t *testing.T) {
	cases := []struct {
		name    string
		model   Model
		wantErr string
	}{
		{"valid list", Model{Name: "claude-sonnet-5", Fallbacks: []Model{{Name: "gpt-4o", Effort: EffortHigh}}}, ""},
		{"empty fallback name", Model{Name: "claude-sonnet-5", Fallbacks: []Model{{Name: ""}}}, "fallback[0]: model name is required"},
		{"bad fallback temp", Model{Name: "claude-sonnet-5", Fallbacks: []Model{{Name: "gpt-4o", Temperature: floatPtr(3)}}}, "fallback[0] \"gpt-4o\""},
		{"bad fallback effort", Model{Name: "claude-sonnet-5", Fallbacks: []Model{{Name: "gpt-4o", Effort: "extreme"}}}, "fallback[0] \"gpt-4o\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.model.Validate()
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

// TestModel_FallbacksRoundTrip pins that object-form fallbacks survive JSON and
// YAML round-trips with their per-model knobs intact.
func TestModel_FallbacksRoundTrip(t *testing.T) {
	in := Model{
		Name: "claude-sonnet-5",
		Fallbacks: []Model{
			{Name: "gpt-4o", Effort: EffortHigh},
			{Name: "gemini-2.0-flash", Temperature: floatPtr(0.2)},
		},
	}
	for _, tc := range []struct {
		name      string
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{
		{"json", json.Marshal, json.Unmarshal},
		{"yaml", yaml.Marshal, yaml.Unmarshal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.marshal(in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var out Model
			if err := tc.unmarshal(data, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Errorf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
			}
		})
	}
}

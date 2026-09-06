package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseCompactElement(t *testing.T) {
	cases := []struct {
		in      string
		want    Model
		wantErr string
	}{
		{in: "opus", want: Model{Name: "opus"}},
		{in: "claude-sonnet-4-6", want: Model{Name: "claude-sonnet-4-6"}},
		{in: "agent:opus:high", want: Model{Name: "opus", Effort: EffortHigh, Mode: ModeAgent}},
		{in: "cmux:opus", want: Model{Name: "opus", Mode: ModeCmux}},
		{in: "api:opus", want: Model{Name: "opus", Mode: ModeAPI}},
		{in: "cli:opus", want: Model{Name: "opus", Mode: ModeCLI}},
		{in: "cmux:codex:medium", want: Model{Name: "codex", Effort: EffortMedium, Mode: ModeCmux}},
		{in: "api:gpt-5.5", want: Model{Name: "gpt-5.5", Mode: ModeAPI}},
		{in: "  agent : opus : high ", want: Model{Name: "opus", Effort: EffortHigh, Mode: ModeAgent}},
		{in: "opus:high", wantErr: "invalid model configuration"},
		{in: "sdk:opus:high", wantErr: "invalid model configuration"},
		{in: "anthropic:opus:high", wantErr: "invalid model configuration"},
		{in: "claude-agent:opus", wantErr: "invalid model configuration"},
		{in: "bogusmode:opus:high", wantErr: "invalid model configuration"},
		{in: "opus:notaneffort", wantErr: "invalid model configuration"},
		{in: "a:b:c:d", wantErr: "too many"},
		{in: "", wantErr: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseCompactElement(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestModel_Expand(t *testing.T) {
	t.Run("plain name unchanged", func(t *testing.T) {
		got, err := Model{Name: "opus", Effort: EffortHigh}.Expand()
		if err != nil || got.Name != "opus" || got.Effort != EffortHigh {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("compact name parsed", func(t *testing.T) {
		got, err := Model{Name: "agent:opus:high"}.Expand()
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "opus" || got.Effort != EffortHigh || got.Mode != ModeAgent {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("csv tail becomes fallbacks", func(t *testing.T) {
		got, err := Model{Name: "agent:opus:high, api:sonnet:medium, api:gpt-5.5"}.Expand()
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "opus" || got.Effort != EffortHigh {
			t.Errorf("primary = %+v", got)
		}
		if len(got.Fallbacks) != 2 {
			t.Fatalf("fallbacks = %+v", got.Fallbacks)
		}
		if got.Fallbacks[0].Name != "sonnet" || got.Fallbacks[0].Effort != EffortMedium {
			t.Errorf("fb0 = %+v", got.Fallbacks[0])
		}
		if got.Fallbacks[1].Name != "gpt-5.5" || got.Fallbacks[1].Mode != ModeAPI {
			t.Errorf("fb1 = %+v", got.Fallbacks[1])
		}
	})
	t.Run("preserves explicit fields not in compact", func(t *testing.T) {
		got, err := Model{Name: "cmux:opus:high"}.Expand()
		if err != nil {
			t.Fatal(err)
		}
		if got.Mode != ModeCmux {
			t.Errorf("backend = %q, want cmux", got.Mode)
		}
	})
	t.Run("bad compact errors", func(t *testing.T) {
		if _, err := (Model{Name: "opus:nope"}).Expand(); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestModelList_Unmarshal(t *testing.T) {
	t.Run("json mixed string and object", func(t *testing.T) {
		var l ModelList
		if err := json.Unmarshal([]byte(`["agent:opus:high", {"model":"sonnet","effort":"medium"}]`), &l); err != nil {
			t.Fatal(err)
		}
		if len(l) != 2 {
			t.Fatalf("len = %d", len(l))
		}
		if l[0].Name != "agent:opus:high" || l[0].Mode != "" || l[0].Effort != "" {
			t.Errorf("l0 = %+v", l[0])
		}
		if l[1].Name != "sonnet" || l[1].Effort != EffortMedium {
			t.Errorf("l1 = %+v", l[1])
		}
	})
	t.Run("yaml mixed", func(t *testing.T) {
		var l ModelList
		if err := yaml.Unmarshal([]byte("- api:opus\n- model: sonnet\n  effort: high\n"), &l); err != nil {
			t.Fatal(err)
		}
		if len(l) != 2 || l[0].Name != "api:opus" || l[0].Mode != "" || l[1].Name != "sonnet" {
			t.Errorf("got %+v", l)
		}
	})
}

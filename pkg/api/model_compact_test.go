package api

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
		{in: "opus:high", want: Model{Name: "opus", Effort: EffortHigh}},
		{in: "sonnet:medium", want: Model{Name: "sonnet", Effort: EffortMedium}},
		{in: "agent:opus:high", want: Model{Name: "opus", Effort: EffortHigh, Backend: BackendClaudeAgent}},
		{in: "sdk:opus:high", want: Model{Name: "opus", Effort: EffortHigh, Backend: BackendClaudeAgent}}, // sdk = agent
		{in: "cmux:opus", want: Model{Name: "opus", Backend: BackendClaudeCmux}},
		{in: "api:opus", want: Model{Name: "opus", Backend: BackendAnthropic}},
		{in: "cli:opus", want: Model{Name: "opus", Backend: BackendClaudeCLI}},
		{in: "cmux:codex:medium", want: Model{Name: "codex", Effort: EffortMedium, Backend: BackendCodexCmux}},
		{in: "api:gpt-5.5", want: Model{Name: "gpt-5.5", Backend: BackendOpenAI}},
		{in: "  opus : high ", want: Model{Name: "opus", Effort: EffortHigh}},
		{in: "cmux:gemini-2.0", wantErr: "not supported for gemini"}, // no gemini cmux
		{in: "bogusmode:opus:high", wantErr: "invalid mode"},
		{in: "opus:notaneffort", wantErr: "ambiguous"},
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
		if got.Name != "opus" || got.Effort != EffortHigh || got.Backend != BackendClaudeAgent {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("csv tail becomes fallbacks", func(t *testing.T) {
		got, err := Model{Name: "opus:high, sonnet:medium, api:gpt-5.5"}.Expand()
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
		if got.Fallbacks[1].Name != "gpt-5.5" || got.Fallbacks[1].Backend != BackendOpenAI {
			t.Errorf("fb1 = %+v", got.Fallbacks[1])
		}
	})
	t.Run("preserves explicit fields not in compact", func(t *testing.T) {
		got, err := Model{Name: "opus:high", Backend: BackendClaudeCmux}.Expand()
		if err != nil {
			t.Fatal(err)
		}
		if got.Backend != BackendClaudeCmux {
			t.Errorf("backend = %q, want preserved claude-cmux", got.Backend)
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
		if l[0].Name != "opus" || l[0].Backend != BackendClaudeAgent || l[0].Effort != EffortHigh {
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
		if len(l) != 2 || l[0].Name != "opus" || l[0].Backend != BackendAnthropic || l[1].Name != "sonnet" {
			t.Errorf("got %+v", l)
		}
	})
}

// TestSpec_FallbacksCompact pins the end-to-end config shape: a Spec whose model
// has compact-string fallbacks, plus the primary parsed via Expand.
func TestSpec_FallbacksCompact(t *testing.T) {
	var spec Spec
	src := "model: opus\neffort: high\nfallbacks:\n  - agent:sonnet:medium\n  - api:gpt-5.5\n"
	if err := yaml.Unmarshal([]byte(src), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Model.Name != "opus" || spec.Model.Effort != EffortHigh {
		t.Fatalf("primary = %+v", spec.Model)
	}
	if len(spec.Model.Fallbacks) != 2 {
		t.Fatalf("fallbacks = %+v", spec.Model.Fallbacks)
	}
	if spec.Model.Fallbacks[0].Name != "sonnet" || spec.Model.Fallbacks[0].Backend != BackendClaudeAgent {
		t.Errorf("fb0 = %+v", spec.Model.Fallbacks[0])
	}
	// Candidates flattens primary + fallbacks in order.
	cands := spec.Model.Candidates()
	if len(cands) != 3 || cands[0].Name != "opus" || cands[1].Name != "sonnet" || cands[2].Name != "gpt-5.5" {
		t.Errorf("candidates = %+v", cands)
	}
}

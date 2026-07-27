package api

import (
	"reflect"
	"testing"
)

func TestSpec_Merge(t *testing.T) {
	cases := []struct {
		name     string
		base     Spec
		override Spec
		check    func(t *testing.T, got Spec)
	}{
		{
			name:     "empty override preserves base",
			base:     sampleSpec(),
			override: Spec{},
			check: func(t *testing.T, got Spec) {
				if !reflect.DeepEqual(got, sampleSpec()) {
					t.Fatalf("empty override mutated base:\n got=%+v", got)
				}
			},
		},
		{
			name:     "override model name wins, base budget/effort kept",
			base:     Spec{Model: Model{Name: "base-model", Effort: EffortMedium}, Budget: Budget{Cost: 5}},
			override: Spec{Model: Model{Name: "op-model"}},
			check: func(t *testing.T, got Spec) {
				if got.Model.Name != "op-model" {
					t.Errorf("Name = %q, want op-model", got.Model.Name)
				}
				if got.Model.Effort != EffortMedium {
					t.Errorf("Effort = %q, want medium (from base)", got.Model.Effort)
				}
				if got.Budget.Cost != 5 {
					t.Errorf("Budget.Cost = %v, want 5 (from base)", got.Budget.Cost)
				}
			},
		},
		{
			name:     "empty override model keeps base model",
			base:     Spec{Model: Model{Name: "base-model", Effort: EffortHigh}},
			override: Spec{Budget: Budget{Cost: 3}},
			check: func(t *testing.T, got Spec) {
				if got.Model.Name != "base-model" || got.Model.Effort != EffortHigh {
					t.Errorf("model = %+v, want base preserved", got.Model)
				}
				if got.Budget.Cost != 3 {
					t.Errorf("Budget.Cost = %v, want 3 (override)", got.Budget.Cost)
				}
			},
		},
		{
			name:     "budget merges field-wise",
			base:     Spec{Model: Model{Name: "m"}, Budget: Budget{Cost: 5, MaxTokens: 1000, MaxTurns: 10}},
			override: Spec{Budget: Budget{Cost: 9}},
			check: func(t *testing.T, got Spec) {
				if got.Budget.Cost != 9 {
					t.Errorf("Cost = %v, want 9", got.Budget.Cost)
				}
				if got.Budget.MaxTokens != 1000 || got.Budget.MaxTurns != 10 {
					t.Errorf("budget lost base fields: %+v", got.Budget)
				}
			},
		},
		{
			name:     "prompt user overrides, base system kept",
			base:     Spec{Model: Model{Name: "m"}, Prompt: Prompt{User: "base body", System: "be precise"}},
			override: Spec{Prompt: Prompt{User: "op body"}},
			check: func(t *testing.T, got Spec) {
				if got.Prompt.User != "op body" {
					t.Errorf("User = %q, want op body", got.Prompt.User)
				}
				if got.Prompt.System != "be precise" {
					t.Errorf("System = %q, want base system kept", got.Prompt.System)
				}
			},
		},
		{
			name:     "fallbacks replace wholesale",
			base:     Spec{Model: Model{Name: "m", Fallbacks: []Model{{Name: "a"}, {Name: "b"}}}},
			override: Spec{Model: Model{Fallbacks: []Model{{Name: "c"}}}},
			check: func(t *testing.T, got Spec) {
				if len(got.Model.Fallbacks) != 1 || got.Model.Fallbacks[0].Name != "c" {
					t.Errorf("Fallbacks = %+v, want [c]", got.Model.Fallbacks)
				}
			},
		},
		{
			name:     "temperature pointer: nil override keeps base",
			base:     Spec{Model: Model{Name: "m", Temperature: floatPtr(0.7)}},
			override: Spec{Model: Model{Name: "m"}},
			check: func(t *testing.T, got Spec) {
				if got.Model.Temperature == nil || *got.Model.Temperature != 0.7 {
					t.Errorf("Temperature = %v, want 0.7 kept", got.Model.Temperature)
				}
			},
		},
		{
			name:     "temperature pointer: explicit 0.0 override wins",
			base:     Spec{Model: Model{Name: "m", Temperature: floatPtr(0.7)}},
			override: Spec{Model: Model{Temperature: floatPtr(0)}},
			check: func(t *testing.T, got Spec) {
				if got.Model.Temperature == nil || *got.Model.Temperature != 0 {
					t.Errorf("Temperature = %v, want explicit 0.0", got.Model.Temperature)
				}
			},
		},
		{
			name:     "bool noCache: base true survives override false",
			base:     Spec{Model: Model{Name: "m", NoCache: true}},
			override: Spec{Model: Model{Name: "m", NoCache: false}},
			check: func(t *testing.T, got Spec) {
				if !got.Model.NoCache {
					t.Errorf("NoCache = false, want base true preserved (false=unset)")
				}
			},
		},
		{
			name:     "setup pointer replaced when set in override",
			base:     Spec{Model: Model{Name: "m"}},
			override: Spec{Model: Model{Name: "m"}, SessionID: "sess-op"},
			check: func(t *testing.T, got Spec) {
				if got.SessionID != "sess-op" {
					t.Errorf("SessionID = %q, want sess-op", got.SessionID)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.base.Merge(tc.override)
			tc.check(t, got)
		})
	}
}

// TestSpec_Merge_DoesNotMutateInputs guards that Merge is pure — neither the base
// nor the override is modified (slice/map aliasing aside, the top-level structs
// must be untouched so a shared base spec can be merged repeatedly).
func TestSpec_Merge_DoesNotMutateInputs(t *testing.T) {
	base := Spec{Model: Model{Name: "base", Effort: EffortLow}, Budget: Budget{Cost: 1}}
	override := Spec{Model: Model{Name: "op"}, Budget: Budget{Cost: 2}}
	baseCopy, overrideCopy := base, override
	_ = base.Merge(override)
	if !reflect.DeepEqual(base, baseCopy) {
		t.Errorf("Merge mutated base: %+v != %+v", base, baseCopy)
	}
	if !reflect.DeepEqual(override, overrideCopy) {
		t.Errorf("Merge mutated override: %+v != %+v", override, overrideCopy)
	}
}

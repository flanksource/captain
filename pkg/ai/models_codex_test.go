package ai

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestParseCodexDebugModels(t *testing.T) {
	models, err := ParseCodexDebugModels([]byte(`{
  "models": [
    {"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"ultra"}],"visibility":"list","priority":1},
    {"slug":"codex-auto-review","visibility":"hide","priority":2}
  ]
}`))
	if err != nil {
		t.Fatalf("ParseCodexDebugModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	got := models[0]
	if got.ID != "gpt-5.6-sol" || got.DefaultEffort != api.EffortNone || got.Priority != 1 || got.ReleaseDate != "2026-07-09" {
		t.Fatalf("model = %+v", got)
	}
	wantEfforts := []api.Effort{api.EffortLow, api.EffortMedium, api.EffortHigh, api.EffortXHigh, api.EffortMax, api.EffortUltra}
	if !reflect.DeepEqual(got.SupportedEfforts, wantEfforts) {
		t.Fatalf("efforts = %v, want %v", got.SupportedEfforts, wantEfforts)
	}
}

func TestParseCodexDebugModelsRequiresVisibleRows(t *testing.T) {
	if _, err := ParseCodexDebugModels([]byte(`{"models":[{"slug":"hidden","visibility":"hide"}]}`)); err == nil {
		t.Fatal("expected empty visible catalog error")
	}
}

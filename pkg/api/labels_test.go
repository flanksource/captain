package api

import (
	"reflect"
	"testing"
)

// TestSpec_HostLabelsDropsRuntimeOwnedKeys pins the split that keeps a task row
// honest: a caller may name its run, but it may not describe the runtime it is
// about to get. Providers add "budget" and "runSession" only when the run really
// has one, so a caller value surviving here would be reported as the effective
// budget of a run with no budget at all.
func TestSpec_HostLabelsDropsRuntimeOwnedKeys(t *testing.T) {
	spec := Spec{Labels: map[string]string{
		"title":      "Stack trace viewer",
		"href":       "/todos/abc",
		"blank":      "  ",
		"budget":     "999",
		"runSession": "not-a-session",
		"model":      "gpt-4",
		"provider":   "openai",
		"mode":       "agent",
	}}

	want := map[string]string{"title": "Stack trace viewer", "href": "/todos/abc"}
	if got := spec.HostLabels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("HostLabels() = %v, want %v", got, want)
	}
}

func TestSpec_HostLabelsIsAFreshMap(t *testing.T) {
	spec := Spec{Labels: map[string]string{"title": "one"}}

	labels := spec.HostLabels()
	labels["model"] = "resolved-later"

	if _, ok := spec.Labels["model"]; ok {
		t.Fatal("a provider writing its own facts must not reach back into the request")
	}
}

func TestSpec_HostLabelReadsOnlyCallerKeys(t *testing.T) {
	spec := Spec{Labels: map[string]string{"title": " padded ", "budget": "999"}}

	if got := spec.HostLabel("title"); got != "padded" {
		t.Fatalf("HostLabel(title) = %q, want %q", got, "padded")
	}
	if got := spec.HostLabel("budget"); got != "" {
		t.Fatalf("HostLabel(budget) = %q, want a runtime-owned key to read empty", got)
	}
}

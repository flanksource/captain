package assistanttags

import "testing"

func TestEnvelopeSummary(t *testing.T) {
	planEnvelopeJSON := `{"endStatus":"completed","plan":{"content":"","path":"/Users/moshe/.codex/plans/todo-review-banner-radio-buttons.md","status":"new"},"questions":[],"summary":"Authored and verified a complete read-only implementation plan.\n\n"}`
	resultEnvelopeJSON := `{"summary":"Implemented the fix and ran the tests.","endStatus":"failed","questions":[]}`

	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"full plan envelope, summary trimmed", planEnvelopeJSON, "Authored and verified a complete read-only implementation plan.", true},
		{"result envelope without plan", resultEnvelopeJSON, "Implemented the fix and ran the tests.", true},
		{"ask envelope", `{"summary":"Blocked on a question.","endStatus":"ask","questions":[{"text":"which db?"}]}`, "Blocked on a question.", true},
		{"leading and trailing whitespace", "\n  {\"summary\":\"done\",\"endStatus\":\"completed\"}  \n", "done", true},
		{"unrelated json object missing endStatus", `{"summary":"looks like a summary but no status"}`, "", false},
		{"invalid endStatus value", `{"summary":"x","endStatus":"in_progress"}`, "", false},
		{"blank summary", `{"summary":"   ","endStatus":"completed"}`, "", false},
		{"ordinary prose", "Here is my plan: first do X, then Y.", "", false},
		{"json array not object", `[{"summary":"x","endStatus":"completed"}]`, "", false},
		{"trailing garbage after object", `{"summary":"x","endStatus":"completed"} and more`, "", false},
		{"empty string", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EnvelopeSummary(tt.input)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("EnvelopeSummary(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

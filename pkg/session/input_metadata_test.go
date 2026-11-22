package session

import (
	"reflect"
	"testing"
)

const taskNotificationPrompt = `<task-notification>
<task-id>b224qho4x</task-id>
<tool-use-id>toolu_01TFApWXyuWXXYbu3Sh6rPNw</tool-use-id>
<output-file>/cache/tasks/b224qho4x.output</output-file>
<status>completed</status>
<summary>Background command "Follow commons-db PR 142 checks" completed (exit code 0)</summary>
</task-notification>`

func TestParseInputMetadataReadsAnXMLEnvelopePromptIntoFields(t *testing.T) {
	got := ParseInputMetadata(taskNotificationPrompt)
	want := &InputMetadata{Tag: "task-notification", Fields: map[string]string{
		"task-id":     "b224qho4x",
		"tool-use-id": "toolu_01TFApWXyuWXXYbu3Sh6rPNw",
		"output-file": "/cache/tasks/b224qho4x.output",
		"status":      "completed",
		"summary":     `Background command "Follow commons-db PR 142 checks" completed (exit code 0)`,
	}, title: `Background command "Follow commons-db PR 142 checks" completed (exit code 0)`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestParseInputMetadataReadsSiblingTagPromptsWithUnescapedText(t *testing.T) {
	prompt := "<command-message>review</command-message>\n<command-name>/review</command-name>\n<command-args>fix a && b</command-args>"
	got := ParseInputMetadata(prompt)
	want := &InputMetadata{Fields: map[string]string{
		"command-message": "review", "command-name": "/review", "command-args": "fix a && b",
	}, title: "review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestParseInputMetadataLeavesProsePromptsAlone(t *testing.T) {
	for _, prompt := range []string{
		"Fix the parser",
		"Render <b>bold</b> titles",
		"<task>wrap</task> then explain it",
		"<summary>Fix <b>bold</b> titles</summary>",
		"<broken>",
		"",
	} {
		if got := ParseInputMetadata(prompt); got != nil {
			t.Fatalf("ParseInputMetadata(%q) = %#v, want nil", prompt, got)
		}
	}
}

func TestDeriveSessionTitleNamesAnEnvelopePromptByItsSummary(t *testing.T) {
	want := `Background command "Follow commons-db PR 142 checks" completed (exit code 0)`
	if title := DeriveTitle(taskNotificationPrompt); title != want {
		t.Fatalf("title = %q, want %q", title, want)
	}
	if title := DeriveTitle("<task-notification><task-id>x1</task-id></task-notification>"); title != "task notification" {
		t.Fatalf("title = %q, want the humanized tag", title)
	}
}

func TestApplySessionIdentityRecordsInputMetadata(t *testing.T) {
	s := &Session{InitialPrompt: taskNotificationPrompt}
	applySessionIdentity(s)
	if s.InputMetadata == nil || s.InputMetadata.Fields["status"] != "completed" {
		t.Fatalf("input metadata = %#v, want the parsed envelope", s.InputMetadata)
	}
}

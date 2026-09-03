package tools

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// VerifyTool renders one verify verdict — the loop's definition of done, voting
// on the turn that just finished.
//
// It is its own row rather than a system line because it is the run's outcome:
// a reader scanning a transcript for why a run stopped is looking for exactly
// these, and a pass and a failure have to be told apart at a glance rather than
// by reading the sentence.
type VerifyTool struct {
	BaseTool
	// Failed distinguishes the two roles this renders; NewTool sets it from the
	// name so the row can colour itself without re-reading the text.
	Failed bool
}

func (t *VerifyTool) Name() string {
	if t.Failed {
		return "Verify failed"
	}
	return "Verified"
}

func (t *VerifyTool) Category() string    { return "verify" }
func (t *VerifyTool) FilePath() string    { return "" }
func (t *VerifyTool) ExtractPath() string { return "" }

func (t *VerifyTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "✓", Iconify: "mdi:check-circle", Style: "text-green-500"}
	label, color := "verified", "text-green-500 font-medium"
	if t.Failed {
		icon = icons.Icon{Unicode: "✗", Iconify: "mdi:close-circle", Style: "text-red-500"}
		label, color = "verify", "text-red-500 font-medium"
	}
	text := t.header(icon, label, color)
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+messagePreview(body), "text-muted")
	}
	return text
}

func (t *VerifyTool) Detail() api.Textable {
	if denied := t.BaseTool.Detail(); denied != nil {
		return denied
	}
	return messageDetail(t.Str("text"))
}

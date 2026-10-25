package cli

import (
	"path/filepath"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

func invalidPromptLink(source promptSource, rel string, err error) promptRecord {
	source.Writable = false
	return promptRecord{
		Source: source, ID: encodePromptID(source.Kind, source.ID, rel),
		Path: filepath.Join(source.Root, rel), Rel: filepath.ToSlash(rel), LinkError: err,
	}
}

func promptLinkWarning(record promptRecord, err error) PromptSummary {
	summary := basePromptSummary(record)
	summary.Writable = false
	diagnostic := promptlib.SymlinkDiagnostic(err)
	diagnostic.Message += " (" + record.Path + ")"
	summary.Diagnostics = []promptlib.Diagnostic{diagnostic}
	return summary
}

func (p PromptSummary) RowDetail() clickyapi.Textable {
	if p.ParseError == "" && len(p.Diagnostics) == 0 {
		return nil
	}
	text := clicky.Text("")
	if p.ParseError != "" {
		text = text.Append(p.ParseError, "error")
	}
	for _, diagnostic := range p.Diagnostics {
		if !text.IsEmpty() {
			text = text.NewLine()
		}
		text = text.Append(diagnostic.Severity+": ", "warning").Append(diagnostic.Message)
	}
	return text
}

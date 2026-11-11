package promptfile

import (
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/gavel/linters"
	"github.com/flanksource/gavel/models"
)

// Results preserves Gavel's JSON array while providing a prompt-specific display.
type Results []*linters.LinterResult

func (results Results) Pretty() api.Text {
	files, errors, warnings := 0, 0, 0
	for _, result := range results {
		files += result.FileCount
		errors += result.Test().Sum().Failed
		for _, violation := range result.Violations {
			if violation.Severity == models.SeverityWarning {
				warnings++
			}
		}
	}
	style := "success"
	if errors > 0 {
		style = "error"
	} else if warnings > 0 {
		style = "warning"
	}
	errorLabel, warningLabel := "errors", "warnings"
	if errors == 1 {
		errorLabel = "error"
	}
	if warnings == 1 {
		warningLabel = "warning"
	}
	return clicky.Text("Prompt lint", "font-bold").Append(fmt.Sprintf(": %d checked, %d %s, %d %s", files, errors, errorLabel, warnings, warningLabel), style)
}

func (results Results) GetChildren() []api.TreeNode {
	var children []api.TreeNode
	for _, result := range results {
		if result.Error != "" {
			children = append(children, result)
		} else {
			children = append(children, result.GetChildren()...)
		}
	}
	return children
}

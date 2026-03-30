package tools

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/sergi/go-diff/diffmatchpatch"
)

func CreateUnifiedDiff(oldStr, newStr string) api.Text {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldStr, newStr, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	result := clicky.Text("")
	var removedLines, addedLines, contextLines []string
	oldLineNum, newLineNum := 1, 1

	for _, d := range diffs {
		lines := strings.Split(d.Text, "\n")
		for i, line := range lines {
			if i == len(lines)-1 && line == "" && len(lines) > 1 {
				continue
			}
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				result = flushDiffLines(result, removedLines, addedLines, &oldLineNum, &newLineNum)
				removedLines, addedLines = nil, nil
				contextLines = append(contextLines, line)
				if len(contextLines) > 3 {
					contextLines = contextLines[len(contextLines)-3:]
				}
				oldLineNum++
				newLineNum++
			case diffmatchpatch.DiffDelete:
				if len(removedLines) == 0 && len(addedLines) == 0 {
					for _, cl := range contextLines {
						result = result.Append(fmt.Sprintf(" %s", cl), "text-gray-400").NewLine()
					}
					contextLines = nil
				}
				removedLines = append(removedLines, line)
			case diffmatchpatch.DiffInsert:
				if len(removedLines) == 0 && len(addedLines) == 0 && len(contextLines) > 0 {
					for _, cl := range contextLines {
						result = result.Append(fmt.Sprintf(" %s", cl), "text-gray-400").NewLine()
					}
					contextLines = nil
				}
				addedLines = append(addedLines, line)
			}
		}
	}
	return flushDiffLines(result, removedLines, addedLines, &oldLineNum, &newLineNum)
}

func flushDiffLines(result api.Text, removedLines, addedLines []string, oldLineNum, newLineNum *int) api.Text {
	if len(removedLines) == 0 && len(addedLines) == 0 {
		return result
	}
	if len(removedLines) == 1 && len(addedLines) == 1 {
		result = result.
			Append(fmt.Sprintf("-%d ", *oldLineNum), "text-red-500").
			Append(removedLines[0], "text-red-500").NewLine().
			Append(fmt.Sprintf("+%d ", *newLineNum), "text-green-500").
			Append(addedLines[0], "text-green-500").NewLine()
		*oldLineNum++
		*newLineNum++
		return result
	}
	for _, line := range removedLines {
		result = result.Append(fmt.Sprintf("-%d ", *oldLineNum), "text-red-500").Append(line, "text-red-500").NewLine()
		*oldLineNum++
	}
	for _, line := range addedLines {
		result = result.Append(fmt.Sprintf("+%d ", *newLineNum), "text-green-500").Append(line, "text-green-500").NewLine()
		*newLineNum++
	}
	return result
}

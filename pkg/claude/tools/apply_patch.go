package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	applyPatchFileRE   = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: ([^\r\n]+)\r?$`)
	applyPatchMoveRE   = regexp.MustCompile(`(?m)^\*\*\* Move to: ([^\r\n]+)\r?$`)
	javascriptStringRE = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
)

// ExtractApplyPatchPaths returns every file header in a native or JavaScript-wrapped Codex patch.
func ExtractApplyPatchPaths(input string) []string {
	var paths []string
	for _, payload := range applyPatchPayloads(input) {
		for _, pattern := range []*regexp.Regexp{applyPatchFileRE, applyPatchMoveRE} {
			for _, match := range pattern.FindAllStringSubmatch(payload, -1) {
				if path := strings.TrimSpace(match[1]); path != "" {
					paths = append(paths, path)
				}
			}
		}
	}
	return paths
}

func applyPatchPayloads(input string) []string {
	if !strings.Contains(input, "tools.apply_patch") {
		return []string{input}
	}
	var payloads []string
	for _, literal := range javascriptStringRE.FindAllString(input, -1) {
		var decoded string
		if json.Unmarshal([]byte(literal), &decoded) == nil && strings.Contains(decoded, "*** Begin Patch") {
			payloads = append(payloads, decoded)
		}
	}
	return payloads
}

package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

func historyFileForRun(backend api.Backend, sessionID, cwd string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	switch backend {
	case api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
		return claudeHistoryFile(sessionID, cwd)
	case api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux:
		return codexHistoryFile(sessionID)
	default:
		return ""
	}
}

func claudeHistoryFile(sessionID, cwd string) string {
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	return filepath.Join(claude.GetProjectsDir(), claude.NormalizePath(abs), sessionID+".jsonl")
}

func codexHistoryFile(sessionID string) string {
	files, err := history.FindCodexSessionFiles()
	if err != nil || len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	for _, file := range files {
		if strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)) == sessionID {
			return file
		}
		meta, err := history.ReadCodexSessionMeta(file)
		if err == nil && meta != nil && meta.ID == sessionID {
			return file
		}
	}
	for _, file := range files {
		if strings.HasPrefix(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), sessionID) {
			return file
		}
	}
	return ""
}

func variantModel(model api.Model, fallbacks []api.Model) api.Model {
	if len(fallbacks) > 0 {
		model.Fallbacks = fallbacks
	}
	return model
}

package cli

import (
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

type ToolAnalysis struct {
	ReadPaths  []string `json:"readPaths,omitempty"`
	WritePaths []string `json:"writePaths,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	Binaries   []string `json:"binaries,omitempty"`
}

// AnalyzeToolUseLegacy is the old API for callers still using claude.ToolUse.
func AnalyzeToolUseLegacy(tu claude.ToolUse, projectRoot string) ToolAnalysis {
	base := tools.BaseTool{
		RawTool:     tu.Tool,
		Input:       tu.Input,
		ProjectRoot: projectRoot,
	}
	return AnalyzeToolUse(tools.NewTool(base))
}

func AnalyzeToolUse(t tools.Tool) ToolAnalysis {
	var a ToolAnalysis
	base := t.Base()

	rel := func(path string) string {
		return claude.RelativePath(path, base.ProjectRoot)
	}

	switch base.RawTool {
	case "Read":
		if path := t.FilePath(); path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Grep":
		if path, ok := base.Input["path"].(string); ok && path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Glob":
		if path, ok := base.Input["path"].(string); ok && path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Write", "Edit":
		if path := t.FilePath(); path != "" {
			a.WritePaths = append(a.WritePaths, rel(path))
		}
	case "Bash":
		a.analyzeBash(base.Input, rel)
		command, _ := base.Input["command"].(string)
		for _, path := range extractApplyPatchPaths(command) {
			a.WritePaths = appendUnique(a.WritePaths, rel(path))
		}
	case "exec":
		input, _ := base.Input["input"].(string)
		for _, path := range extractApplyPatchPaths(input) {
			a.WritePaths = appendUnique(a.WritePaths, rel(path))
		}
	case "WebFetch":
		if urlStr, ok := base.Input["url"].(string); ok {
			if u, err := url.Parse(urlStr); err == nil && u.Host != "" {
				a.Domains = append(a.Domains, u.Hostname())
			}
		}
	case "WebSearch":
		a.Domains = append(a.Domains, "api.anthropic.com")
	default:
		if strings.HasPrefix(base.RawTool, "mcp__") {
			domains := make(map[string]bool)
			for _, v := range base.Input {
				if s, ok := v.(string); ok {
					extractURLDomains(s, domains)
				}
			}
			a.Domains = sortedKeys(domains)
		}
	}

	sort.Strings(a.ReadPaths)
	sort.Strings(a.WritePaths)
	sort.Strings(a.Domains)
	sort.Strings(a.Binaries)
	return a
}

func (a *ToolAnalysis) analyzeBash(input map[string]any, rel func(string) string) {
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return
	}

	result, err := bash.Analyze(cmd)
	if result == nil {
		return
	}
	_ = err

	for _, op := range result.Operations {
		a.WritePaths = appendUnique(a.WritePaths, rel(op.Path))
	}
	for _, path := range result.ReferencedPaths {
		a.ReadPaths = appendUnique(a.ReadPaths, rel(path))
	}
	for _, path := range extractWritePathsFromBash(cmd) {
		a.WritePaths = appendUnique(a.WritePaths, rel(path))
	}

	binaries := make(map[string]bool)
	extractBinariesFromInput(input, binaries)
	a.Binaries = sortedKeys(binaries)

	domains := make(map[string]bool)
	extractDomainsFromInput(input, domains)
	a.Domains = sortedKeys(domains)
}

func extractBinariesFromInput(input map[string]any, binaries map[string]bool) {
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return
	}
	result, _ := bash.Analyze(cmd)
	if result == nil {
		return
	}
	for _, c := range result.Commands {
		if fields := strings.Fields(c); len(fields) > 0 {
			binary := filepath.Base(fields[0])
			if !skipBinaries[binary] {
				binaries[binary] = true
			}
		}
	}
}

func extractDomainsFromInput(input map[string]any, domains map[string]bool) {
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return
	}
	for pattern, ds := range domainPatterns {
		if pattern.MatchString(cmd) {
			for _, d := range ds {
				domains[d] = true
			}
		}
	}
	extractURLDomains(cmd, domains)
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

func FormatPathsWithIcons(readPaths, writePaths []string) string {
	var parts []string
	seen := make(map[string]bool)
	for _, p := range readPaths {
		dir := pathToDir(p)
		key := "r:" + dir
		if !seen[key] {
			seen[key] = true
			parts = append(parts, "⬇ "+dir)
		}
	}
	for _, p := range writePaths {
		dir := pathToDir(p)
		key := "w:" + dir
		if !seen[key] {
			seen[key] = true
			parts = append(parts, "⬆ "+dir)
		}
	}
	return strings.Join(parts, " ")
}

func pathToDir(p string) string {
	if strings.HasSuffix(p, "/") || filepath.Ext(p) == "" {
		return p
	}
	dir := filepath.Dir(p)
	if dir == "." {
		return "./"
	}
	return dir + "/"
}

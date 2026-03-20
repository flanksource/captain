package cli

import (
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
)

type ToolAnalysis struct {
	ReadPaths  []string `json:"readPaths,omitempty"`
	WritePaths []string `json:"writePaths,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	Binaries   []string `json:"binaries,omitempty"`
}

func AnalyzeToolUse(tu claude.ToolUse, projectRoot string) ToolAnalysis {
	var a ToolAnalysis

	rel := func(path string) string {
		return claude.RelativePath(path, projectRoot)
	}

	switch tu.Tool {
	case "Read":
		if path := tu.FilePath(); path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Grep":
		if path, ok := tu.Input["path"].(string); ok && path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Glob":
		if path, ok := tu.Input["path"].(string); ok && path != "" {
			a.ReadPaths = append(a.ReadPaths, rel(path))
		}
	case "Write", "Edit":
		if path := tu.FilePath(); path != "" {
			a.WritePaths = append(a.WritePaths, rel(path))
		}
	case "Bash":
		a.analyzeBash(tu, rel)
	case "WebFetch":
		if urlStr, ok := tu.Input["url"].(string); ok {
			if u, err := url.Parse(urlStr); err == nil && u.Host != "" {
				a.Domains = append(a.Domains, u.Hostname())
			}
		}
	case "WebSearch":
		a.Domains = append(a.Domains, "api.anthropic.com")
	default:
		if strings.HasPrefix(tu.Tool, "mcp__") {
			domains := make(map[string]bool)
			for _, v := range tu.Input {
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

func (a *ToolAnalysis) analyzeBash(tu claude.ToolUse, rel func(string) string) {
	cmd, _ := tu.Input["command"].(string)
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
	extractBinaries(tu, binaries)
	a.Binaries = sortedKeys(binaries)

	domains := make(map[string]bool)
	extractDomains(tu, domains)
	a.Domains = sortedKeys(domains)
}

func appendUnique(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

// FormatPathsWithIcons returns a single string with ⬇/⬆ prefixed directories for table display.
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

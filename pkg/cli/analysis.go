package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// ToolAnalysis is the file/network footprint of one tool use. ReadPaths and
// WritePaths are ABSOLUTE: they are data, shared across sessions and processes
// (gavel stages from them), so they are anchored to the cwd of the tool use that
// produced them rather than relativised against whatever base that call had.
// Relativise with DisplayPath at the point of display.
type ToolAnalysis struct {
	ReadPaths  []string `json:"readPaths,omitempty"`
	WritePaths []string `json:"writePaths,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	Binaries   []string `json:"binaries,omitempty"`
}

// AnalyzeToolUseLegacy is the old API for callers still using claude.ToolUse.
func AnalyzeToolUseLegacy(tu claude.ToolUse, projectRoot string) ToolAnalysis {
	base := tools.BaseTool{
		RawTool: tu.Tool,
		Input:   tu.Input,
		// CWD anchors the tool's relative paths; without it a bash `cat pkg/x.go`
		// could only be guessed at from the project root.
		CWD:         tu.CWD,
		ProjectRoot: projectRoot,
	}
	return AnalyzeToolUse(tools.NewTool(base))
}

func AnalyzeToolUse(t tools.Tool) ToolAnalysis {
	var a ToolAnalysis
	base := t.Base()

	abs := func(path string) string {
		return claude.AbsolutePath(path, base.CWD, base.ProjectRoot)
	}

	// Which files a tool touched is resolved through history's canonical
	// footprint rather than a second set of rules here, so the paths reported to
	// callers that stage from them cannot drift from the ones the session
	// builders persist and the agent runner records.
	footprint := history.ToolFootprint(history.ToolUse{Tool: base.RawTool, Input: base.Input, CWD: base.CWD})
	for _, path := range footprint.Written {
		a.WritePaths = appendUnique(a.WritePaths, abs(path))
	}
	for _, path := range footprint.Read {
		a.ReadPaths = appendUnique(a.ReadPaths, abs(path))
	}

	switch base.RawTool {
	case "Bash":
		a.analyzeBash(base.Input)
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

// analyzeBash covers the parts of a shell command that are not a file
// footprint: which binaries it invokes and which hosts it can reach. The read
// and write paths come from history.ToolFootprint with every other tool's.
func (a *ToolAnalysis) analyzeBash(input map[string]any) {
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return
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

// displayBase is the working directory display paths render against, resolved
// once per process.
var displayBase = sync.OnceValue(func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
})

// DisplayPath renders a canonical (absolute) path for humans: relative to the
// working directory when it sits inside it, otherwise the absolute path — a long
// ../../.. chain reads worse than the full path, and a file from another project
// is genuinely elsewhere.
//
// Display only. Never feed the result back into anything that resolves paths:
// that round trip is what made session paths ambiguous in the first place.
func DisplayPath(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	base := displayBase()
	if base == "" {
		return path
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// DisplayPaths maps DisplayPath over a slice, for rendering a path list.
func DisplayPaths(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = DisplayPath(p)
	}
	return out
}

func FormatPathsWithIcons(readPaths, writePaths []string) string {
	var parts []string
	seen := make(map[string]bool)
	for _, p := range readPaths {
		dir := pathToDir(DisplayPath(p))
		key := "r:" + dir
		if !seen[key] {
			seen[key] = true
			parts = append(parts, "⬇ "+dir)
		}
	}
	for _, p := range writePaths {
		dir := pathToDir(DisplayPath(p))
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

package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/logger"
)

type SRTGenerateOptions struct {
	Since       time.Time `flag:"since" help:"History window" default:"now-7d" short:"s"`
	All         bool      `flag:"all" help:"Scan all projects" short:"a"`
	Output      string    `flag:"output" help:"Write output to file instead of stdout" short:"o"`
	Merge       bool      `flag:"merge" help:"Merge with existing srt-settings.json"`
	MergeFrom   string    `flag:"merge-from" help:"Existing config to merge with" default:"~/.srt-settings.json"`
	ExtraDomain []string  `flag:"extra-domain" help:"Additional domains to allow"`
	DenyRead    []string  `flag:"deny-read" help:"Additional paths to deny reads" default:"~/.ssh,~/.gnupg"`
}

type SRTConfig struct {
	Network     SRTNetwork     `json:"network"`
	Filesystem  SRTFilesystem  `json:"filesystem"`
	Environment SRTEnvironment `json:"environment"`
	Binaries    []string       `json:"binaries,omitempty"`
}

type SRTEnvironment struct {
	Passthrough []string `json:"passthrough"`
}

type SRTNetwork struct {
	AllowedDomains []string `json:"allowedDomains"`
	DeniedDomains  []string `json:"deniedDomains"`
}

type SRTFilesystem struct {
	DenyRead   []string `json:"denyRead"`
	AllowWrite []string `json:"allowWrite"`
	DenyWrite  []string `json:"denyWrite"`
}

var DefaultEnvPassthrough = []string{
	"ALL_PROXY",
	"BUN_INSTALL",
	"BUILDKITE",
	"CARGO_HOME",
	"CARGO_TARGET_DIR",
	"CI",
	"CIRCLECI",
	"CLICOLOR",
	"CLICOLOR_FORCE",
	"COLORTERM",
	"COLUMNS",
	"CONDA_PREFIX",
	"CONTINUOUS_INTEGRATION",
	"DENO_DIR",
	"DOTNET_CLI_HOME",
	"EDITOR",
	"FORCE_COLOR",
	"GEM_HOME",
	"GEM_PATH",
	"GITHUB_ACTIONS",
	"GIT_AUTHOR_EMAIL",
	"GIT_AUTHOR_NAME",
	"GIT_COMMITTER_EMAIL",
	"GIT_COMMITTER_NAME",
	"GIT_EDITOR",
	"GIT_PAGER",
	"GITLAB_CI",
	"GO111MODULE",
	"GOBIN",
	"GOCACHE",
	"GOMAXPROCS",
	"GOMODCACHE",
	"GOPATH",
	"GOROOT",
	"GRADLE_USER_HOME",
	"HOME",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"JAVA_HOME",
	"JDK_HOME",
	"LANG",
	"LANGUAGE",
	"LC_*",
	"LINES",
	"LOGNAME",
	"M2_HOME",
	"MAVEN_HOME",
	"NODE_PATH",
	"NO_COLOR",
	"NO_PROXY",
	"NPM_CONFIG_CACHE",
	"NPM_CONFIG_COLOR",
	"NPM_CONFIG_PREFIX",
	"PAGER",
	"PATH",
	"PIP_CACHE_DIR",
	"PIP_DISABLE_PIP_VERSION_CHECK",
	"PIP_NO_COLOR",
	"PIP_NO_INPUT",
	"PIP_PROGRESS_BAR",
	"PIP_REQUIRE_VIRTUALENV",
	"PIP_ROOT_USER_ACTION",
	"PIP_TIMEOUT",
	"PIPX_BIN_DIR",
	"PIPX_HOME",
	"PNPM_HOME",
	"POETRY_CACHE_DIR",
	"POETRY_HOME",
	"POETRY_VIRTUALENVS_IN_PROJECT",
	"POETRY_VIRTUALENVS_PATH",
	"PWD",
	"PYTHONDONTWRITEBYTECODE",
	"PYTHONHOME",
	"PYTHONNOUSERSITE",
	"PYTHONPATH",
	"PYTHONPYCACHEPREFIX",
	"PYTHONUNBUFFERED",
	"PYTHONUSERBASE",
	"RUSTUP_HOME",
	"RUSTUP_TOOLCHAIN",
	"SHELL",
	"SHLVL",
	"TEMP",
	"TERM",
	"TERM_PROGRAM",
	"TERM_PROGRAM_VERSION",
	"TF_BUILD",
	"TMP",
	"TMPDIR",
	"TRAVIS",
	"TZ",
	"USER",
	"USERNAME",
	"UV_CACHE_DIR",
	"UV_PYTHON_INSTALL_DIR",
	"UV_TOOL_BIN_DIR",
	"UV_TOOL_DIR",
	"VIRTUAL_ENV",
	"VISUAL",
	"WORKON_HOME",
	"XDG_BIN_HOME",
	"XDG_CACHE_HOME",
	"XDG_CONFIG_DIRS",
	"XDG_CONFIG_HOME",
	"XDG_DATA_DIRS",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"YARN_CACHE_FOLDER",
	"YARN_ENABLE_COLORS",
	"YARN_GLOBAL_FOLDER",
	"all_proxy",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

var blockedEnvPatterns = []string{
	"*_TOKEN", "*_KEY", "*_SECRET", "*_PASSWORD", "*_CREDENTIALS",
	"AWS_*", "AZURE_*", "GOOGLE_*",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
	"DATABASE_URL", "PG*", "MYSQL_*", "REDIS_*",
	"KUBECONFIG",
	"NPM_TOKEN", "COMPOSER_AUTH", "BUNDLE_USER_CONFIG",
	"PIP_CONFIG_FILE", "PIP_INDEX_URL", "NPM_CONFIG_USERCONFIG",
	"SSH_AUTH_SOCK", "SSH_AGENT_PID",
	"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS",
	"XDG_RUNTIME_DIR",
	"TMUX",
}

func classifyEnvVar(name string, passthrough []string) string {
	for _, p := range passthrough {
		if matchEnvPattern(p, name) {
			return "passthrough"
		}
	}
	for _, p := range blockedEnvPatterns {
		if matchEnvPattern(p, name) {
			return "blocked"
		}
	}
	return "ignored"
}

func matchEnvPattern(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(name, pattern[1:])
	}
	return false
}

func logEnvClassification(passthrough []string) {
	if !logger.IsDebugEnabled() {
		return
	}
	var passed, blocked, ignored []string
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		switch classifyEnvVar(name, passthrough) {
		case "passthrough":
			passed = append(passed, name)
		case "blocked":
			blocked = append(blocked, name)
		default:
			ignored = append(ignored, name)
		}
	}
	sort.Strings(passed)
	sort.Strings(blocked)
	sort.Strings(ignored)
	logger.Debugf("env passthrough (%d): %s", len(passed), strings.Join(passed, ", "))
	logger.Debugf("env blocked (%d): %s", len(blocked), strings.Join(blocked, ", "))
	logger.Debugf("env ignored (%d): %s", len(ignored), strings.Join(ignored, ", "))
}

func RunSRTGenerate(opts SRTGenerateOptions) (any, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	filter := claude.Filter{Since: &opts.Since}
	parseResult, err := claude.ParseHistory(cwd, opts.All, filter)
	if err != nil {
		return nil, err
	}

	denyRead := opts.DenyRead
	if len(denyRead) == 0 {
		denyRead = []string{"~/.ssh", "~/.gnupg"}
	}

	config := SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: make([]string, 0),
			DeniedDomains:  make([]string, 0),
		},
		Filesystem: SRTFilesystem{
			DenyRead:   denyRead,
			AllowWrite: []string{".", "/tmp"},
			DenyWrite:  []string{".env"},
		},
		Environment: SRTEnvironment{
			Passthrough: DefaultEnvPassthrough,
		},
	}

	domains := make(map[string]bool)
	writeDirs := map[string]bool{".": true, "/tmp": true}
	binaries := make(map[string]bool)
	projectRoot := claude.FindProjectRoot(cwd)

	for _, tu := range parseResult.ToolUses {
		if tu.ProjectRoot == "" {
			tu.ProjectRoot = projectRoot
		}
		analysis := AnalyzeToolUse(tu, projectRoot)
		for _, p := range analysis.WritePaths {
			addDir(writeDirs, p, "")
		}
		for _, d := range analysis.Domains {
			domains[d] = true
		}
		for _, b := range analysis.Binaries {
			binaries[b] = true
		}
	}

	for _, d := range opts.ExtraDomain {
		domains[d] = true
	}

	config.Network.AllowedDomains = sortedKeys(domains)
	config.Filesystem.AllowWrite = collapseToTopDirs(writeDirs)
	config.Binaries = sortedKeys(binaries)
	logEnvClassification(config.Environment.Passthrough)

	if opts.Merge {
		existing, err := loadSRTConfig(expandHome(opts.MergeFrom))
		if err == nil {
			config = mergeSRTConfigs(existing, config)
		}
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}

	if opts.Output != "" {
		if err := os.WriteFile(expandHome(opts.Output), append(data, '\n'), 0644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", opts.Output, err)
		}
		return SRTResult{
			Output:   opts.Output,
			Domains:  len(config.Network.AllowedDomains),
			Paths:    len(config.Filesystem.AllowWrite),
			Binaries: len(config.Binaries),
		}, nil
	}

	fmt.Println(string(data))
	return nil, nil
}

type SRTResult struct {
	Output   string `json:"output,omitempty" pretty:"label=Output"`
	Domains  int    `json:"domains" pretty:"label=Domains"`
	Paths    int    `json:"paths" pretty:"label=Write Paths"`
	Binaries int    `json:"binaries" pretty:"label=Binaries"`
}

func addDir(dirs map[string]bool, path, projectRoot string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if projectRoot != "" {
		if dir == projectRoot {
			return
		}
		if strings.HasPrefix(dir, projectRoot+"/") {
			dir = dir[len(projectRoot)+1:]
		}
	}
	if dir == "" || dir == "." {
		return
	}
	if filepath.IsAbs(dir) {
		return
	}
	// Keep the first path segment only (topmost directory)
	if i := strings.Index(dir, "/"); i >= 0 {
		dir = dir[:i]
	}
	dirs[dir+"/"] = true
}

// collapseToTopDirs removes child dirs already covered by a parent dir.
func collapseToTopDirs(dirs map[string]bool) []string {
	all := sortedKeys(dirs)
	var result []string
	for _, d := range all {
		covered := false
		for _, parent := range result {
			if strings.HasPrefix(d, parent) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, d)
		}
	}
	return result
}

func extractWritePathsFromBash(cmd string) []string {
	var paths []string
	redir := regexp.MustCompile(`>>?\s+(\S+)`)
	for _, m := range redir.FindAllStringSubmatch(cmd, -1) {
		paths = append(paths, m[1])
	}
	mkdirRe := regexp.MustCompile(`mkdir\s+(?:-p\s+)?(\S+)`)
	for _, m := range mkdirRe.FindAllStringSubmatch(cmd, -1) {
		paths = append(paths, m[1])
	}
	return paths
}

// skipBinaries are shell builtins / trivial commands not worth listing
var skipBinaries = map[string]bool{
	"echo": true, "printf": true, "cd": true, "export": true,
	"set": true, "unset": true, "true": true, "false": true,
	"test": true, "[": true, "[[": true, "exit": true,
	"return": true, "source": true, ".": true, "eval": true,
	"exec": true, "local": true, "declare": true, "typeset": true,
	"readonly": true, "shift": true, "wait": true, "trap": true,
	"read": true, "pushd": true, "popd": true, "dirs": true,
	"alias": true, "unalias": true, "command": true, "builtin": true,
	"type": true, "hash": true, "help": true, "let": true,
	"sleep": true, "kill": true, "pwd": true, "which": true,
	"xargs": true, "tee": true,
}

func extractBinaries(tu claude.ToolUse, binaries map[string]bool) {
	if tu.Tool != "Bash" {
		return
	}
	cmd, _ := tu.Input["command"].(string)
	if cmd == "" {
		return
	}
	result, _ := bash.Analyze(cmd)
	if result == nil {
		return
	}
	for _, c := range result.Commands {
		binary := strings.Fields(c)[0]
		binary = filepath.Base(binary) // strip path prefix like /usr/bin/git → git
		if !skipBinaries[binary] {
			binaries[binary] = true
		}
	}
}

var domainPatterns = map[*regexp.Regexp][]string{
	regexp.MustCompile(`\bgh\b`):                            {"github.com", "*.github.com", "api.github.com"},
	regexp.MustCompile(`\bgit\s+(clone|push|pull|fetch)\b`): {"github.com", "*.github.com"},
	regexp.MustCompile(`\bnpm\s+(install|i|ci|publish)\b`):  {"registry.npmjs.org", "*.npmjs.org"},
	regexp.MustCompile(`\byarn\s+(install|add)\b`):          {"registry.yarnpkg.com", "registry.npmjs.org"},
	regexp.MustCompile(`\bpnpm\s+(install|add)\b`):          {"registry.npmjs.org"},
	regexp.MustCompile(`\bpip3?\s+install\b`):               {"pypi.org", "files.pythonhosted.org"},
	regexp.MustCompile(`\bgo\s+(get|mod\s+download)\b`):     {"proxy.golang.org", "sum.golang.org"},
	regexp.MustCompile(`\bcargo\s+(install|add)\b`):         {"crates.io", "static.crates.io"},
	regexp.MustCompile(`\bbrew\s+install\b`):                {"formulae.brew.sh", "ghcr.io"},
}

func extractDomains(tu claude.ToolUse, domains map[string]bool) {
	if tu.Tool != "Bash" {
		return
	}
	cmd, _ := tu.Input["command"].(string)
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

var urlRe = regexp.MustCompile(`https?://[^\s"'` + "`" + `]+`)

func extractURLDomains(cmd string, domains map[string]bool) {
	for _, match := range urlRe.FindAllString(cmd, -1) {
		if u, err := url.Parse(match); err == nil && u.Host != "" {
			domains[u.Hostname()] = true
		}
	}
}

func loadSRTConfig(path string) (SRTConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SRTConfig{}, err
	}
	var config SRTConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return SRTConfig{}, err
	}
	return config, nil
}

func mergeSRTConfigs(existing, generated SRTConfig) SRTConfig {
	return SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: mergeStringSlices(existing.Network.AllowedDomains, generated.Network.AllowedDomains),
			DeniedDomains:  mergeStringSlices(existing.Network.DeniedDomains, generated.Network.DeniedDomains),
		},
		Filesystem: SRTFilesystem{
			DenyRead:   mergeStringSlices(existing.Filesystem.DenyRead, generated.Filesystem.DenyRead),
			AllowWrite: mergeStringSlices(existing.Filesystem.AllowWrite, generated.Filesystem.AllowWrite),
			DenyWrite:  mergeStringSlices(existing.Filesystem.DenyWrite, generated.Filesystem.DenyWrite),
		},
		Environment: SRTEnvironment{
			Passthrough: mergeStringSlices(existing.Environment.Passthrough, generated.Environment.Passthrough),
		},
		Binaries: mergeStringSlices(existing.Binaries, generated.Binaries),
	}
}

func mergeStringSlices(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

package sandbox

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var DefaultPassthroughEnv = []string{
	// Terminal/display
	"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION",
	"TERMINFO", "GHOSTTY_RESOURCES_DIR",
	// Locale
	"LANG", "LC_ALL", "LC_CTYPE",
	// User/system
	"HOME", "USER", "LOGNAME", "SHELL", "PATH",
	// Editor
	"EDITOR", "VISUAL",
	// XDG
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}

var DangerousFiles = []string{
	".gitconfig",
	".gitmodules",
	".bashrc",
	".bash_profile",
	".zshrc",
	".zprofile",
	".profile",
	".ripgreprc",
	".mcp.json",
	".sandbox.yaml",
}

func GetDangerousDirectories() []string {
	return []string{".vscode", ".idea", ".claude/commands", ".claude/agents"}
}

func NormalizeCaseForComparison(path string) string {
	_ = runtime.GOOS
	return strings.ToLower(path)
}

func ContainsGlobChars(pathPattern string) bool {
	return strings.ContainsAny(pathPattern, "*?[]")
}

func RemoveTrailingGlobSuffix(pathPattern string) string {
	return strings.TrimSuffix(pathPattern, "/**")
}

func normalizePathForBoundaryChecks(pathStr string) string {
	return filepath.ToSlash(filepath.Clean(pathStr))
}

func hasPathPrefix(pathStr string, prefix string) bool {
	if pathStr == prefix {
		return true
	}
	if prefix == "/" {
		return strings.HasPrefix(pathStr, "/")
	}
	return strings.HasPrefix(pathStr, strings.TrimSuffix(prefix, "/")+"/")
}

// IsSymlinkOutsideBoundary returns true when resolvedPath broadens path access
// beyond what originalPath suggests.
func IsSymlinkOutsideBoundary(originalPath, resolvedPath string) bool {
	normalizedOriginal := normalizePathForBoundaryChecks(originalPath)
	normalizedResolved := normalizePathForBoundaryChecks(resolvedPath)

	if normalizedResolved == normalizedOriginal {
		return false
	}

	// Allow macOS canonical aliases.
	if strings.HasPrefix(normalizedOriginal, "/tmp/") && normalizedResolved == "/private"+normalizedOriginal {
		return false
	}
	if strings.HasPrefix(normalizedOriginal, "/var/") && normalizedResolved == "/private"+normalizedOriginal {
		return false
	}
	if strings.HasPrefix(normalizedOriginal, "/private/tmp/") && normalizedResolved == normalizedOriginal {
		return false
	}
	if strings.HasPrefix(normalizedOriginal, "/private/var/") && normalizedResolved == normalizedOriginal {
		return false
	}

	if normalizedResolved == "/" {
		return true
	}

	resolvedParts := strings.Split(strings.Trim(normalizedResolved, "/"), "/")
	if len(resolvedParts) <= 1 {
		return true
	}

	// Resolved path is an ancestor of the original path.
	if hasPathPrefix(normalizedOriginal, normalizedResolved) && normalizedOriginal != normalizedResolved {
		return true
	}

	canonicalOriginal := normalizedOriginal
	if strings.HasPrefix(normalizedOriginal, "/tmp/") {
		canonicalOriginal = "/private" + normalizedOriginal
	} else if strings.HasPrefix(normalizedOriginal, "/var/") {
		canonicalOriginal = "/private" + normalizedOriginal
	}

	if canonicalOriginal != normalizedOriginal && hasPathPrefix(canonicalOriginal, normalizedResolved) && canonicalOriginal != normalizedResolved {
		return true
	}

	resolvedStartsWithOriginal := hasPathPrefix(normalizedResolved, normalizedOriginal) && normalizedResolved != normalizedOriginal
	resolvedStartsWithCanonical := canonicalOriginal != normalizedOriginal && hasPathPrefix(normalizedResolved, canonicalOriginal) && normalizedResolved != canonicalOriginal
	resolvedIsCanonical := canonicalOriginal != normalizedOriginal && normalizedResolved == canonicalOriginal
	resolvedIsSame := normalizedResolved == normalizedOriginal

	if !resolvedIsSame && !resolvedIsCanonical && !resolvedStartsWithOriginal && !resolvedStartsWithCanonical {
		return true
	}

	return false
}

func NormalizePathForSandbox(pathPattern string) string {
	if pathPattern == "" {
		return pathPattern
	}

	p := pathPattern
	home, _ := os.UserHomeDir()
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}

	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}

	if ContainsGlobChars(p) {
		staticPrefix := p
		if idx := strings.IndexAny(p, "*?[]"); idx >= 0 {
			staticPrefix = p[:idx]
		}
		if staticPrefix != "" && staticPrefix != string(filepath.Separator) {
			baseDir := ""
			if strings.HasSuffix(staticPrefix, string(filepath.Separator)) {
				baseDir = strings.TrimSuffix(staticPrefix, string(filepath.Separator))
			} else {
				baseDir = filepath.Dir(staticPrefix)
			}

			if baseDir != "" {
				if resolvedBaseDir, err := filepath.EvalSymlinks(baseDir); err == nil {
					if !IsSymlinkOutsideBoundary(baseDir, resolvedBaseDir) {
						patternSuffix := strings.TrimPrefix(p, baseDir)
						return filepath.Clean(resolvedBaseDir) + patternSuffix
					}
				}
			}
		}
		return filepath.Clean(p)
	}

	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		if !IsSymlinkOutsideBoundary(p, resolved) {
			p = resolved
		}
	}

	return filepath.Clean(p)
}

func GetDefaultWritePaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"/dev/stdout",
		"/dev/stderr",
		"/dev/null",
		"/dev/tty",
		"/dev/dtracehelper",
		"/dev/autofs_nowait",
		"/tmp/claude",
		"/private/tmp/claude",
		filepath.Join(home, ".npm", "_logs"),
		filepath.Join(home, ".claude", "debug"),
	}
}

func GenerateProxyEnvVars(httpProxyPort, socksProxyPort int, passthroughEnv []string, extraEnv ...map[string]string) []string {
	tmpdir := os.Getenv("CLAUDE_TMPDIR")
	if tmpdir == "" {
		tmpdir = "/tmp/claude"
	}
	envVars := []string{
		"SANDBOX_RUNTIME=1",
		"TMPDIR=" + tmpdir,
		"ATUIN_DISABLED=true",
		"HISTFILE=" + filepath.Join(tmpdir, ".shell_history"),
	}

	if httpProxyPort != 0 || socksProxyPort != 0 {
		noProxy := strings.Join([]string{
			"localhost",
			"127.0.0.1",
			"::1",
			"*.local",
			".local",
			"169.254.0.0/16",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
		}, ",")
		envVars = append(envVars, "NO_PROXY="+noProxy, "no_proxy="+noProxy)

		if httpProxyPort != 0 {
			httpProxy := "http://localhost:" + itoa(httpProxyPort)
			envVars = append(envVars,
				"HTTP_PROXY="+httpProxy,
				"HTTPS_PROXY="+httpProxy,
				"http_proxy="+httpProxy,
				"https_proxy="+httpProxy,
			)
		}

		if socksProxyPort != 0 {
			socksProxy := "socks5h://localhost:" + itoa(socksProxyPort)
			envVars = append(envVars,
				"ALL_PROXY="+socksProxy,
				"all_proxy="+socksProxy,
				"FTP_PROXY="+socksProxy,
				"ftp_proxy="+socksProxy,
				"RSYNC_PROXY=localhost:"+itoa(socksProxyPort),
				"GRPC_PROXY="+socksProxy,
				"grpc_proxy="+socksProxy,
			)
			if runtime.GOOS == "darwin" {
				envVars = append(envVars,
					"GIT_SSH_COMMAND=ssh -o ProxyCommand='nc -X 5 -x localhost:"+itoa(socksProxyPort)+" %h %p'",
				)
			}
			if httpProxyPort != 0 {
				envVars = append(envVars,
					"DOCKER_HTTP_PROXY=http://localhost:"+itoa(httpProxyPort),
					"DOCKER_HTTPS_PROXY=http://localhost:"+itoa(httpProxyPort),
					"CLOUDSDK_PROXY_TYPE=https",
					"CLOUDSDK_PROXY_ADDRESS=localhost",
					"CLOUDSDK_PROXY_PORT="+itoa(httpProxyPort),
				)
			} else {
				envVars = append(envVars,
					"DOCKER_HTTP_PROXY=http://localhost:"+itoa(socksProxyPort),
					"DOCKER_HTTPS_PROXY=http://localhost:"+itoa(socksProxyPort),
				)
			}
		}
	}

	// Build set of already-defined keys for dedup
	setKeys := make(map[string]bool, len(envVars))
	for _, kv := range envVars {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			setKeys[kv[:idx]] = true
		}
	}

	// Passthrough: defaults + caller-specified
	allPassthrough := mergeStringSlicesDedup(DefaultPassthroughEnv, passthroughEnv)
	for _, name := range allPassthrough {
		if setKeys[name] {
			continue
		}
		if val := os.Getenv(name); val != "" {
			envVars = append(envVars, name+"="+val)
			setKeys[name] = true
		}
	}

	// Explicit env overrides everything
	for _, m := range extraEnv {
		for k, v := range m {
			envVars = append(envVars, k+"="+v)
		}
	}

	return envVars
}

func EncodeSandboxedCommand(command string) string {
	if len(command) > 100 {
		command = command[:100]
	}
	return base64.StdEncoding.EncodeToString([]byte(command))
}

func DecodeSandboxedCommand(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func GlobToRegex(globPattern string) string {
	var b strings.Builder
	for _, r := range globPattern {
		switch r {
		case '.', '^', '$', '+', '{', '}', '(', ')', '|', '\\':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	p := b.String()

	// Escape a trailing unclosed '[' so it is treated literally.
	lastOpen := -1
	for i, r := range p {
		switch r {
		case '[':
			lastOpen = i
		case ']':
			lastOpen = -1
		}
	}
	if lastOpen >= 0 {
		p = p[:lastOpen] + "\\" + p[lastOpen:]
	}

	p = strings.ReplaceAll(p, "**/", "__GLOBSTAR_SLASH__")
	p = strings.ReplaceAll(p, "**", "__GLOBSTAR__")
	p = strings.ReplaceAll(p, "*", "[^/]*")
	p = strings.ReplaceAll(p, "?", "[^/]")
	p = strings.ReplaceAll(p, "__GLOBSTAR_SLASH__", "(.*/)?")
	p = strings.ReplaceAll(p, "__GLOBSTAR__", ".*")
	return "^" + p + "$"
}

func itoa(v int) string {
	return strconv.Itoa(v)
}

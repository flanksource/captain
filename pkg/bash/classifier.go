package bash

import (
	"os"
	"path/filepath"
	"strings"
)

var systemPaths = []string{
	"/etc/",
	"/usr/",
	"/var/",
	"/bin/",
	"/sbin/",
	"/boot/",
	"/sys/",
	"/proc/",
	"/lib/",
	"/lib64/",
	"/opt/",
}

// PathClassifier handles classification of file paths for safety
type PathClassifier struct {
	cwd     string
	homeDir string
	config  *Config
}

// NewPathClassifier creates a new path classifier with context
func NewPathClassifier(cwd string, config *Config) *PathClassifier {
	homeDir, _ := os.UserHomeDir()
	return &PathClassifier{
		cwd:     cwd,
		homeDir: homeDir,
		config:  config,
	}
}

// ClassifyPath determines if a path is safe for writing
func (pc *PathClassifier) ClassifyPath(path string) PathClassification {
	if path == "" {
		return PathClassification{Path: path, IsSafe: true, Reason: "Empty path"}
	}

	if path == "/dev/null" {
		return PathClassification{Path: path, IsSafe: true, Reason: "Standard null device"}
	}

	resolved := pc.resolvePath(path)

	if pc.config != nil {
		for _, safePath := range pc.config.SafePaths {
			if pc.matchesPattern(resolved, safePath) {
				return PathClassification{Path: path, IsSafe: true, Reason: "Matches configured safe path: " + safePath}
			}
		}
	}

	if strings.HasPrefix(resolved, "/tmp/") || resolved == "/tmp" {
		return PathClassification{Path: path, IsSafe: true, Reason: "Temporary directory"}
	}

	if strings.Contains(path, "..") {
		return PathClassification{Path: path, IsSafe: false, Reason: "Parent directory traversal detected"}
	}

	for _, sysPath := range systemPaths {
		if strings.HasPrefix(resolved, sysPath) {
			return PathClassification{Path: path, IsSafe: false, Reason: "System directory: " + sysPath}
		}
	}

	if pc.homeDir != "" && strings.HasPrefix(resolved, pc.homeDir) {
		if resolved == pc.homeDir || strings.HasPrefix(resolved, pc.homeDir+"/") {
			if !strings.HasPrefix(resolved, pc.cwd) {
				return PathClassification{Path: path, IsSafe: false, Reason: "Home directory write outside CWD"}
			}
		}
	}

	if !filepath.IsAbs(resolved) {
		return PathClassification{Path: path, IsSafe: true, Reason: "Relative path in current working directory"}
	}

	if strings.HasPrefix(resolved, pc.cwd+"/") || resolved == pc.cwd {
		return PathClassification{Path: path, IsSafe: true, Reason: "Within current working directory"}
	}

	return PathClassification{Path: path, IsSafe: false, Reason: "Absolute path outside CWD and not in safe list"}
}

func (pc *PathClassifier) resolvePath(path string) string {
	resolved := path

	if pc.homeDir != "" {
		resolved = strings.ReplaceAll(resolved, "$HOME", pc.homeDir)
		if strings.HasPrefix(resolved, "~/") {
			resolved = filepath.Join(pc.homeDir, resolved[2:])
		} else if resolved == "~" {
			resolved = pc.homeDir
		}
	}

	if pc.cwd != "" {
		resolved = strings.ReplaceAll(resolved, "$PWD", pc.cwd)
		resolved = strings.ReplaceAll(resolved, "$(pwd)", pc.cwd)
	}

	return filepath.Clean(resolved)
}

func (pc *PathClassifier) matchesPattern(path, pattern string) bool {
	matched, err := filepath.Match(pattern, path)
	if err != nil {
		return false
	}
	return matched
}

// IsDynamicPath checks if a path contains unresolved variables or command substitutions
func IsDynamicPath(path string) bool {
	if strings.Contains(path, "$") &&
		!strings.Contains(path, "$HOME") &&
		!strings.Contains(path, "$PWD") &&
		!strings.Contains(path, "$(pwd)") {
		return true
	}
	if strings.Contains(path, "$(") || strings.Contains(path, "`") {
		return true
	}
	return false
}
